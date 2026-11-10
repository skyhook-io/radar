package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	clienttesting "k8s.io/client-go/testing"

	"github.com/skyhook-io/radar/internal/k8s"
	bp "github.com/skyhook-io/radar/pkg/audit"
)

// The web client sends "_" as the namespace segment for cluster-scoped
// resources, because proxies that merge "//" drop an empty segment. Edit and
// delete must read "_" as no namespace, or the apiserver is asked for
// /namespaces/_/persistentvolumes/pv-a and 404s.
func TestClusterScopedWritesAcceptPlaceholderNamespace(t *testing.T) {
	pvGVR := schema.GroupVersionResource{Version: "v1", Resource: "persistentvolumes"}
	cmGVR := schema.GroupVersionResource{Version: "v1", Resource: "configmaps"}
	issuerGVR := schema.GroupVersionResource{Group: "cert-manager.io", Version: "v1", Resource: "clusterissuers"}
	obj := func(apiVersion, kind, ns, name string) *unstructured.Unstructured {
		meta := map[string]any{"name": name}
		if ns != "" {
			meta["namespace"] = ns
		}
		return &unstructured.Unstructured{Object: map[string]any{"apiVersion": apiVersion, "kind": kind, "metadata": meta}}
	}
	resources := []k8s.APIResource{
		{Version: "v1", Kind: "PersistentVolume", Name: "persistentvolumes", Namespaced: false, Verbs: []string{"get", "list", "watch", "patch", "delete"}},
		{Version: "v1", Kind: "ConfigMap", Name: "configmaps", Namespaced: true, Verbs: []string{"get", "list", "watch", "patch", "delete"}},
		{Group: "cert-manager.io", Version: "v1", Kind: "ClusterIssuer", Name: "clusterissuers", Namespaced: false, IsCRD: true, Verbs: []string{"get", "list", "watch", "patch", "delete"}},
	}

	cases := []struct {
		name, method, path, body string
		wantStatus               int
		wantVerb, wantResource   string
		wantNamespace            string
	}{
		{"edit PersistentVolume", http.MethodPut, "/api/resources/persistentvolumes/_/pv-a",
			"apiVersion: v1\nkind: PersistentVolume\nmetadata:\n  name: pv-a\n", http.StatusOK, "patch", "persistentvolumes", ""},
		{"delete PersistentVolume", http.MethodDelete, "/api/resources/persistentvolumes/_/pv-a", "",
			http.StatusOK, "delete", "persistentvolumes", ""},
		{"edit cluster-scoped CRD", http.MethodPut, "/api/resources/clusterissuers/_/letsencrypt",
			"apiVersion: cert-manager.io/v1\nkind: ClusterIssuer\nmetadata:\n  name: letsencrypt\n", http.StatusOK, "patch", "clusterissuers", ""},
		{"delete cluster-scoped CRD", http.MethodDelete, "/api/resources/clusterissuers/_/letsencrypt?group=cert-manager.io", "",
			http.StatusOK, "delete", "clusterissuers", ""},
		{"edit namespaced ConfigMap", http.MethodPut, "/api/resources/configmaps/default/app",
			"apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: app\n  namespace: default\n", http.StatusOK, "patch", "configmaps", "default"},
		{"delete namespaced ConfigMap", http.MethodDelete, "/api/resources/configmaps/default/app", "",
			http.StatusOK, "delete", "configmaps", "default"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
				runtime.NewScheme(),
				map[schema.GroupVersionResource]string{
					pvGVR:     "PersistentVolumeList",
					cmGVR:     "ConfigMapList",
					issuerGVR: "ClusterIssuerList",
				},
				obj("v1", "PersistentVolume", "", "pv-a"),
				obj("v1", "ConfigMap", "default", "app"),
				obj("cert-manager.io/v1", "ClusterIssuer", "", "letsencrypt"),
			)
			// The fake tracker does not implement server-side apply; answer
			// the patch with the stored object so the handler can finish.
			dyn.PrependReactor("patch", "*", func(a clienttesting.Action) (bool, runtime.Object, error) {
				p := a.(clienttesting.PatchAction)
				stored, err := dyn.Tracker().Get(p.GetResource(), p.GetNamespace(), p.GetName())
				return true, stored, err
			})
			if err := k8s.InitTestDynamicResourceCache(dyn, resources); err != nil {
				t.Fatalf("InitTestDynamicResourceCache: %v", err)
			}
			t.Cleanup(k8s.ResetTestDynamicState)

			req, err := http.NewRequest(tc.method, testServer.URL+tc.path, bytes.NewBufferString(tc.body))
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Content-Type", "text/plain")
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != tc.wantStatus {
				var body map[string]any
				_ = json.NewDecoder(resp.Body).Decode(&body)
				t.Fatalf("%s %s = %d, want %d: %v", tc.method, tc.path, resp.StatusCode, tc.wantStatus, body)
			}

			var hit bool
			for _, a := range dyn.Actions() {
				if a.GetVerb() != tc.wantVerb || a.GetResource().Resource != tc.wantResource {
					continue
				}
				hit = true
				if a.GetNamespace() != tc.wantNamespace {
					t.Errorf("%s went to namespace %q, want %q", tc.wantVerb, a.GetNamespace(), tc.wantNamespace)
				}
			}
			if !hit {
				t.Fatalf("no %s %s reached the cluster; actions: %v", tc.wantVerb, tc.wantResource, dyn.Actions())
			}
		})
	}
}

// A cluster-scoped resource's audit findings are stored with an empty
// namespace; the "_" path placeholder must match them.
func TestAuditResourceClusterScopedPlaceholder(t *testing.T) {
	t.Cleanup(func() { auditCache.mu.Lock(); auditCache.results = nil; auditCache.mu.Unlock() })
	req := httptest.NewRequest("GET", "/api/audit/resource/persistentvolumes/_/pv-a?raw=true", nil)
	route := chi.NewRouteContext()
	route.URLParams.Add("kind", "persistentvolumes")
	route.URLParams.Add("namespace", "_")
	route.URLParams.Add("name", "pv-a")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, route))
	auditCache.mu.Lock()
	auditCache.results = &bp.ScanResults{Findings: []bp.Finding{{Kind: "PersistentVolume", Name: "pv-a", CheckID: "pv-failed"}}}
	auditCache.key = auditKey(k8s.GetResourceCache(), testServerSrv.parseNamespacesForUser(req), testServerSrv.auditOptions(req))
	auditCache.expiresAt = time.Now().Add(time.Minute)
	auditCache.mu.Unlock()

	rec := httptest.NewRecorder()
	testServerSrv.handleAuditResource(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var got []bp.Finding
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].CheckID != "pv-failed" {
		t.Fatalf("cluster-scoped findings not matched: %+v", got)
	}
}
