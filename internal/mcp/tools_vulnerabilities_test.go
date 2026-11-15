package mcp

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/vulnerabilities"
	"github.com/skyhook-io/radar/pkg/k8score"
	"github.com/skyhook-io/radar/pkg/readreason"
)

// setupRunningVulnerableWeb runs Deployment web (ReplicaSet web-1) in team-a
// and team-b, each with a VulnerabilityReport about the ReplicaSet, and in
// team-c, not scanned yet.
func setupRunningVulnerableWeb(t *testing.T) {
	t.Helper()
	controller := true
	objs := []runtime.Object{}
	var reports []runtime.Object
	for _, ns := range []string{"team-a", "team-b", "team-c"} {
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
		if ns == "team-c" {
			continue
		}
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

	// dave may list reports and Pods in team-a and team-c but read the
	// workloads only in team-a. team-c's image has no report yet; it is
	// still withheld and named, not dropped from an answer that reads complete.
	ctx = withRestrictedUser(t, "dave", []string{"team-a", "team-c"})
	dave := getPermCache().Get("dave", nil)
	dave.SetCanI("list", vulnReportGVR.Group, vulnReportGVR.Resource, "", true)
	for _, ns := range []string{"team-a", "team-c"} {
		dave.SetCanI("get", "", "pods", ns, true)
		dave.SetCanI("list", "", "pods", ns, true)
		for _, r := range []string{"deployments", "replicasets"} {
			dave.SetCanI("get", "apps", r, ns, ns == "team-a")
			dave.SetCanI("list", "apps", r, ns, ns == "team-a")
		}
	}
	got = listVulns(t, ctx, listVulnerabilitiesInput{})
	if len(got.Unscanned) != 0 || got.Coverage.Complete || len(got.Coverage.Gaps) != 1 || got.Coverage.Gaps[0].Code != readreason.UserForbidden {
		t.Fatalf("dave's unreadable, unscanned workload must be a gap: %+v / %+v", got.Unscanned, got.Coverage)
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

// setupCronAndJob runs, in shop, an unsuspended CronJob nightly between runs
// (its report matches its template) and a Pod of an unrelated Job batch-1,
// with only the given typed kinds collected.
func setupCronAndJob(t *testing.T, kinds ...string) {
	t.Helper()
	controller := true
	scopes := map[string]k8score.ResourceScope{}
	for _, k := range kinds {
		scopes[k] = k8score.ResourceScope{Enabled: true}
	}
	suspended := false
	objs := []runtime.Object{
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "shop"}},
		&batchv1.CronJob{ObjectMeta: metav1.ObjectMeta{Name: "nightly", Namespace: "shop", UID: "uid-nightly"}, Spec: batchv1.CronJobSpec{
			Suspend: &suspended,
			JobTemplate: batchv1.JobTemplateSpec{Spec: batchv1.JobSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
				Containers: []corev1.Container{{Name: "report", Image: "acme/report:1"}}}}}},
		}},
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "batch-1-x", Namespace: "shop",
				OwnerReferences: []metav1.OwnerReference{{APIVersion: "batch/v1", Kind: "Job", Name: "batch-1", Controller: &controller}}},
			Spec:   corev1.PodSpec{Containers: []corev1.Container{{Name: "work", Image: "acme/batch:1"}}},
			Status: corev1.PodStatus{Phase: corev1.PodRunning},
		},
	}
	u := &unstructured.Unstructured{Object: map[string]any{"report": map[string]any{
		"artifact":        map[string]any{"repository": "acme/report", "tag": "1"},
		"vulnerabilities": []any{map[string]any{"vulnerabilityID": "CVE-1", "resource": "zlib", "installedVersion": "1", "fixedVersion": "2", "severity": "HIGH"}},
	}}}
	u.SetAPIVersion("aquasecurity.github.io/v1alpha1")
	u.SetKind("VulnerabilityReport")
	u.SetNamespace("shop")
	u.SetName("cronjob-nightly-report")
	u.SetLabels(map[string]string{"trivy-operator.container.name": "report"})
	u.SetOwnerReferences([]metav1.OwnerReference{{APIVersion: "batch/v1", Kind: "CronJob", Name: "nightly", UID: "uid-nightly", Controller: &controller}})
	if err := k8s.InitScopedTestResourceCache(fake.NewClientset(objs...), scopes); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k8s.ResetResourceCache)
	k8s.ResetTestDynamicState()
	t.Cleanup(k8s.ResetTestDynamicState)
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{vulnReportGVR: "VulnerabilityReportList"}, u)
	if err := k8s.InitTestDynamicResourceCache(client, []k8s.APIResource{{
		Group: vulnReportGVR.Group, Version: vulnReportGVR.Version, Kind: "VulnerabilityReport", Name: vulnReportGVR.Resource,
		Namespaced: true, IsCRD: true, Verbs: []string{"list", "watch"},
	}}); err != nil {
		t.Fatal(err)
	}
}

// Without Jobs, a Job's Pods can't be traced to a CronJob: they're withheld
// and named, while a CronJob with its own target keeps its report.
func TestListVulnerabilitiesWithoutJobs(t *testing.T) {
	setupCronAndJob(t, "pods", "namespaces", "cronjobs", "replicasets", "deployments")
	got := listVulns(t, context.Background(), listVulnerabilitiesInput{})
	if len(got.Images) != 1 || got.Images[0].Workloads[0].Name != "nightly" {
		t.Fatalf("the scheduled CronJob keeps its scan: %+v", got.Images)
	}
	if len(got.Unscanned) != 0 || got.Coverage.Complete || len(got.Coverage.Gaps) != 1 || got.Coverage.Gaps[0].Code != readreason.RadarKindNotCollected {
		t.Fatalf("the Job's Pod is withheld and named, not unscanned: %+v / %+v", got.Unscanned, got.Coverage)
	}
}

// Without Pods nothing is known to run: the answer says why instead of
// waiting on a cache that won't load.
func TestListVulnerabilitiesWithoutPods(t *testing.T) {
	setupCronAndJob(t, "namespaces", "cronjobs", "jobs", "replicasets", "deployments")
	got := listVulns(t, context.Background(), listVulnerabilitiesInput{})
	if got.Coverage.Complete || len(got.Coverage.Gaps) != 1 || got.Coverage.Gaps[0].Code != readreason.RadarKindNotCollected {
		t.Fatalf("coverage: %+v", got.Coverage)
	}
}
