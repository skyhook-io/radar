package mcp

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/release"
	"helm.sh/helm/v3/pkg/storage/driver"
	authv1 "k8s.io/api/authorization/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"

	"github.com/skyhook-io/radar/internal/k8s"
)

// A release a Flux HelmRelease owns is refused for every write, before Helm
// runs, including when the controller reference is hidden.
func TestManageHelmRefusesFluxManagedRelease(t *testing.T) {
	setupFakeCacheForFilterTests(t)
	gvr := schema.GroupVersionResource{Group: "helm.toolkit.fluxcd.io", Version: "v2", Resource: "helmreleases"}
	owner := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "helm.toolkit.fluxcd.io/v2", "kind": "HelmRelease",
		"metadata": map[string]any{"name": "hidden-manager", "namespace": "team-b"},
		"spec":     map[string]any{"releaseName": "demo", "storageNamespace": "team-a"},
	}}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{gvr: "HelmReleaseList"}, owner)
	if err := k8s.InitTestDynamicResourceCache(dyn, []k8s.APIResource{{Group: gvr.Group, Version: gvr.Version, Name: gvr.Resource, Kind: "HelmRelease", Namespaced: true, Verbs: []string{"get", "list", "watch"}}}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k8s.ResetTestDynamicState)
	cache := k8s.GetDynamicResourceCache()
	if err := cache.EnsureWatching(gvr); err != nil {
		t.Fatal(err)
	}
	if !cache.WaitForSync(gvr, 5*time.Second) {
		t.Fatal("HelmRelease cache did not sync")
	}

	secrets := fake.NewClientset().CoreV1().Secrets("team-a")
	storage := driver.NewSecrets(secrets)
	rel := &release.Release{Name: "demo", Namespace: "team-a", Version: 1,
		Chart: &chart.Chart{Metadata: &chart.Metadata{Name: "fixture", Version: "1.0.0"}},
		Info:  &release.Info{Status: release.StatusDeployed}}
	if err := storage.Create("sh.helm.release.v1.demo.v1", rel); err != nil {
		t.Fatal(err)
	}
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/version":
			json.NewEncoder(w).Encode(map[string]string{"gitVersion": "v1.34.0"})
		case r.Method == http.MethodPost && r.URL.Path == "/apis/authorization.k8s.io/v1/subjectaccessreviews":
			json.NewEncoder(w).Encode(authv1.SubjectAccessReview{TypeMeta: metav1.TypeMeta{APIVersion: "authorization.k8s.io/v1", Kind: "SubjectAccessReview"}, Status: authv1.SubjectAccessReviewStatus{Allowed: true}})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/namespaces/team-a/secrets":
			list, err := secrets.List(r.Context(), metav1.ListOptions{LabelSelector: r.URL.Query().Get("labelSelector")})
			if err != nil {
				t.Error(err)
				return
			}
			list.TypeMeta = metav1.TypeMeta{APIVersion: "v1", Kind: "SecretList"}
			json.NewEncoder(w).Encode(list)
		default:
			t.Errorf("unexpected request (a Flux-owned release must not be touched): %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer api.Close()
	cfg := &rest.Config{Host: api.URL, QPS: -1, ContentConfig: rest.ContentConfig{ContentType: "application/json"}}
	client, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	oldClient, oldConfig, oldStatus := k8s.SetTestClient(client), k8s.SetTestConfig(cfg), k8s.GetConnectionStatus()
	k8s.SetConnectionStatus(k8s.ConnectionStatus{State: k8s.StateConnected})
	defer func() {
		k8s.SetTestClient(oldClient)
		k8s.SetTestConfig(oldConfig)
		k8s.SetConnectionStatus(oldStatus)
	}()

	for _, caller := range []struct {
		name          string
		namespaces    []string
		wantReference bool
	}{
		{"restricted", []string{"team-a"}, false},
		{"admin", nil, true},
	} {
		ctx := withTestUserPerms(t, "helm-mcp-flux-"+caller.name, nil, caller.namespaces)
		for _, in := range []manageHelmInput{
			{Action: "upgrade", Namespace: "team-a", Name: "demo", Values: map[string]any{"replicas": 2}},
			{Action: "upgrade", Namespace: "team-a", Name: "demo", Values: map[string]any{"replicas": 2}, DryRun: true},
			{Action: "rollback", Namespace: "team-a", Name: "demo", Revision: 1},
			{Action: "uninstall", Namespace: "team-a", Name: "demo"},
		} {
			_, _, err := handleManageHelm(ctx, nil, in)
			if err == nil || !strings.Contains(err.Error(), "managed by Flux") {
				t.Errorf("%s (dry_run=%v): a Flux-owned release must be refused: %v", in.Action, in.DryRun, err)
			}

			if err != nil {
				hasReference := strings.Contains(err.Error(), "team-b/hidden-manager")
				if hasReference != caller.wantReference {
					t.Errorf("%s/%s: controller reference shown=%v, want=%v: %v", caller.name, in.Action, hasReference, caller.wantReference, err)
				}
				if !caller.wantReference && (strings.Contains(err.Error(), "hidden-manager") || strings.Contains(err.Error(), "team-b")) {
					t.Errorf("%s: write refusal leaked hidden controller: %v", in.Action, err)
				}
			}
		}
	}
}
