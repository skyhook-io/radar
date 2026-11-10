package server

import (
	"encoding/json"
	"net/http"
	"slices"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/k8s"
)

// Trivy reports carry CVE and exposed-secret findings, so search returns them
// only where the caller may list that report kind, not wherever they may list
// Pods.
func TestProxyAuth_SearchTrivyReports_PerKindRBAC(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "aquasecurity.github.io", Version: "v1alpha1", Resource: "vulnerabilityreports"}
	var objs []runtime.Object
	for _, ns := range []string{"default", "broken"} {
		u := &unstructured.Unstructured{Object: map[string]any{
			"report": map[string]any{"vulnerabilities": []any{map[string]any{"vulnerabilityID": "CVE-2026-109618", "resource": "php"}}},
		}}
		u.SetAPIVersion("aquasecurity.github.io/v1alpha1")
		u.SetKind("VulnerabilityReport")
		u.SetNamespace(ns)
		u.SetName("replicaset-web-php")
		objs = append(objs, u)
	}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{gvr: "VulnerabilityReportList"}, objs...)
	if err := k8s.InitTestDynamicResourceCache(dyn, []k8s.APIResource{{
		Group: gvr.Group, Version: gvr.Version, Kind: "VulnerabilityReport", Name: gvr.Resource,
		Namespaced: true, IsCRD: true, Verbs: []string{"list", "watch"},
	}}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k8s.ResetTestDynamicState)
	dc := k8s.GetDynamicResourceCache()
	if err := dc.EnsureWatching(gvr); err != nil {
		t.Fatal(err)
	}
	if !dc.WaitForSync(gvr, 5*time.Second) {
		t.Fatal("VulnerabilityReport cache did not sync")
	}

	env := newAuthTestServer(t)
	env.srv.permCache.Set("alice", nil, &auth.UserPermissions{AllowedNamespaces: []string{"default", "broken"}})
	perms := env.srv.permCache.Get("alice", nil)
	perms.SetCanI("list", gvr.Group, gvr.Resource, "default", true)
	perms.SetCanI("list", gvr.Group, gvr.Resource, "broken", false)

	resp := env.authGet(t, "/api/search?q=CVE-2026-109618&context=none&include=none", "alice", "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var body struct {
		Hits []struct {
			Namespace string `json:"namespace"`
		} `json:"hits"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, h := range body.Hits {
		got = append(got, h.Namespace)
	}
	if !slices.Equal(got, []string{"default"}) {
		t.Fatalf("hits in %v, want default only", got)
	}
}
