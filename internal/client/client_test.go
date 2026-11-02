package client

import (
	"testing"

	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

func TestResolveContext(t *testing.T) {
	for _, tc := range []struct {
		name     string
		current  string
		override string
		want     string
	}{
		{name: "current", current: "kind-ate", want: "kind-ate"},
		{name: "override", current: "kind-ate", override: "prod", want: "prod"},
		{name: "none", want: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := resolveContext(clientcmdapi.Config{CurrentContext: tc.current}, tc.override)
			if got != tc.want {
				t.Errorf("resolveContext() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestEnvOverrides(t *testing.T) {
	for _, tc := range []struct {
		name                       string
		env                        map[string]string
		wantServer, wantNS, wantSA string
	}{
		{
			name:       "defaults",
			wantServer: "api.ate-system.svc", wantNS: "ate-system", wantSA: "ate-client",
		},
		{
			name: "overrides",
			env: map[string]string{
				NamespaceEnv:            "sub",
				APIServiceEnv:           "x-api",
				ClientServiceAccountEnv: "x-client",
			},
			wantServer: "x-api.sub.svc", wantNS: "sub", wantSA: "x-client",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, k := range []string{NamespaceEnv, APIServiceEnv, ClientServiceAccountEnv} {
				t.Setenv(k, tc.env[k])
			}
			if got := apiServerName(); got != tc.wantServer {
				t.Errorf("apiServerName() = %q, want %q", got, tc.wantServer)
			}
			if got := systemNamespace(); got != tc.wantNS {
				t.Errorf("systemNamespace() = %q, want %q", got, tc.wantNS)
			}
			if got := clientServiceAccount(); got != tc.wantSA {
				t.Errorf("clientServiceAccount() = %q, want %q", got, tc.wantSA)
			}
		})
	}
}

func TestKubeClientConfig(t *testing.T) {
	const kubeconfig = `apiVersion: v1
kind: Config
current-context: a
clusters:
- name: c
  cluster: {server: "https://example.invalid"}
users:
- name: u
  user: {token: t}
contexts:
- name: a
  context: {cluster: c, user: u}
- name: b
  context: {cluster: c, user: u}
`
	path := t.TempDir() + "/config"
	if err := writeFile(path, kubeconfig); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name     string
		override string
		want     string
		wantErr  bool
	}{
		{name: "current", want: "a"},
		{name: "override", override: "b", want: "b"},
		{name: "missing context", override: "nope", wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cc := kubeClientConfig(path, tc.override)
			raw, err := cc.RawConfig()
			if err != nil {
				t.Fatal(err)
			}
			_, err = cc.ClientConfig()
			if gotErr := err != nil; gotErr != tc.wantErr {
				t.Fatalf("ClientConfig() err = %v, wantErr %v", err, tc.wantErr)
			}
			if tc.wantErr {
				return
			}
			if got := resolveContext(raw, tc.override); got != tc.want {
				t.Errorf("context = %q, want %q", got, tc.want)
			}
		})
	}
}
