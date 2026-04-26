package helm

import (
	"testing"

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
	g := newRESTConfigGetter(src, "default", "alice@example.com", []string{"viewers"})

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
