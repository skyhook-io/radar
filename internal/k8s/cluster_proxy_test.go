package k8s

import (
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"k8s.io/client-go/rest"

	"github.com/skyhook-io/radar/internal/settings"
)

func TestParseClusterProxyURL(t *testing.T) {
	for _, raw := range []string{"http://proxy:3128", "https://user:pass@proxy.example.com:8443", "socks5://127.0.0.1:1080", " socks5://bastion:1080 "} {
		if _, err := ParseClusterProxyURL(raw); err != nil {
			t.Errorf("ParseClusterProxyURL(%q) = %v, want accepted", raw, err)
		}
	}
	for _, raw := range []string{"", "proxy:3128", "ftp://proxy:21", "socks4://proxy:1080", "http://", "http://:3128"} {
		if _, err := ParseClusterProxyURL(raw); err == nil {
			t.Errorf("ParseClusterProxyURL(%q) accepted, want rejected", raw)
		}
	}
}

func TestApplyClusterProxy(t *testing.T) {
	saved := map[string]string{"via-socks": "socks5://127.0.0.1:1080", "broken": "ftp://x"}
	orig := savedClusterProxy
	savedClusterProxy = func(name string) (string, error) { return saved[name], nil }
	t.Cleanup(func() { savedClusterProxy = orig })

	req, _ := http.NewRequest(http.MethodGet, "https://10.0.0.1:6443/version", nil)

	config := &rest.Config{}
	if applied, err := applyClusterProxy(config, "via-socks", "via-socks"); err != nil || applied != "socks5://127.0.0.1:1080" {
		t.Fatalf("applied = %q, %v", applied, err)
	}
	if config.Proxy == nil {
		t.Fatal("saved proxy not applied")
	}
	if u, _ := config.Proxy(req); u == nil || u.String() != "socks5://127.0.0.1:1080" {
		t.Fatalf("proxy = %v, want socks5://127.0.0.1:1080", u)
	}

	kubeconfigProxy := func(*http.Request) (*url.URL, error) { return nil, nil }
	untouched := &rest.Config{Proxy: kubeconfigProxy}
	if applied, err := applyClusterProxy(untouched, "no-proxy-saved", "no-proxy-saved"); err != nil || applied != "" || untouched.Proxy == nil {
		t.Fatalf("a context without a saved proxy must keep the kubeconfig's: err=%v", err)
	}

	if _, err := applyClusterProxy(&rest.Config{}, "broken", "broken"); err == nil {
		t.Fatal("an invalid saved proxy must fail the connection, not connect directly")
	}
}

// A context's display name gains a source suffix when another kubeconfig adds
// one of the same name; the binding follows the file and in-file name instead,
// so a saved proxy neither moves to the other cluster nor gets lost.
func TestContextBindingForSeparatesSameNamedContexts(t *testing.T) {
	t.Cleanup(SetTestRegistryEntry("prod", "/kube/a.yaml", "prod"))
	t.Cleanup(SetTestRegistryEntry("prod (b)", "/kube/b.yaml", "prod"))

	a, b := ContextBindingFor("prod"), ContextBindingFor("prod (b)")
	if a == "" || b == "" || a == b {
		t.Fatalf("bindings = %q, %q; want two distinct non-empty bindings", a, b)
	}
	if a != sourceContextBinding("/kube/a.yaml", "prod") {
		t.Fatal("binding must follow the source file and in-file name")
	}
	if ContextBindingFor("unknown") != "" {
		t.Fatal("an unknown context has no binding")
	}
}

// The key a proxy is saved under (ContextBindingFor, used by the API) must be
// the one prepareContext reads when it connects, in single-file and
// multi-file mode alike.
func TestSavedProxyReachesPreparedContext(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Cleanup(SetTestLocalMode())
	kubeconfig := filepath.Join(t.TempDir(), "config")
	if err := os.WriteFile(kubeconfig, []byte(`apiVersion: v1
kind: Config
clusters: [{name: c, cluster: {server: "https://10.0.0.1:6443"}}]
users: [{name: u, user: {token: t}}]
contexts: [{name: prod, context: {cluster: c, user: u}}]
current-context: prod
`), 0o600); err != nil {
		t.Fatal(err)
	}
	clientMu.Lock()
	prevPath, prevRegistry := kubeconfigPath, contextRegistry
	clientMu.Unlock()
	t.Cleanup(func() {
		clientMu.Lock()
		kubeconfigPath, contextRegistry = prevPath, prevRegistry
		clientMu.Unlock()
	})

	for _, tc := range []struct {
		mode, name string
		registry   map[string]contextEntry
	}{
		{"single file", "prod", nil},
		{"multi file", "prod (b)", map[string]contextEntry{"prod (b)": {SourceFile: kubeconfig, InFileName: "prod"}}},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			clientMu.Lock()
			kubeconfigPath, contextRegistry = kubeconfig, tc.registry
			clientMu.Unlock()
			if err := settings.SetClusterProxy(ContextBindingFor(tc.name), "socks5://127.0.0.1:1080"); err != nil {
				t.Fatal(err)
			}
			p, err := prepareContext(tc.name)
			if err != nil {
				t.Fatal(err)
			}
			req, _ := http.NewRequest(http.MethodGet, "https://10.0.0.1:6443/version", nil)
			if u, _ := p.config.Proxy(req); p.proxy == "" || u == nil || u.String() != "socks5://127.0.0.1:1080" {
				t.Fatalf("prepared proxy = %q / %v, want the saved socks5 proxy", p.proxy, u)
			}
		})
	}
}

func TestResetTestStateClearsTheActiveClusterProxy(t *testing.T) {
	clientMu.Lock()
	activeClusterProxy = "socks5://127.0.0.1:1080"
	clientMu.Unlock()
	ResetTestState()
	clientMu.RLock()
	defer clientMu.RUnlock()
	if activeClusterProxy != "" {
		t.Fatalf("activeClusterProxy = %q after ResetTestState", activeClusterProxy)
	}
}
