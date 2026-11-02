package client

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"os"
	"strings"
	"testing"
	"time"

	authv1 "k8s.io/api/authentication/v1"
	certsv1 "k8s.io/api/certificates/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

func writeFile(path, content string) error {
	return os.WriteFile(path, []byte(content), 0o600)
}

func TestReadBearerToken(t *testing.T) {
	for _, tc := range []struct {
		name    string
		in      string
		want    string
		wantErr bool
	}{
		{name: "plain", in: "abc", want: "abc"},
		{name: "trailing newline", in: "abc\n", want: "abc"},
		{name: "surrounding space", in: "  abc \t\n", want: "abc"},
		{name: "empty", in: "", wantErr: true},
		{name: "whitespace only", in: " \n", wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := readBearerToken(strings.NewReader(tc.in))
			if gotErr := err != nil; gotErr != tc.wantErr {
				t.Fatalf("readBearerToken() err = %v, wantErr %v", err, tc.wantErr)
			}
			if string(got) != tc.want {
				t.Errorf("readBearerToken() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestFileBearerTokenCreds(t *testing.T) {
	dir := t.TempDir()
	for _, tc := range []struct {
		name    string
		content *string // nil: file does not exist
		want    string
		wantErr bool
	}{
		{name: "token", content: ptr("tok\n"), want: "Bearer tok"},
		{name: "empty", content: ptr("\n"), wantErr: true},
		{name: "missing", wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := dir + "/" + tc.name
			if tc.content != nil {
				if err := writeFile(path, *tc.content); err != nil {
					t.Fatal(err)
				}
			}
			md, err := fileBearerTokenCreds(path).GetRequestMetadata(context.Background())
			if gotErr := err != nil; gotErr != tc.wantErr {
				t.Fatalf("GetRequestMetadata() err = %v, wantErr %v", err, tc.wantErr)
			}
			if got := md["authorization"]; got != tc.want {
				t.Errorf("authorization = %q, want %q", got, tc.want)
			}
		})
	}
}

func ptr[T any](v T) *T { return &v }

// tokenReactor fakes the TokenRequest API, recording each request and
// issuing tokens "tok-1", "tok-2", ... that expire at expiry.
type tokenReactor struct {
	expiry time.Time
	calls  []*authv1.TokenRequest
	ns, sa []string
	err    error
}

func (r *tokenReactor) install(kube *fake.Clientset) {
	kube.PrependReactor("create", "serviceaccounts", func(a ktesting.Action) (bool, runtime.Object, error) {
		ca, ok := a.(ktesting.CreateAction)
		if !ok || a.GetSubresource() != "token" {
			return false, nil, nil
		}
		if r.err != nil {
			return true, nil, r.err
		}
		req := ca.GetObject().(*authv1.TokenRequest)
		r.calls = append(r.calls, req)
		r.ns = append(r.ns, a.GetNamespace())
		r.sa = append(r.sa, ca.(ktesting.CreateActionImpl).Name)
		resp := req.DeepCopy()
		resp.Status = authv1.TokenRequestStatus{
			Token:               fmt.Sprintf("tok-%d", len(r.calls)),
			ExpirationTimestamp: metav1.NewTime(r.expiry),
		}
		return true, resp, nil
	})
}

func TestTokenCredentials(t *testing.T) {
	for _, k := range []string{NamespaceEnv, APIServiceEnv, ClientServiceAccountEnv} {
		t.Setenv(k, "")
	}
	tokenPath := t.TempDir() + "/token"
	if err := writeFile(tokenPath, "from-file\n"); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name      string
		tokenFile string
		stdin     string
		mintErr   error
		want      string
		wantErr   bool
		wantMints int
	}{
		{name: "stdin", tokenFile: "-", stdin: "from-stdin\n", want: "Bearer from-stdin"},
		{name: "stdin empty", tokenFile: "-", stdin: "", wantErr: true},
		{name: "file", tokenFile: tokenPath, want: "Bearer from-file"},
		{name: "minted", tokenFile: "", want: "Bearer tok-1", wantMints: 1},
		{name: "mint fails", tokenFile: "", mintErr: fmt.Errorf("forbidden"), wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			kube := fake.NewClientset()
			r := &tokenReactor{expiry: time.Now().Add(time.Hour), err: tc.mintErr}
			r.install(kube)
			ctx := context.Background()
			creds, err := tokenCredentials(ctx, kube, tc.tokenFile, strings.NewReader(tc.stdin))
			if gotErr := err != nil; gotErr != tc.wantErr {
				t.Fatalf("tokenCredentials() err = %v, wantErr %v", err, tc.wantErr)
			}
			if tc.wantErr {
				return
			}
			if !creds.RequireTransportSecurity() {
				t.Errorf("RequireTransportSecurity() = false, want true")
			}
			md, err := creds.GetRequestMetadata(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if got := md["authorization"]; got != tc.want {
				t.Errorf("authorization = %q, want %q", got, tc.want)
			}
			if got := len(r.calls); got != tc.wantMints {
				t.Errorf("mints = %d, want %d", got, tc.wantMints)
			}
			if tc.wantMints > 0 {
				if got, want := r.ns[0], "ate-system"; got != want {
					t.Errorf("namespace = %q, want %q", got, want)
				}
				if got, want := r.sa[0], "ate-client"; got != want {
					t.Errorf("service account = %q, want %q", got, want)
				}
				if got, want := strings.Join(r.calls[0].Spec.Audiences, ","), "api.ate-system.svc"; got != want {
					t.Errorf("audiences = %q, want %q", got, want)
				}
			}
		})
	}
}

func TestMintedTokenRefresh(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name    string
		elapsed time.Duration
		want    string
	}{
		{name: "fresh", elapsed: 0, want: "Bearer tok-1"},
		{name: "before refresh window", elapsed: tokenLifetime - tokenRefreshBefore - time.Second, want: "Bearer tok-1"},
		{name: "in refresh window", elapsed: tokenLifetime - tokenRefreshBefore, want: "Bearer tok-2"},
		{name: "expired", elapsed: 2 * tokenLifetime, want: "Bearer tok-2"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			kube := fake.NewClientset()
			r := &tokenReactor{expiry: start.Add(tokenLifetime)}
			r.install(kube)
			now := start
			creds := &mintedTokenCreds{kube: kube, namespace: "ns", account: "sa", audience: "aud", now: func() time.Time { return now }}
			ctx := context.Background()
			if _, err := creds.GetRequestMetadata(ctx); err != nil {
				t.Fatal(err)
			}
			now = start.Add(tc.elapsed)
			md, err := creds.GetRequestMetadata(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if got := md["authorization"]; got != tc.want {
				t.Errorf("authorization = %q, want %q", got, tc.want)
			}
		})
	}
}

func testCAPEM(t *testing.T) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}

func TestServerTLSConfig(t *testing.T) {
	caPEM := testCAPEM(t)
	live := map[string]string{"podcert.ate.dev/canarying": "live"}
	bundle := func(name, signer, pem string, labels map[string]string) *certsv1.ClusterTrustBundle {
		return &certsv1.ClusterTrustBundle{
			ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels},
			Spec:       certsv1.ClusterTrustBundleSpec{SignerName: signer, TrustBundle: pem},
		}
	}
	for _, tc := range []struct {
		name    string
		objs    []runtime.Object
		wantErr string
	}{
		{
			name: "live bundle",
			objs: []runtime.Object{bundle("b", serviceDNSSignerName, caPEM, live)},
		},
		{
			name: "other signers ignored",
			objs: []runtime.Object{
				bundle("other", "example.com/other", "garbage", live),
				bundle("b", serviceDNSSignerName, caPEM, live),
			},
		},
		{name: "none", wantErr: "no live ClusterTrustBundle"},
		{
			name:    "not live",
			objs:    []runtime.Object{bundle("b", serviceDNSSignerName, caPEM, nil)},
			wantErr: "no live ClusterTrustBundle",
		},
		{
			name:    "bad pem",
			objs:    []runtime.Object{bundle("b", serviceDNSSignerName, "garbage", live)},
			wantErr: "contains no valid certificates",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			kube := fake.NewClientset(tc.objs...)
			cfg, err := serverTLSConfig(context.Background(), kube, "api.ate-system.svc")
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("serverTLSConfig() err = %v, want containing %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got, want := cfg.ServerName, "api.ate-system.svc"; got != want {
				t.Errorf("ServerName = %q, want %q", got, want)
			}
			if cfg.RootCAs == nil {
				t.Errorf("RootCAs = nil, want pool")
			}
		})
	}
}
