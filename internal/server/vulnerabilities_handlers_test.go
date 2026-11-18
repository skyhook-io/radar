package server

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/vulnerabilities"
)

var vulnerabilityReportGVR = schema.GroupVersionResource{Group: "aquasecurity.github.io", Version: "v1alpha1", Resource: "vulnerabilityreports"}

// setupVulnerabilityReports runs a bare Pod "web" in legacy and in other and
// caches one VulnerabilityReport about each, carrying one Critical finding.
func setupVulnerabilityReports(t *testing.T) {
	t.Helper()
	addPackageNamespaces(t, "legacy", "other")
	var objs []runtime.Object
	for _, ns := range []string{"legacy", "other"} {
		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: ns},
			Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "acme/web:1"}}},
			Status:     corev1.PodStatus{Phase: corev1.PodRunning},
		}
		if _, err := testFakeClient.CoreV1().Pods(ns).Create(context.Background(), pod, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			_ = testFakeClient.CoreV1().Pods(ns).Delete(context.Background(), "web", metav1.DeleteOptions{})
		})

		u := &unstructured.Unstructured{Object: map[string]any{
			"report": map[string]any{
				"artifact":        map[string]any{"repository": "acme/web", "tag": "1", "digest": "sha256:" + ns},
				"updateTimestamp": "2026-11-10T10:00:00Z",
				"vulnerabilities": []any{map[string]any{"vulnerabilityID": "CVE-2026-0001", "severity": "CRITICAL", "resource": "zlib", "installedVersion": "1", "fixedVersion": "2"}},
			},
		}}
		u.SetAPIVersion("aquasecurity.github.io/v1alpha1")
		u.SetKind("VulnerabilityReport")
		u.SetNamespace(ns)
		u.SetName("pod-web-app")
		u.SetLabels(map[string]string{"trivy-operator.resource.kind": "Pod", "trivy-operator.resource.name": "web", "trivy-operator.container.name": "app"})
		objs = append(objs, u)
	}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		vulnerabilityReportGVR: "VulnerabilityReportList",
	}, objs...)
	if err := k8s.InitTestDynamicResourceCache(dyn, []k8s.APIResource{{
		Group: vulnerabilityReportGVR.Group, Version: vulnerabilityReportGVR.Version, Kind: "VulnerabilityReport", Name: vulnerabilityReportGVR.Resource,
		Namespaced: true, IsCRD: true, Verbs: []string{"get", "list", "watch"},
	}}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k8s.ResetTestDynamicState)
	deadline := time.Now().Add(5 * time.Second)
	for {
		legacy, _ := k8s.GetResourceCache().Pods().Pods("legacy").Get("web")
		other, _ := k8s.GetResourceCache().Pods().Pods("other").Get("web")
		if legacy != nil && other != nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("pods never reached the cache")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func getVulnerabilities(t *testing.T, env *authTestEnv, path, user string) (int, vulnerabilities.Response) {
	t.Helper()
	resp := env.authGet(t, path, user, "")
	body := readBody(t, resp)
	var out vulnerabilities.Response
	if resp.StatusCode == http.StatusOK {
		if err := json.Unmarshal([]byte(body), &out); err != nil {
			t.Fatalf("decode %s: %v", body, err)
		}
	}
	return resp.StatusCode, out
}

// The rollup reads reports only where the caller may list the report kind,
// and names the namespaces it withheld rather than reading as clean.
func TestVulnerabilitiesFollowReportRBAC(t *testing.T) {
	setupVulnerabilityReports(t)
	env := newAuthTestServer(t)
	g, res := vulnerabilityReportGVR.Group, vulnerabilityReportGVR.Resource

	alice := &auth.UserPermissions{AllowedNamespaces: []string{"legacy", "other"}}
	alice.SetCanI("list", g, res, "", false)
	alice.SetCanI("list", g, res, "legacy", true)
	alice.SetCanI("list", g, res, "other", true)
	env.srv.permCache.Set("alice", nil, alice)

	bob := &auth.UserPermissions{AllowedNamespaces: []string{"legacy", "other"}}
	bob.SetCanI("list", g, res, "", false)
	bob.SetCanI("list", g, res, "legacy", true)
	bob.SetCanI("list", g, res, "other", false)
	env.srv.permCache.Set("bob", nil, bob)

	status, got := getVulnerabilities(t, env, "/api/vulnerabilities", "alice")
	if status != http.StatusOK || len(got.Images) != 2 || !got.Coverage.Complete || got.Summary.ImagesWithCritical != 2 {
		t.Fatalf("alice: %d %+v", status, got)
	}

	status, got = getVulnerabilities(t, env, "/api/vulnerabilities", "bob")
	if status != http.StatusOK || len(got.Images) != 1 || got.Images[0].Workloads[0].Namespace != "legacy" {
		t.Fatalf("bob: %d %+v", status, got.Images)
	}
	if got.Coverage.Complete || len(got.Coverage.Gaps) != 1 || got.Coverage.Gaps[0].Code != "user_forbidden" ||
		got.Coverage.Gaps[0].Namespaces == nil || len(got.Coverage.Gaps[0].Namespaces.Names) != 1 || got.Coverage.Gaps[0].Namespaces.Names[0] != "other" {
		t.Fatalf("bob's coverage must name other: %+v", got.Coverage)
	}
	// other's Pod runs, but bob can't read its report: it is withheld, not
	// listed as an unscanned image.
	if len(got.Unscanned) != 0 {
		t.Fatalf("bob unscanned: %+v", got.Unscanned)
	}

	status, got = getVulnerabilities(t, env, "/api/vulnerabilities?view=cve&severity=critical&fixable=1", "alice")
	if status != http.StatusOK || len(got.CVEs) != 1 || got.CVEs[0].Images != 2 || got.CVEs[0].Workloads != 2 {
		t.Fatalf("alice by CVE: %d %+v", status, got.CVEs)
	}

	if status, _ := getVulnerabilities(t, env, "/api/vulnerabilities?view=nope", "alice"); status != http.StatusBadRequest {
		t.Fatalf("bad view: %d", status)
	}
}
