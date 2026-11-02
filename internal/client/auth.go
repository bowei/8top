package client

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc/credentials"
	authv1 "k8s.io/api/authentication/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

const (
	// serviceDNSSignerName and liveBundleSelector select the
	// ClusterTrustBundles in-cluster clients use to verify ateapi.
	serviceDNSSignerName = "servicedns.podcert.ate.dev/identity"
	liveBundleSelector   = "podcert.ate.dev/canarying=live"

	// tokenLifetime is the requested lifetime of a minted token, and
	// tokenRefreshBefore how long before expiry it is replaced. 8top is
	// long-running, so unlike `kubectl ate` it must refresh the token.
	tokenLifetime      = time.Hour
	tokenRefreshBefore = 5 * time.Minute
)

// serverTLSConfig returns a TLS config that verifies serverName against the
// live servicedns ClusterTrustBundles.
func serverTLSConfig(ctx context.Context, kube kubernetes.Interface, serverName string) (*tls.Config, error) {
	bundles, err := listTrustBundles(ctx, kube)
	if err != nil {
		return nil, fmt.Errorf("failed to list ClusterTrustBundles: %w", err)
	}
	pool := x509.NewCertPool()
	found := false
	for _, b := range bundles {
		if b.signer != serviceDNSSignerName {
			continue
		}
		if !pool.AppendCertsFromPEM([]byte(b.pem)) {
			return nil, fmt.Errorf("ClusterTrustBundle %q contains no valid certificates", b.name)
		}
		found = true
	}
	if !found {
		return nil, fmt.Errorf("no live ClusterTrustBundle found for signer %q", serviceDNSSignerName)
	}
	return &tls.Config{
		MinVersion: tls.VersionTLS13,
		RootCAs:    pool,
		ServerName: serverName,
	}, nil
}

type trustBundle struct {
	name, signer, pem string
}

// listTrustBundles lists live bundles from the v1 API, falling back to
// v1beta1 on clusters that do not serve v1.
func listTrustBundles(ctx context.Context, kube kubernetes.Interface) ([]trustBundle, error) {
	opts := metav1.ListOptions{LabelSelector: liveBundleSelector}
	var out []trustBundle
	v1, err := kube.CertificatesV1().ClusterTrustBundles().List(ctx, opts)
	if err == nil {
		for _, b := range v1.Items {
			out = append(out, trustBundle{name: b.Name, signer: b.Spec.SignerName, pem: b.Spec.TrustBundle})
		}
		return out, nil
	}
	if !apierrors.IsNotFound(err) {
		return nil, err
	}
	beta, err := kube.CertificatesV1beta1().ClusterTrustBundles().List(ctx, opts)
	if err != nil {
		return nil, err
	}
	for _, b := range beta.Items {
		out = append(out, trustBundle{name: b.Name, signer: b.Spec.SignerName, pem: b.Spec.TrustBundle})
	}
	return out, nil
}

// tokenCredentials returns the per-RPC credentials for tokenFile: "-" reads a
// token from stdin once, a path re-reads the file on every RPC, and ""
// mints (and refreshes) a token for the client ServiceAccount.
//
// The minted token authenticates as the ServiceAccount, not as whoever runs
// 8top, the same as `kubectl ate`.
func tokenCredentials(ctx context.Context, kube kubernetes.Interface, tokenFile string, stdin io.Reader) (credentials.PerRPCCredentials, error) {
	switch tokenFile {
	case "-":
		creds, err := readBearerToken(stdin)
		if err != nil {
			return nil, fmt.Errorf("read bearer token from stdin: %w", err)
		}
		return creds, nil
	case "":
		creds := &mintedTokenCreds{
			kube:      kube,
			namespace: systemNamespace(),
			account:   clientServiceAccount(),
			audience:  apiServerName(),
			now:       time.Now,
		}
		// Mint once up front so misconfiguration fails at startup.
		if _, err := creds.GetRequestMetadata(ctx); err != nil {
			return nil, err
		}
		return creds, nil
	default:
		return fileBearerTokenCreds(tokenFile), nil
	}
}

func readBearerToken(r io.Reader) (bearerTokenCreds, error) {
	b, err := io.ReadAll(r)
	if err != nil {
		return "", err
	}
	token := strings.TrimSpace(string(b))
	if token == "" {
		return "", fmt.Errorf("bearer token is empty")
	}
	return bearerTokenCreds(token), nil
}

func authHeader(token string) map[string]string {
	return map[string]string{"authorization": "Bearer " + token}
}

// bearerTokenCreds is a fixed bearer token.
type bearerTokenCreds string

func (c bearerTokenCreds) GetRequestMetadata(context.Context, ...string) (map[string]string, error) {
	if c == "" {
		return nil, fmt.Errorf("bearer token is empty")
	}
	return authHeader(string(c)), nil
}

func (c bearerTokenCreds) RequireTransportSecurity() bool { return true }

// fileBearerTokenCreds reads the token from a file on every RPC so rotated
// tokens are picked up.
type fileBearerTokenCreds string

func (c fileBearerTokenCreds) GetRequestMetadata(context.Context, ...string) (map[string]string, error) {
	b, err := os.ReadFile(string(c))
	if err != nil {
		return nil, fmt.Errorf("read bearer token file %q: %w", c, err)
	}
	token := strings.TrimSpace(string(b))
	if token == "" {
		return nil, fmt.Errorf("bearer token file %q is empty", c)
	}
	return authHeader(token), nil
}

func (c fileBearerTokenCreds) RequireTransportSecurity() bool { return true }

// mintedTokenCreds mints ServiceAccount tokens via the TokenRequest API and
// replaces them shortly before they expire.
type mintedTokenCreds struct {
	kube                         kubernetes.Interface
	namespace, account, audience string
	now                          func() time.Time

	mu     sync.Mutex
	token  string
	expiry time.Time
}

func (c *mintedTokenCreds) GetRequestMetadata(ctx context.Context, _ ...string) (map[string]string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.token == "" || !c.now().Before(c.expiry.Add(-tokenRefreshBefore)) {
		if err := c.mintLocked(ctx); err != nil {
			return nil, err
		}
	}
	return authHeader(c.token), nil
}

func (c *mintedTokenCreds) RequireTransportSecurity() bool { return true }

func (c *mintedTokenCreds) mintLocked(ctx context.Context) error {
	seconds := int64(tokenLifetime / time.Second)
	req := &authv1.TokenRequest{
		Spec: authv1.TokenRequestSpec{
			Audiences:         []string{c.audience},
			ExpirationSeconds: &seconds,
		},
	}
	resp, err := c.kube.CoreV1().ServiceAccounts(c.namespace).CreateToken(ctx, c.account, req, metav1.CreateOptions{})
	if err != nil {
		return fmt.Errorf("failed to request ateapi bearer token: %w", err)
	}
	if resp.Status.Token == "" {
		return fmt.Errorf("failed to request ateapi bearer token: token response was empty")
	}
	c.token = resp.Status.Token
	c.expiry = resp.Status.ExpirationTimestamp.Time
	if c.expiry.IsZero() {
		c.expiry = c.now().Add(tokenLifetime)
	}
	return nil
}
