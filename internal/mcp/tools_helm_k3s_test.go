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

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/k8s"
)

// A release a k3s HelmChart installs is refused for every write, before Helm
// runs: helm-controller reinstalls it from the HelmChart at its next reconcile.
func TestManageHelmRefusesK3sManagedRelease(t *testing.T) {
	setupFakeCacheForFilterTests(t)
	charts := schema.GroupVersionResource{Group: "helm.cattle.io", Version: "v1", Resource: "helmcharts"}
	configs := schema.GroupVersionResource{Group: "helm.cattle.io", Version: "v1", Resource: "helmchartconfigs"}
	k3sObj := func(kind string) *unstructured.Unstructured {
		return &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "helm.cattle.io/v1", "kind": kind,
			"metadata": map[string]any{"name": "demo", "namespace": "kube-system"},
		}}
	}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{charts: "HelmChartList", configs: "HelmChartConfigList"}, k3sObj("HelmChart"), k3sObj("HelmChartConfig"))
	if err := k8s.InitTestDynamicResourceCache(dyn, []k8s.APIResource{
		{Group: charts.Group, Version: charts.Version, Name: charts.Resource, Kind: "HelmChart", Namespaced: true, Verbs: []string{"get", "list", "watch"}},
		{Group: configs.Group, Version: configs.Version, Name: configs.Resource, Kind: "HelmChartConfig", Namespaced: true, Verbs: []string{"get", "list", "watch"}},
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k8s.ResetTestDynamicState)
	cache := k8s.GetDynamicResourceCache()
	for _, gvr := range []schema.GroupVersionResource{charts, configs} {
		if err := cache.EnsureWatching(gvr); err != nil {
			t.Fatal(err)
		}
		if !cache.WaitForSync(gvr, 5*time.Second) {
			t.Fatalf("%s cache did not sync", gvr.Resource)
		}
	}

	secrets := fake.NewClientset().CoreV1().Secrets("kube-system")
	storage := driver.NewSecrets(secrets)
	rel := &release.Release{Name: "demo", Namespace: "kube-system", Version: 1,
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
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/namespaces/kube-system/secrets":
			list, err := secrets.List(r.Context(), metav1.ListOptions{LabelSelector: r.URL.Query().Get("labelSelector")})
			if err != nil {
				t.Error(err)
				return
			}
			list.TypeMeta = metav1.TypeMeta{APIVersion: "v1", Kind: "SecretList"}
			json.NewEncoder(w).Encode(list)
		default:
			t.Errorf("unexpected request (a k3s-owned release must not be touched): %s %s", r.Method, r.URL.Path)
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

	ctx := auth.ContextWithUser(t.Context(), &auth.User{Username: "helm-mcp-k3s"})
	for _, in := range []manageHelmInput{
		{Action: "upgrade", Namespace: "kube-system", Name: "demo", Values: map[string]any{"replicas": 2}},
		{Action: "upgrade", Namespace: "kube-system", Name: "demo", Values: map[string]any{"replicas": 2}, DryRun: true},
		{Action: "rollback", Namespace: "kube-system", Name: "demo", Revision: 1},
		{Action: "uninstall", Namespace: "kube-system", Name: "demo"},
	} {
		_, _, err := handleManageHelm(ctx, nil, in)
		if err == nil || !strings.Contains(err.Error(), "k3s HelmChart kube-system/demo") || !strings.Contains(err.Error(), "HelmChartConfig kube-system/demo") {
			t.Errorf("%s (dry_run=%v): a k3s-owned release must be refused naming its HelmChart and HelmChartConfig: %v", in.Action, in.DryRun, err)
		}
	}
}
