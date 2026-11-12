package server

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/k8s"
)

var traefikKinds = []k8s.APIResource{
	{Group: "traefik.io", Version: "v1alpha1", Name: "ingressroutes", Kind: "IngressRoute", Namespaced: true, Verbs: []string{"get", "list", "watch"}},
	{Group: "traefik.io", Version: "v1alpha1", Name: "middlewares", Kind: "Middleware", Namespaced: true, Verbs: []string{"get", "list", "watch"}},
}

func traefikIR(ns, name, match string, svcNs string, mw string, tls map[string]any) *unstructured.Unstructured {
	svc := map[string]any{"name": "ui", "port": int64(80)}
	if svcNs != "" {
		svc["namespace"] = svcNs
	}
	rule := map[string]any{"match": match, "kind": "Rule", "services": []any{svc}}
	if mw != "" {
		rule["middlewares"] = []any{map[string]any{"name": mw}}
	}
	spec := map[string]any{"routes": []any{rule}}
	if tls != nil {
		spec["tls"] = tls
	}
	return cnpgObj("traefik.io/v1alpha1", "IngressRoute", ns, name, spec, nil)
}

func seedTraefikFixture(t *testing.T) {
	t.Helper()
	restoreFixtureCache(t)
	k8s.ResetResourceCache()
	if err := k8s.InitTestResourceCache(fake.NewClientset(
		&corev1.Service{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "ui"}, Spec: corev1.ServiceSpec{Selector: map[string]string{"app": "ui"}}},
	)); err != nil {
		t.Fatal(err)
	}
	seedCNPGWorkspace(t, traefikKinds,
		traefikIR("shop", "ui", "Host(`ui.example.com`)", "", "redirect", map[string]any{"secretName": "ui-tls"}),
		traefikIR("shop", "api", "Host(`api.example.com`)", "other", "", nil),
		traefikIR("other", "x", "Host(`x.example.com`)", "", "", nil),
		cnpgObj("traefik.io/v1alpha1", "Middleware", "shop", "redirect", map[string]any{"redirectScheme": map[string]any{"scheme": "https"}}, nil),
	)
}

func decodeTraefik(t *testing.T, resp *http.Response) (traefikRoutesResponse, string) {
	t.Helper()
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d: %s", resp.StatusCode, raw)
	}
	var out traefikRoutesResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return out, string(raw)
}

func TestTraefikRoutesNotInstalled(t *testing.T) {
	restoreFixtureCache(t)
	seedCNPGWorkspace(t, nil)
	resp, err := http.Get(testServer.URL + "/api/traefik/routes")
	if err != nil {
		t.Fatal(err)
	}
	got, _ := decodeTraefik(t, resp)
	if got.Installed || len(got.Routes) != 0 {
		t.Fatalf("got %+v", got)
	}
}

func aliceTraefik(t *testing.T, secrets bool) *authTestEnv {
	env := newAuthTestServer(t)
	perms := &auth.UserPermissions{AllowedNamespaces: []string{"shop"}}
	allow(perms, "traefik.io", "ingressroutes", "", false)
	allow(perms, "traefik.io", "ingressroutes", "shop", true)
	allow(perms, "traefik.io", "middlewares", "", false)
	allow(perms, "traefik.io", "middlewares", "shop", true)
	allow(perms, "", "secrets", "shop", secrets)
	env.srv.permCache.Set("alice", nil, perms)
	return env
}

func TestTraefikRoutesPerUserNamespaces(t *testing.T) {
	seedTraefikFixture(t)
	got, _ := decodeTraefik(t, aliceTraefik(t, false).authGet(t, "/api/traefik/routes", "alice", ""))
	if len(got.Routes) != 2 {
		t.Fatalf("routes = %+v", got.Routes)
	}
	for _, r := range got.Routes {
		if r.Ref.Namespace != "shop" {
			t.Errorf("row outside the caller's namespaces: %+v", r.Ref)
		}
		if r.Ref.Name == "api" && r.Services[0].Exists != nil {
			t.Errorf("a backend in an unseen namespace must be can't tell: %+v", r.Services[0])
		}
	}
	// Coverage is over the caller's own namespaces (the workspace
	// convention), which alice's read covers fully.
	if got.Coverage["ingressroutes"].State != "full" {
		t.Errorf("coverage = %+v", got.Coverage["ingressroutes"])
	}
}

func TestTraefikRoutesSecretGate(t *testing.T) {
	seedTraefikFixture(t)
	got, raw := decodeTraefik(t, aliceTraefik(t, false).authGet(t, "/api/traefik/routes", "alice", ""))
	for _, r := range got.Routes {
		if r.Ref.Name == "ui" && r.TLS.Certificate != "denied" {
			t.Errorf("tls = %+v", r.TLS)
		}
	}
	if strings.Contains(raw, "notAfter") {
		t.Errorf("no notAfter without Secret access: %s", raw)
	}
}

func TestTraefikRoutesSingleRouteAndMiddlewareModes(t *testing.T) {
	seedTraefikFixture(t)
	get := func(q string) (*http.Response, error) { return http.Get(testServer.URL + "/api/traefik/routes" + q) }
	resp, _ := get("?namespace=shop&name=ui")
	one, _ := decodeTraefik(t, resp)
	if len(one.Routes) != 1 || one.Routes[0].Ref.Name != "ui" {
		t.Fatalf("single route = %+v", one.Routes)
	}
	if len(one.Middlewares) != 1 || one.Middlewares[0].UsedBy != 1 {
		t.Errorf("middlewares = %+v", one.Middlewares)
	}
	resp, _ = get("?middleware=shop/redirect")
	used, _ := decodeTraefik(t, resp)
	if len(used.Routes) != 1 || used.Routes[0].Ref.Name != "ui" {
		t.Fatalf("middleware mode = %+v", used.Routes)
	}
	resp, _ = get("?namespace=shop&name=ui&middleware=shop/redirect")
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("both modes: status %d, want 400", resp.StatusCode)
	}
}

func TestTraefikRoutesWithoutAuthReadsAsRadar(t *testing.T) {
	seedTraefikFixture(t)
	req, _ := http.NewRequest("GET", testServer.URL+"/api/traefik/routes", nil)
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-site GET = %d, want 403", resp.StatusCode)
	}
	resp, _ = http.Get(testServer.URL + "/api/traefik/routes")
	got, _ := decodeTraefik(t, resp)
	if !got.Installed || len(got.Routes) != 3 {
		t.Fatalf("plain GET = %+v", got)
	}
}
