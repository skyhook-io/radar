package helm

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"

	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/kube"
	"helm.sh/helm/v3/pkg/release"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/rest"

	"github.com/skyhook-io/radar/internal/k8s"
)

// An install limited by --rbac-namespaces holds no namespace create, and Helm
// sends that create even for a namespace that exists. Installing into a listed
// namespace with "create namespace" on must still succeed.
func TestInstallIntoRBACNamespaceSkipsNamespaceCreate(t *testing.T) {
	cases := []struct {
		name          string
		rbac          []string
		mode          installMode
		wantNSCreate  bool
		wantInstalled bool
	}{
		{name: "listed namespace", rbac: []string{"app"}, mode: installFresh, wantInstalled: true},
		{name: "listed namespace, replacing an uninstalled release", rbac: []string{"app"}, mode: installReplace, wantInstalled: true},
		{name: "namespace outside the list", rbac: []string{"billing"}, mode: installFresh, wantNSCreate: true},
		{name: "no list", mode: installFresh, wantNSCreate: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			k8s.SetRBACNamespaces(tc.rbac)
			t.Cleanup(func() { k8s.SetRBACNamespaces(nil) })
			cfg, requests := namespaceLimitedActionConfig(t)
			if tc.mode == installReplace {
				seedUninstalledRelease(t, cfg)
			}
			ch := &chart.Chart{
				Metadata:  &chart.Metadata{APIVersion: "v2", Name: "demo", Version: "1.0.0"},
				Templates: []*chart.File{{Name: "templates/cm.yaml", Data: []byte("apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: demo\ndata:\n  a: b\n")}},
			}
			_, err := runInstallOrUpgrade(cfg, &InstallRequest{ReleaseName: "demo", Namespace: "app", CreateNamespace: true, Values: map[string]any{}}, ch, tc.mode)
			if got := slices.Contains(*requests, "POST /api/v1/namespaces"); got != tc.wantNSCreate {
				t.Fatalf("namespace create sent = %v, want %v (requests %v)", got, tc.wantNSCreate, *requests)
			}
			if installed := slices.Contains(*requests, "POST /api/v1/namespaces/app/configmaps"); installed != tc.wantInstalled {
				t.Fatalf("installed = %v, want %v (err %v)", installed, tc.wantInstalled, err)
			}
			if tc.wantInstalled && err != nil {
				t.Fatalf("install failed: %v", err)
			}
		})
	}
}

func seedUninstalledRelease(t *testing.T, cfg *action.Configuration) {
	t.Helper()
	seedRelease(t, cfg, "demo", release.StatusUninstalled, 1)
}

// namespaceLimitedActionConfig serves an apiserver where the caller may write
// ConfigMaps in "app" and is refused any namespace create.
func namespaceLimitedActionConfig(t *testing.T) (*action.Configuration, *[]string) {
	t.Helper()
	cfg := memoryActionConfig(t)
	var mu sync.Mutex
	var requests []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/version" {
			writeTestJSON(t, w, map[string]string{"gitVersion": "v1.36.2"})
			return
		}
		switch r.URL.Path {
		case "/openapi/v3":
			writeRaw(t, w, `{"paths":{"api/v1":{"serverRelativeURL":"/openapi/v3/api/v1"}}}`)
			return
		case "/openapi/v3/api/v1":
			writeRaw(t, w, `{"openapi":"3.0.0","info":{"title":"fixture","version":"v1"},"paths":{"/api/v1/namespaces/{namespace}/configmaps/{name}":{"patch":{"x-kubernetes-group-version-kind":{"group":"","version":"v1","kind":"ConfigMap"},"parameters":[{"name":"fieldValidation","in":"query","schema":{"type":"string"}}],"responses":{"200":{"description":"ok"}}}},"/api/v1/namespaces/{name}":{"patch":{"x-kubernetes-group-version-kind":{"group":"","version":"v1","kind":"Namespace"},"parameters":[{"name":"fieldValidation","in":"query","schema":{"type":"string"}}],"responses":{"200":{"description":"ok"}}}}}}`)
			return
		}
		mu.Lock()
		if r.Method != http.MethodGet {
			requests = append(requests, r.Method+" "+r.URL.Path)
		}
		mu.Unlock()
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/namespaces":
			writeK8sStatus(t, w, http.StatusForbidden, "Forbidden", `namespaces is forbidden: cannot create resource "namespaces" at the cluster scope`)
		case r.URL.Path == "/api/v1/namespaces/app/configmaps" && r.Method == http.MethodPost:
			w.WriteHeader(http.StatusCreated)
			writeTestJSON(t, w, corev1.ConfigMap{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "ConfigMap"}, ObjectMeta: metav1.ObjectMeta{Name: "demo", Namespace: "app"}})
		default:
			writeK8sStatus(t, w, http.StatusNotFound, "NotFound", "absent")
		}
	}))
	t.Cleanup(srv.Close)
	getter := newRESTConfigGetter(&rest.Config{Host: srv.URL}, "app", nil)
	mapper := meta.NewDefaultRESTMapper([]schema.GroupVersion{{Version: "v1"}})
	mapper.Add(schema.GroupVersionKind{Version: "v1", Kind: "ConfigMap"}, meta.RESTScopeNamespace)
	mapper.Add(schema.GroupVersionKind{Version: "v1", Kind: "Namespace"}, meta.RESTScopeRoot)
	getter.mapper = mapper
	cfg.KubeClient = kube.New(getter)
	cfg.KubeClient.(*kube.Client).Log = func(string, ...any) {}
	cfg.RESTClientGetter = getter
	return cfg, &requests
}

func writeRaw(t *testing.T, w http.ResponseWriter, body string) {
	t.Helper()
	if _, err := w.Write([]byte(body)); err != nil {
		t.Error(err)
	}
}
