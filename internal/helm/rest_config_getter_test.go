package helm

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/k8s"
	pkgauth "github.com/skyhook-io/radar/pkg/auth"

	"helm.sh/helm/v3/pkg/action"
	kubefake "helm.sh/helm/v3/pkg/kube/fake"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/rest"
)

func TestRESTConfigGetter_ToRESTConfig_NoImpersonation(t *testing.T) {
	src := &rest.Config{Host: "https://kubernetes.default.svc"}
	g := newRESTConfigGetter(src, "kube-system", nil)

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
	g := newRESTConfigGetter(src, "default", &pkgauth.User{Username: "alice@example.com", Groups: srcGroups})

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
	g := newRESTConfigGetter(nil, "default", nil)
	if _, err := g.ToRESTConfig(); err == nil {
		t.Fatal("ToRESTConfig with nil config: want error, got nil")
	}
}

func TestRESTConfigGetter_ToRawKubeConfigLoader_NamespaceOverride(t *testing.T) {
	g := newRESTConfigGetter(&rest.Config{Host: "https://example"}, "my-ns", nil)
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
	g := newRESTConfigGetter(&rest.Config{Host: "https://nowhere.invalid"}, "default", nil)

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

func TestActionConfigUsesCapturedRESTConfig(t *testing.T) {
	for _, host := range []string{"https://example", "https://kubernetes.default.svc"} {
		previous := k8s.SetTestConfig(&rest.Config{Host: host})
		client := &Client{conn: k8s.Current()}
		k8s.SetTestConfig(previous)
		cfg, err := client.getActionConfig("kube-system")
		if err != nil {
			t.Fatal(err)
		}
		resolved, err := cfg.RESTClientGetter.ToRESTConfig()
		if err != nil || resolved.Host != host {
			t.Fatalf("config = %v, %v; want %s", resolved, err, host)
		}
		ns, _, err := cfg.RESTClientGetter.(*restConfigGetter).ToRawKubeConfigLoader().Namespace()
		if err != nil || ns != "kube-system" {
			t.Fatalf("namespace = %s, %v", ns, err)
		}
	}
	if _, err := (&Client{}).getActionConfig("default"); err == nil {
		t.Fatal("expected missing config error")
	}
}

func TestConnectionHelmDiscovery(t *testing.T) {
	t.Cleanup(k8s.SnapshotTestConnection())
	const group = "example.com"
	var walks atomic.Int32
	var crdInstalled atomic.Bool
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/version":
			fmt.Fprint(w, `{"major":"1","minor":"34","gitVersion":"v1.34.0"}`)
		case "/api":
			walks.Add(1)
			fmt.Fprint(w, `{"kind":"APIVersions","versions":["v1"]}`)
		case "/apis":
			groups := metav1.APIGroupList{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "APIGroupList"}}
			if crdInstalled.Load() {
				version := metav1.GroupVersionForDiscovery{GroupVersion: group + "/v1", Version: "v1"}
				groups.Groups = []metav1.APIGroup{{Name: group, Versions: []metav1.GroupVersionForDiscovery{version}, PreferredVersion: version}}
			}
			writeTestJSON(t, w, groups)
		case "/api/v1":
			fmt.Fprint(w, `{"kind":"APIResourceList","apiVersion":"v1","groupVersion":"v1","resources":[{"name":"configmaps","kind":"ConfigMap","namespaced":true}]}`)
		case "/apis/" + group + "/v1":
			writeTestJSON(t, w, metav1.APIResourceList{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "APIResourceList"}, GroupVersion: group + "/v1", APIResources: []metav1.APIResource{{Name: "widgets", Kind: "Widget", Namespaced: true}}})
		default:
			t.Errorf("unexpected discovery request %s", r.URL)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(api.Close)
	config := &rest.Config{Host: api.URL, QPS: 50, Burst: 100}
	install := func() *Client {
		t.Helper()
		if _, err := k8s.InstallTestServingConnection("test", config, nil, discovery.NewDiscoveryClientForConfigOrDie(config)); err != nil {
			t.Fatal(err)
		}
		return &Client{conn: k8s.Current()}
	}
	client := install()
	actionConfig := func(t *testing.T, client *Client, namespace string, user *pkgauth.User) *action.Configuration {
		t.Helper()
		cfg, err := client.getActionConfigForUser(namespace, user)
		if err != nil {
			t.Fatal(err)
		}
		return cfg
	}
	first := actionConfig(t, client, "default", nil)
	second := actionConfig(t, client, "other", nil)
	t.Run("sharing", func(t *testing.T) {
		getter := func(cfg *action.Configuration) discovery.CachedDiscoveryInterface {
			t.Helper()
			dc, err := cfg.RESTClientGetter.ToDiscoveryClient()
			if err != nil {
				t.Fatal(err)
			}
			return dc
		}
		dc := getter(first)
		if dc != getter(second) {
			t.Error("two Helm actions on one connection did not share discovery")
		}
		if dc == getter(actionConfig(t, install(), "default", nil)) {
			t.Error("a replacement connection reused discovery")
		}
		t.Cleanup(func() { pkgauth.SetCredentialMode(pkgauth.CredentialsImpersonate) })
		for _, mode := range []pkgauth.CredentialMode{pkgauth.CredentialsImpersonate, pkgauth.CredentialsUserToken} {
			pkgauth.SetCredentialMode(mode)
			if dc == getter(actionConfig(t, client, "default", &pkgauth.User{Username: "alice", Token: "alice-token"})) {
				t.Errorf("caller in %v mode reused Radar's discovery", mode)
			}
		}
	})
	t.Run("warm render", func(t *testing.T) {
		before := walks.Load()
		releases := memoryActionConfig(t).Releases
		for _, scenario := range []struct {
			config *action.Configuration
			mode   installMode
		}{{first, installFresh}, {second, installUpgrade}} {
			cfg := scenario.config
			cfg.Releases, cfg.KubeClient = releases, &kubefake.PrintingKubeClient{Out: io.Discard}
			if _, err := runInstallOrUpgrade(cfg, &InstallRequest{ReleaseName: "demo", Namespace: "default"}, sourceTestChart("render"), scenario.mode); err != nil {
				t.Fatal(err)
			}
		}
		if got := walks.Load() - before; got != 1 {
			t.Errorf("two Helm actions walked discovery %d times, want one", got)
		}
	})
	t.Run("new CRD", func(t *testing.T) {
		mapper, err := second.RESTClientGetter.ToRESTMapper()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := mapper.RESTMapping(schema.GroupKind{Kind: "ConfigMap"}, "v1"); err != nil {
			t.Fatal(err)
		}
		crdInstalled.Store(true)
		mapping, err := mapper.RESTMapping(schema.GroupKind{Group: group, Kind: "Widget"}, "v1")
		if err != nil || mapping.Resource.Resource != "widgets" {
			t.Fatalf("mapping for new CRD = %v, %v, want widgets", mapping, err)
		}
	})
}

func TestRESTConfigGetter_ToRESTConfig_UserTokenMode(t *testing.T) {
	pkgauth.SetCredentialMode(pkgauth.CredentialsUserToken)
	t.Cleanup(func() { pkgauth.SetCredentialMode(pkgauth.CredentialsImpersonate) })
	src := &rest.Config{Host: "https://kubernetes.default.svc", BearerToken: "sa-token", BearerTokenFile: "/var/run/secrets/token"}
	g := newRESTConfigGetter(src, "default", &pkgauth.User{Username: "oidc:alice", Token: "alice-token"})

	cfg, err := g.ToRESTConfig()
	if err != nil {
		t.Fatalf("ToRESTConfig: %v", err)
	}
	if cfg.BearerToken != "alice-token" || cfg.BearerTokenFile != "" || cfg.Impersonate.UserName != "" {
		t.Fatalf("Helm config = token %q file %q impersonate %q, want only the user's token", cfg.BearerToken, cfg.BearerTokenFile, cfg.Impersonate.UserName)
	}
}

func TestActionConfigUsesCallerToken(t *testing.T) {
	pkgauth.SetCredentialMode(pkgauth.CredentialsUserToken)
	t.Cleanup(func() { pkgauth.SetCredentialMode(pkgauth.CredentialsImpersonate) })
	action, err := NewStandaloneClient(&rest.Config{Host: "https://example"}).getActionConfigForUser("default", &pkgauth.User{Username: "oidc:alice", Token: "alice-token"})
	if err != nil {
		t.Fatal(err)
	}
	got := action.RESTClientGetter
	cfg, err := got.ToRESTConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.BearerToken != "alice-token" {
		t.Fatalf("getter = %T with token %q, want the user's token on the resolved config", got, cfg.BearerToken)
	}
}
