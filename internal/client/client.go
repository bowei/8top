// Package client connects 8top to a Substrate cluster the same way
// `kubectl ate` does: it loads a kubeconfig, verifies the ateapi serving
// certificate against the cluster's ClusterTrustBundles, authenticates with a
// bearer token, and either dials an explicit endpoint or port-forwards to a
// ready ate-api-server pod.
package client

import (
	"context"
	"fmt"
	"os"

	"go.ate.dev/substrate/api/ateapi/v1"
	"go.ate.dev/substrate/pkg/client/clientset/versioned"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

// NamespaceEnv overrides the namespace substrate is installed in.
const NamespaceEnv = "ATE_NAMESPACE"

// APIServiceEnv and ClientServiceAccountEnv override the ateapi Service and
// the ServiceAccount the bearer token is minted from. They match the
// variables `kubectl ate` reads.
const (
	APIServiceEnv           = "ATE_API_SERVICE_NAME"
	ClientServiceAccountEnv = "ATE_CLIENT_SERVICE_ACCOUNT"
)

// Defaults mirrored from substrate's internal/installdefaults.
const (
	defaultSystemNamespace      = "ate-system"
	defaultAPIServiceName       = "api"
	defaultClientServiceAccount = "ate-client"

	// apiServicePort is the Service port ateapi serves gRPC on.
	apiServicePort = 443
)

// roundRobinServiceConfig spreads RPCs over every address the resolver
// returns when dialing an explicit endpoint (e.g. a headless Service).
const roundRobinServiceConfig = `{"loadBalancingConfig": [{"round_robin":{}}]}`

// Options selects the cluster to connect to. The fields correspond to the
// `kubectl ate` global flags of the same names.
type Options struct {
	// Kubeconfig is the kubeconfig path. "" uses the default loading rules
	// (~/.kube/config, $KUBECONFIG).
	Kubeconfig string
	// Context is the kubeconfig context. "" uses the current context.
	Context string
	// Endpoint, if non-empty, is dialed directly instead of port-forwarding.
	Endpoint string
	// TokenFile is a bearer token file for ateapi, or "-" to read it from
	// stdin. "" mints a token for the ate-client ServiceAccount.
	TokenFile string
}

// Client holds the connections 8top uses.
type Client struct {
	// Ate is the Substrate control API.
	Ate ateapi.ControlClient
	// Kube is the Kubernetes API.
	Kube kubernetes.Interface
	// Pools is the clientset for substrate CRDs (WorkerPool).
	Pools versioned.Interface
	// Context is the resolved kubeconfig context name.
	Context string
	// Endpoint is a human-readable description of the ateapi endpoint.
	Endpoint string

	conn *grpc.ClientConn
	fwd  *forwarder
}

// New connects to the cluster selected by opts. With no Endpoint it
// port-forwards to a ready ate-api-server pod; if that pod goes away the
// tunnel is re-established against another ready pod on the next gRPC
// reconnect.
func New(ctx context.Context, opts Options) (*Client, error) {
	cc := kubeClientConfig(opts.Kubeconfig, opts.Context)
	raw, err := cc.RawConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to load kubeconfig: %w", err)
	}
	config, err := cc.ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to load kubeconfig: %w", err)
	}
	// Older clusters use the deprecated beta ClusterTrustBundle API.
	// Suppress its warnings when falling back from v1.
	config.WarningHandlerWithContext = &rest.NoWarnings{}

	kube, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("failed to create k8s client: %w", err)
	}
	pools, err := versioned.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("failed to create substrate CRD client: %w", err)
	}

	// Verify the server before attaching the bearer token: the token must
	// never be sent over an unauthenticated channel.
	tlsCfg, err := serverTLSConfig(ctx, kube, apiServerName())
	if err != nil {
		return nil, err
	}
	creds, err := tokenCredentials(ctx, kube, opts.TokenFile, os.Stdin)
	if err != nil {
		return nil, err
	}
	dialOpts := []grpc.DialOption{
		grpc.WithTransportCredentials(credentials.NewTLS(tlsCfg)),
		grpc.WithPerRPCCredentials(creds),
	}

	c := &Client{
		Kube:     kube,
		Pools:    pools,
		Context:  resolveContext(raw, opts.Context),
		Endpoint: opts.Endpoint,
	}
	target := opts.Endpoint
	if opts.Endpoint == "" {
		c.fwd = newForwarder(config, kube, systemNamespace(), apiServiceName(), apiServicePort)
		pod, err := c.fwd.start(ctx)
		if err != nil {
			return nil, err
		}
		c.Endpoint = fmt.Sprintf("port-forward %s/%s", systemNamespace(), pod)
		// The forwarder ignores the address; passthrough skips DNS
		// resolution of a name that only resolves in-cluster.
		target = "passthrough:///" + apiServerName()
		dialOpts = append(dialOpts, grpc.WithContextDialer(c.fwd.dial))
	} else {
		dialOpts = append(dialOpts, grpc.WithDefaultServiceConfig(roundRobinServiceConfig))
	}

	conn, err := grpc.NewClient(target, dialOpts...)
	if err != nil {
		c.Close()
		return nil, fmt.Errorf("failed to create gRPC client for %s: %w", c.Endpoint, err)
	}
	c.conn = conn
	c.Ate = ateapi.NewControlClient(conn)
	return c, nil
}

// Close closes the gRPC connection and stops any port-forward.
func (c *Client) Close() {
	if c.conn != nil {
		c.conn.Close()
	}
	if c.fwd != nil {
		c.fwd.close()
	}
}

// kubeClientConfig builds a kubeconfig loader using the default loading rules
// with optional path and context overrides.
func kubeClientConfig(kubeconfigPath, k8sContext string) clientcmd.ClientConfig {
	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	loadingRules.ExplicitPath = kubeconfigPath
	overrides := &clientcmd.ConfigOverrides{CurrentContext: k8sContext}
	return clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, overrides)
}

// resolveContext returns the context name that will be used: the override if
// set, otherwise the kubeconfig's current context.
func resolveContext(raw clientcmdapi.Config, override string) string {
	if override != "" {
		return override
	}
	return raw.CurrentContext
}

func envOr(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}

// systemNamespace is the namespace ateapi runs in.
func systemNamespace() string { return envOr(NamespaceEnv, defaultSystemNamespace) }

// apiServiceName is the Service that fronts ateapi.
func apiServiceName() string { return envOr(APIServiceEnv, defaultAPIServiceName) }

// clientServiceAccount is the ServiceAccount the bearer token is minted from.
func clientServiceAccount() string {
	return envOr(ClientServiceAccountEnv, defaultClientServiceAccount)
}

// apiServerName is the in-cluster DNS name of the ateapi Service. It is both
// the name on ateapi's serving cert and the audience of the minted token.
func apiServerName() string {
	return fmt.Sprintf("%s.%s.svc", apiServiceName(), systemNamespace())
}
