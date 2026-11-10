package mcp

import (
	"context"
	"encoding/json"
	"slices"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/search"
	pkgauth "github.com/skyhook-io/radar/pkg/auth"
)

var vulnReportGVR = schema.GroupVersionResource{Group: "aquasecurity.github.io", Version: "v1alpha1", Resource: "vulnerabilityreports"}

func setupTrivyReports(t *testing.T) {
	t.Helper()
	// The reports' namespaces exist, so a caller who sees every namespace and
	// names one is read rather than told it doesn't exist.
	namespaces := []runtime.Object{
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "team-a"}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "team-b"}},
	}
	if err := k8s.InitTestResourceCache(fake.NewClientset(namespaces...)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k8s.ResetResourceCache)
	k8s.ResetTestDynamicState()
	t.Cleanup(k8s.ResetTestDynamicState)

	var objs []runtime.Object
	for _, ns := range []string{"team-a", "team-b"} {
		u := &unstructured.Unstructured{Object: map[string]any{
			"report": map[string]any{
				"artifact": map[string]any{"repository": "library/php", "tag": "8.3.10-apache"},
				"vulnerabilities": []any{map[string]any{
					"vulnerabilityID": "CVE-2026-109618", "resource": "php", "installedVersion": "8.3.10", "fixedVersion": "8.3.36", "severity": "CRITICAL",
				}},
			},
		}}
		u.SetAPIVersion("aquasecurity.github.io/v1alpha1")
		u.SetKind("VulnerabilityReport")
		u.SetNamespace(ns)
		u.SetName("replicaset-web-php")
		objs = append(objs, u)
	}
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{vulnReportGVR: "VulnerabilityReportList"}, objs...)
	if err := k8s.InitTestDynamicResourceCache(client, []k8s.APIResource{{
		Group: vulnReportGVR.Group, Version: vulnReportGVR.Version, Kind: "VulnerabilityReport", Name: vulnReportGVR.Resource,
		Namespaced: true, IsCRD: true, Verbs: []string{"list", "watch"},
	}}); err != nil {
		t.Fatal(err)
	}
	dc := k8s.GetDynamicResourceCache()
	if err := dc.EnsureWatching(vulnReportGVR); err != nil {
		t.Fatal(err)
	}
	if !dc.WaitForSync(vulnReportGVR, 2*time.Second) {
		t.Fatal("VulnerabilityReport cache did not sync")
	}
}

func searchHitNamespaces(t *testing.T, ctx context.Context, query string) []string {
	t.Helper()
	result, _, err := handleSearch(ctx, nil, searchInput{Query: query, Context: "none", Include: "none"})
	if err != nil {
		t.Fatalf("handleSearch: %v", err)
	}
	var res search.Result
	if err := json.Unmarshal([]byte(extractText(t, result)), &res); err != nil {
		t.Fatal(err)
	}
	out := []string{}
	for _, h := range res.Hits {
		out = append(out, h.Namespace)
	}
	slices.Sort(out)
	return out
}

func TestHandleSearch_TrivyReportsFollowPerKindRBAC(t *testing.T) {
	setupTrivyReports(t)
	t.Cleanup(func() { getPermCache().Invalidate() })
	const q = "CVE-2026-109618"

	if got := searchHitNamespaces(t, context.Background(), q); !slices.Equal(got, []string{"team-a", "team-b"}) {
		t.Fatalf("auth off: hits in %v", got)
	}

	ctx := withRestrictedUser(t, "alice", []string{"team-a", "team-b"})
	perms := getPermCache().Get("alice", nil)
	perms.SetCanI("list", vulnReportGVR.Group, vulnReportGVR.Resource, "team-a", true)
	perms.SetCanI("list", vulnReportGVR.Group, vulnReportGVR.Resource, "team-b", false)
	if got := searchHitNamespaces(t, ctx, q); !slices.Equal(got, []string{"team-a"}) {
		t.Fatalf("namespace-scoped reader: hits in %v, want team-a only", got)
	}

	ctx = pkgauth.ContextWithUser(context.Background(), &pkgauth.User{Username: "bob"})
	bob := &pkgauth.UserPermissions{AllowedNamespaces: nil}
	bob.SetCanI("list", vulnReportGVR.Group, vulnReportGVR.Resource, "", false)
	bob.SetCanI("list", vulnReportGVR.Group, vulnReportGVR.Resource, "team-a", false)
	bob.SetCanI("list", vulnReportGVR.Group, vulnReportGVR.Resource, "team-b", true)
	getPermCache().Set("bob", nil, bob)
	if got := searchHitNamespaces(t, ctx, q); !slices.Equal(got, []string{"team-b"}) {
		t.Fatalf("cluster-wide pod reader with report RBAC in team-b: hits in %v", got)
	}
}
