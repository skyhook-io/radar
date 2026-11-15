package mcp

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/vulnerabilities"
	"github.com/skyhook-io/radar/pkg/readreason"
)

// setupRunningVulnerableWeb runs Deployment web (ReplicaSet web-1) in team-a
// and team-b, each with a VulnerabilityReport about the ReplicaSet.
func setupRunningVulnerableWeb(t *testing.T) {
	t.Helper()
	controller := true
	objs := []runtime.Object{}
	var reports []runtime.Object
	for _, ns := range []string{"team-a", "team-b"} {
		objs = append(objs,
			&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}},
			&appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Name: "web-1", Namespace: ns,
				OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "Deployment", Name: "web", Controller: &controller}}}},
			&corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{Name: "web-1-a", Namespace: ns,
					OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "ReplicaSet", Name: "web-1", Controller: &controller}}},
				Spec:   corev1.PodSpec{Containers: []corev1.Container{{Name: "php", Image: "php:8.3.10-apache"}}},
				Status: corev1.PodStatus{Phase: corev1.PodRunning},
			},
		)
		u := &unstructured.Unstructured{Object: map[string]any{
			"report": map[string]any{
				"registry": map[string]any{"server": "index.docker.io"},
				"artifact": map[string]any{"repository": "library/php", "tag": "8.3.10-apache", "digest": "sha256:" + ns},
				"vulnerabilities": []any{map[string]any{
					"vulnerabilityID": "CVE-2026-109618", "resource": "php", "installedVersion": "8.3.10", "fixedVersion": "8.3.36", "severity": "CRITICAL",
				}},
			},
		}}
		u.SetAPIVersion("aquasecurity.github.io/v1alpha1")
		u.SetKind("VulnerabilityReport")
		u.SetNamespace(ns)
		u.SetName("replicaset-web-1-php")
		u.SetLabels(map[string]string{"trivy-operator.container.name": "php"})
		u.SetOwnerReferences([]metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "ReplicaSet", Name: "web-1", Controller: &controller}})
		reports = append(reports, u)
	}
	if err := k8s.InitTestResourceCache(fake.NewClientset(objs...)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k8s.ResetResourceCache)
	k8s.ResetTestDynamicState()
	t.Cleanup(k8s.ResetTestDynamicState)
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{vulnReportGVR: "VulnerabilityReportList"}, reports...)
	if err := k8s.InitTestDynamicResourceCache(client, []k8s.APIResource{{
		Group: vulnReportGVR.Group, Version: vulnReportGVR.Version, Kind: "VulnerabilityReport", Name: vulnReportGVR.Resource,
		Namespaced: true, IsCRD: true, Verbs: []string{"list", "watch"},
	}}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if c := k8s.GetResourceCache(); c != nil && c.IsKindReady("pods") && c.IsKindReady("replicasets") {
			if pods, _ := c.Pods().Pods("team-b").Get("web-1-a"); pods != nil {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("typed cache never synced")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func listVulns(t *testing.T, ctx context.Context, input listVulnerabilitiesInput) vulnerabilities.Response {
	t.Helper()
	result, _, err := handleListVulnerabilities(ctx, nil, input)
	if err != nil {
		t.Fatalf("list_vulnerabilities: %v", err)
	}
	var out vulnerabilities.Response
	if err := json.Unmarshal([]byte(extractText(t, result)), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestListVulnerabilitiesFollowsReportRBAC(t *testing.T) {
	setupRunningVulnerableWeb(t)
	t.Cleanup(func() { getPermCache().Invalidate() })

	all := listVulns(t, context.Background(), listVulnerabilitiesInput{View: "cve"})
	if len(all.CVEs) != 1 || all.CVEs[0].Workloads != 2 || all.CVEs[0].Affected[0].Workloads[0].Kind != "Deployment" {
		t.Fatalf("auth off: %+v", all.CVEs)
	}

	ctx := withRestrictedUser(t, "alice", []string{"team-a", "team-b"})
	perms := getPermCache().Get("alice", nil)
	perms.SetCanI("list", vulnReportGVR.Group, vulnReportGVR.Resource, "", false)
	perms.SetCanI("list", vulnReportGVR.Group, vulnReportGVR.Resource, "team-a", true)
	perms.SetCanI("list", vulnReportGVR.Group, vulnReportGVR.Resource, "team-b", false)
	for _, kind := range []struct{ group, resource string }{{"apps", "deployments"}, {"apps", "replicasets"}, {"", "pods"}} {
		for _, ns := range []string{"team-a", "team-b"} {
			perms.SetCanI("get", kind.group, kind.resource, ns, true)
			perms.SetCanI("list", kind.group, kind.resource, ns, true)
		}
	}
	got := listVulns(t, ctx, listVulnerabilitiesInput{})
	if len(got.Images) != 1 || got.Images[0].Workloads[0].Namespace != "team-a" {
		t.Fatalf("alice: %+v", got.Images)
	}
	if got.Coverage.Complete || len(got.Coverage.Gaps) != 1 || got.Coverage.Gaps[0].Code != readreason.UserForbidden {
		t.Fatalf("alice's coverage must name team-b: %+v", got.Coverage)
	}

	// carol may read the reports everywhere but Pods only in team-a: what runs
	// in team-b is unknown, so its reports are withheld and named, not
	// counted as inactive and not showing Pod images she can't read.
	ctx = withRestrictedUser(t, "carol", []string{"team-a", "team-b"})
	carol := getPermCache().Get("carol", nil)
	carol.SetCanI("list", vulnReportGVR.Group, vulnReportGVR.Resource, "", true)
	for _, ns := range []string{"team-a", "team-b"} {
		for _, r := range []string{"deployments", "replicasets"} {
			carol.SetCanI("get", "apps", r, ns, true)
			carol.SetCanI("list", "apps", r, ns, true)
		}
	}
	carol.SetCanI("get", "", "pods", "team-a", true)
	carol.SetCanI("list", "", "pods", "team-a", true)
	carol.SetCanI("get", "", "pods", "team-b", false)
	carol.SetCanI("list", "", "pods", "team-b", false)
	got = listVulns(t, ctx, listVulnerabilitiesInput{})
	if len(got.Images) != 1 || got.Images[0].Workloads[0].Namespace != "team-a" || got.Coverage.InactiveReports != 0 {
		t.Fatalf("carol: %+v / %+v", got.Images, got.Coverage)
	}
	if got.Coverage.Complete || len(got.Coverage.Gaps) != 1 || got.Coverage.Gaps[0].Code != readreason.UserForbidden {
		t.Fatalf("carol's coverage must name team-b: %+v", got.Coverage)
	}

	if _, _, err := handleListVulnerabilities(context.Background(), nil, listVulnerabilitiesInput{View: "nope"}); err == nil {
		t.Fatal("bad view must fail")
	}
}

func TestListVulnerabilitiesWithoutTrivy(t *testing.T) {
	setupRunningVulnerableWeb(t)
	k8s.ResetTestDynamicState()
	if err := k8s.InitTestDynamicResourceCache(dynamicfake.NewSimpleDynamicClient(runtime.NewScheme()), nil); err != nil {
		t.Fatal(err)
	}
	_, _, err := handleListVulnerabilities(context.Background(), nil, listVulnerabilitiesInput{})
	if code := readErrorCode(t, err); code != readreason.KindUnknown {
		t.Fatalf("code %q", code)
	}
}

// A trimmed answer says it isn't complete.
func TestTrimVulnerabilitiesMarksIncomplete(t *testing.T) {
	resp := vulnerabilities.Response{Images: make([]vulnerabilities.Image, 3), Coverage: vulnerabilities.Coverage{Complete: true}}
	got := trimVulnerabilities(resp, 2)
	if got.Coverage.Complete || got.Coverage.RowsOmitted != 1 || len(got.Images) != 2 {
		t.Fatalf("%+v", got.Coverage)
	}
}
