package helm

import (
	"strings"
	"testing"
	"time"

	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/client-go/rest"
)

func TestRESTConfigGetter_ToRESTConfig_NoImpersonation(t *testing.T) {
	src := &rest.Config{Host: "https://kubernetes.default.svc"}
	g := newRESTConfigGetter(src, "kube-system", "", nil)

	cfg, err := g.ToRESTConfig()
	if err != nil {
		t.Fatalf("ToRESTConfig: %v", err)
	}
	if cfg.Host != src.Host {
		t.Errorf("Host = %q, want %q", cfg.Host, src.Host)
	}
	if cfg.Impersonate.UserName != "" {
		t.Errorf("Impersonate.UserName = %q, want empty", cfg.Impersonate.UserName)
	}
	// Must be a copy — mutating result must not bleed into the source.
	cfg.Host = "mutated"
	if src.Host == "mutated" {
		t.Error("ToRESTConfig returned shared pointer; expected a copy")
	}
}

func TestRESTConfigGetter_ToRESTConfig_WithImpersonation(t *testing.T) {
	src := &rest.Config{Host: "https://kubernetes.default.svc"}
	srcGroups := []string{"viewers"}
	g := newRESTConfigGetter(src, "default", "alice@example.com", srcGroups)

	cfg, err := g.ToRESTConfig()
	if err != nil {
		t.Fatalf("ToRESTConfig: %v", err)
	}
	if cfg.Impersonate.UserName != "alice@example.com" {
		t.Errorf("Impersonate.UserName = %q, want alice@example.com", cfg.Impersonate.UserName)
	}
	if len(cfg.Impersonate.Groups) != 1 || cfg.Impersonate.Groups[0] != "viewers" {
		t.Errorf("Impersonate.Groups = %v, want [viewers]", cfg.Impersonate.Groups)
	}
	if src.Impersonate.UserName != "" {
		t.Error("source rest.Config was mutated; impersonation must apply only to the copy")
	}
	// Groups must not share storage with the caller's slice — mutating
	// the returned config's groups must not bleed into the source.
	cfg.Impersonate.Groups[0] = "mutated"
	if srcGroups[0] != "viewers" {
		t.Errorf("ToRESTConfig leaked Groups slice; src[0] = %q, want viewers", srcGroups[0])
	}
}

func TestRESTConfigGetter_ToRESTConfig_NilConfig(t *testing.T) {
	g := newRESTConfigGetter(nil, "default", "", nil)
	if _, err := g.ToRESTConfig(); err == nil {
		t.Fatal("ToRESTConfig with nil config: want error, got nil")
	}
}

func TestRESTConfigGetter_ToRawKubeConfigLoader_NamespaceOverride(t *testing.T) {
	g := newRESTConfigGetter(&rest.Config{Host: "https://example"}, "my-ns", "", nil)
	loader := g.ToRawKubeConfigLoader()
	ns, _, err := loader.Namespace()
	if err != nil {
		t.Fatalf("Namespace(): %v", err)
	}
	if ns != "my-ns" {
		t.Errorf("Namespace = %q, want my-ns", ns)
	}
}

// TestRESTConfigGetter_DiscoveryAndMapper_NoDeadlock exercises the
// ToDiscoveryClient → ToRESTMapper chain Helm uses on every install /
// upgrade / resource-builder call. ToRESTMapper used to acquire the
// getter's mutex and then call ToDiscoveryClient, which re-acquires the
// same non-reentrant sync.Mutex — deadlocking the first call.
//
// The cfg.Host is bogus on purpose: discovery client construction is
// lazy (no network until a request is made), so we get to test the
// locking discipline without standing up a fake apiserver.
func TestRESTConfigGetter_DiscoveryAndMapper_NoDeadlock(t *testing.T) {
	g := newRESTConfigGetter(&rest.Config{Host: "https://nowhere.invalid"}, "default", "", nil)

	done := make(chan struct{})
	go func() {
		defer close(done)
		if _, err := g.ToDiscoveryClient(); err != nil {
			t.Errorf("ToDiscoveryClient: %v", err)
		}
		if _, err := g.ToRESTMapper(); err != nil {
			t.Errorf("ToRESTMapper: %v", err)
		}
		// Second call must also return without blocking — both should
		// hit their cached paths.
		if _, err := g.ToRESTMapper(); err != nil {
			t.Errorf("ToRESTMapper (cached): %v", err)
		}
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("ToDiscoveryClient/ToRESTMapper deadlocked")
	}
}

// TestBuildRESTClientGetter exercises the strategy picker that decides
// when to use the captured rest.Config vs a kubeconfig-backed
// ConfigFlags. This is the actual decision the PR ships — the leaf
// getter tests above are necessary but not sufficient.
func TestBuildRESTClientGetter(t *testing.T) {
	cases := []struct {
		name       string
		params     restClientGetterParams
		wantType   string // "configFlags" | "restConfigGetter" | ""
		wantErr    bool
		wantErrSub string
	}{
		{
			name: "kubeconfig set: use ConfigFlags (dominant OSS laptop case)",
			params: restClientGetterParams{
				kubeconfig: "/home/user/.kube/config",
				restConfig: &rest.Config{Host: "https://example"},
				namespace:  "default",
			},
			wantType: "configFlags",
		},
		{
			name: "kubeconfig empty + rest.Config available: use restConfigGetter (in-cluster / multi-source)",
			params: restClientGetterParams{
				kubeconfig: "",
				restConfig: &rest.Config{Host: "https://kubernetes.default.svc"},
				namespace:  "kube-system",
			},
			wantType: "restConfigGetter",
		},
		{
			name: "kubeconfig empty + no rest.Config: error",
			params: restClientGetterParams{
				kubeconfig: "",
				restConfig: nil,
			},
			wantErr:    true,
			wantErrSub: "no kubeconfig path and no resolved rest.Config",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := buildRESTClientGetter(tc.params)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("want error, got %T", got)
				}
				if tc.wantErrSub != "" && !strings.Contains(err.Error(), tc.wantErrSub) {
					t.Errorf("error %q missing substring %q", err.Error(), tc.wantErrSub)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			switch tc.wantType {
			case "configFlags":
				if _, ok := got.(*genericclioptions.ConfigFlags); !ok {
					t.Errorf("got %T, want *genericclioptions.ConfigFlags", got)
				}
			case "restConfigGetter":
				if _, ok := got.(*restConfigGetter); !ok {
					t.Errorf("got %T, want *restConfigGetter", got)
				}
			}
		})
	}
}
