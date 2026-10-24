package server

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	authv1 "k8s.io/api/authorization/v1"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/search"
)

var exposedSecretReportGVR = schema.GroupVersionResource{Group: "aquasecurity.github.io", Version: "v1alpha1", Resource: "exposedsecretreports"}

const leakedMatch = "AWS_ACCESS_KEY_ID=AKIAFAKEFAKEFAKE1234"

// setupExposedSecretReports caches one ExposedSecretReport in each of legacy
// and other, each carrying a leaked credential in report.secrets[].match.
func setupExposedSecretReports(t *testing.T) {
	t.Helper()
	gvr := exposedSecretReportGVR
	var objs []runtime.Object
	for _, ns := range []string{"legacy", "other"} {
		u := &unstructured.Unstructured{Object: map[string]any{
			"report": map[string]any{"secrets": []any{map[string]any{
				"ruleID": "aws-access-key-id", "severity": "CRITICAL", "match": leakedMatch,
			}}},
		}}
		u.SetAPIVersion("aquasecurity.github.io/v1alpha1")
		u.SetKind("ExposedSecretReport")
		u.SetNamespace(ns)
		u.SetName("replicaset-app-app")
		objs = append(objs, u)
	}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		gvr: "ExposedSecretReportList",
		// /api/resource-counts also probes EndpointSlices through this client.
		{Group: "discovery.k8s.io", Version: "v1", Resource: "endpointslices"}: "EndpointSliceList",
	}, objs...)
	if err := k8s.InitTestDynamicResourceCache(dyn, []k8s.APIResource{{
		Group: gvr.Group, Version: gvr.Version, Kind: "ExposedSecretReport", Name: gvr.Resource,
		Namespaced: true, IsCRD: true, Verbs: []string{"get", "list", "watch"},
	}}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k8s.ResetTestDynamicState)
	dc := k8s.GetDynamicResourceCache()
	if err := dc.EnsureWatching(gvr); err != nil {
		t.Fatal(err)
	}
	if !dc.WaitForSync(gvr, 5*time.Second) {
		t.Fatal("ExposedSecretReport cache did not sync")
	}
}

func readBody(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// bob can list Pods in legacy and has no RBAC on aquasecurity.github.io, as
// in a real cluster where `kubectl auth can-i list exposedsecretreports -n
// legacy --as bob` says no. No read surface may hand him the reports.
func TestProxyAuth_TrivyReportsDeniedWithoutReportRBAC(t *testing.T) {
	setupExposedSecretReports(t)
	env := newAuthTestServer(t)
	bob := &auth.UserPermissions{AllowedNamespaces: []string{"legacy"}}
	// The fake has no apiserver to answer checks, so bob's denial is seeded.
	bob.SetCanI("list", exposedSecretReportGVR.Group, exposedSecretReportGVR.Resource, "", false)
	bob.SetCanI("list", exposedSecretReportGVR.Group, exposedSecretReportGVR.Resource, "legacy", false)
	env.srv.permCache.Set("bob", nil, bob)

	for _, path := range []string{
		"/api/resources/exposedsecretreports?group=aquasecurity.github.io&namespaces=legacy",
		"/api/resources/exposedsecretreports?namespaces=legacy",
		"/api/resources/exposedsecretreports?group=aquasecurity.github.io",
	} {
		resp := env.authGet(t, path, "bob", "")
		body := readBody(t, resp)
		if resp.StatusCode != http.StatusOK || strings.TrimSpace(body) != "[]" {
			t.Errorf("%s: status %d body %s, want 200 []", path, resp.StatusCode, body)
		}
	}
	for _, path := range []string{
		"/api/resources/exposedsecretreports/legacy/replicaset-app-app?group=aquasecurity.github.io",
		"/api/resources/exposedsecretreports/legacy/replicaset-app-app",
		"/api/ai/resources/exposedsecretreports?group=aquasecurity.github.io&namespaces=legacy",
		"/api/ai/resources/exposedsecretreports/legacy/replicaset-app-app?group=aquasecurity.github.io",
	} {
		resp := env.authGet(t, path, "bob", "")
		body := readBody(t, resp)
		if resp.StatusCode != http.StatusForbidden || strings.Contains(body, leakedMatch) {
			t.Errorf("%s: status %d body %s, want 403", path, resp.StatusCode, body)
		}
	}

	resp := env.authGet(t, "/api/search?q=replicaset-app-app&include=raw", "bob", "")
	if body := readBody(t, resp); strings.Contains(body, "ExposedSecretReport") {
		t.Errorf("search returned the report: %s", body)
	}

	resp = env.authGet(t, "/api/resource-counts", "bob", "")
	var counts ResourceCountsResponse
	if err := json.Unmarshal([]byte(readBody(t, resp)), &counts); err != nil {
		t.Fatal(err)
	}
	key := "aquasecurity.github.io/ExposedSecretReport"
	if n, ok := counts.Counts[key]; ok {
		t.Errorf("counts report %d %s to bob", n, key)
	}
	if !slices.Contains(counts.Forbidden, key) {
		t.Errorf("counts forbidden = %v, want %s", counts.Forbidden, key)
	}

	resp = env.authGet(t, "/api/dashboard/crds", "bob", "")
	if body := readBody(t, resp); strings.Contains(body, "ExposedSecretReport") {
		t.Errorf("dashboard CRD counts include the report: %s", body)
	}
}

func TestProxyAuth_TrivyReportsFollowReportRBAC(t *testing.T) {
	setupExposedSecretReports(t)
	const listPath = "/api/resources/exposedsecretreports?group=aquasecurity.github.io"

	tests := []struct {
		name       string
		namespaces []string // nil = cluster-wide Pod reader
		grants     map[string]bool
		listPath   string
		wantList   []string
		wantCount  int
	}{
		{"namespace reader granted in legacy", []string{"legacy"}, map[string]bool{"": false, "legacy": true}, listPath, []string{"legacy"}, 1},
		// A cluster-wide grant also allows each namespace; the fake has no
		// apiserver to derive that, so it is seeded.
		{"cluster-wide reader granted cluster-wide", nil, map[string]bool{"": true, "legacy": true, "other": true}, listPath, []string{"legacy", "other"}, 2},
		// As with Secrets, reading every namespace needs the cluster-wide
		// grant; naming the namespace reads the reports it holds.
		{"cluster-wide reader granted only in legacy, all namespaces", nil, map[string]bool{"": false, "legacy": true, "other": false}, listPath, nil, 0},
		{"cluster-wide reader granted only in legacy, legacy named", nil, map[string]bool{"": false, "legacy": true, "other": false}, listPath + "&namespaces=legacy", []string{"legacy"}, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := newAuthTestServer(t)
			perms := &auth.UserPermissions{AllowedNamespaces: tt.namespaces}
			for ns, allowed := range tt.grants {
				perms.SetCanI("list", exposedSecretReportGVR.Group, exposedSecretReportGVR.Resource, ns, allowed)
				perms.SetCanI("get", exposedSecretReportGVR.Group, exposedSecretReportGVR.Resource, ns, allowed)
			}
			env.srv.permCache.Set("reader", nil, perms)

			resp := env.authGet(t, tt.listPath, "reader", "")
			var items []unstructured.Unstructured
			if err := json.Unmarshal([]byte(readBody(t, resp)), &items); err != nil {
				t.Fatal(err)
			}
			var got []string
			for _, u := range items {
				got = append(got, u.GetNamespace())
			}
			slices.Sort(got)
			if !slices.Equal(got, tt.wantList) {
				t.Fatalf("list namespaces = %v, want %v", got, tt.wantList)
			}

			resp = env.authGet(t, "/api/resources/exposedsecretreports/legacy/replicaset-app-app?group=aquasecurity.github.io", "reader", "")
			if body := readBody(t, resp); resp.StatusCode != http.StatusOK || !strings.Contains(body, leakedMatch) {
				t.Fatalf("get: status %d, want 200 with the report", resp.StatusCode)
			}

			resp = env.authGet(t, "/api/resource-counts", "reader", "")
			var counts ResourceCountsResponse
			if err := json.Unmarshal([]byte(readBody(t, resp)), &counts); err != nil {
				t.Fatal(err)
			}
			if n := counts.Counts["aquasecurity.github.io/ExposedSecretReport"]; n != tt.wantCount {
				t.Fatalf("count = %d, want %d", n, tt.wantCount)
			}
		})
	}
}

// Counts visit every report kind on each load, so fresh per-namespace checks
// are capped per request. A count past the cap is unavailable, never partial,
// and the next load finishes it from the remembered decisions.
func TestProxyAuth_TrivyReportCountsBoundChecks(t *testing.T) {
	setupExposedSecretReports(t)
	reviews := fakeSARServer(t, func(a authv1.ResourceAttributes) bool {
		return a.Group != exposedSecretReportGVR.Group || a.Namespace != ""
	})
	namespaces := []string{"legacy", "other"}
	for i := len(namespaces); i < 100; i++ {
		namespaces = append(namespaces, fmt.Sprintf("team-%02d", i))
	}
	env := newAuthTestServer(t)
	env.srv.permCache.Set("reader", nil, &auth.UserPermissions{AllowedNamespaces: namespaces})
	const key = "aquasecurity.github.io/ExposedSecretReport"
	reportChecks := func() int {
		n := 0
		for _, a := range *reviews {
			if a.Resource == exposedSecretReportGVR.Resource && a.Namespace != "" {
				n++
			}
		}
		return n
	}

	resp := env.authGet(t, "/api/resource-counts", "reader", "")
	var counts ResourceCountsResponse
	if err := json.Unmarshal([]byte(readBody(t, resp)), &counts); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(counts.Unavailable, key) {
		t.Fatalf("first load: %s not unavailable (count %d)", key, counts.Counts[key])
	}
	if n := reportChecks(); n > search.MaxAccessChecks {
		t.Fatalf("first load sent %d namespace checks, cap is %d", n, search.MaxAccessChecks)
	}

	resp = env.authGet(t, "/api/resource-counts", "reader", "")
	counts = ResourceCountsResponse{}
	if err := json.Unmarshal([]byte(readBody(t, resp)), &counts); err != nil {
		t.Fatal(err)
	}
	if n := counts.Counts[key]; n != 2 {
		t.Fatalf("second load: count = %d, want 2 (unavailable=%v)", n, counts.Unavailable)
	}
	if n := reportChecks(); n != len(namespaces) {
		t.Fatalf("namespace checks across both loads = %d, want %d (each once)", n, len(namespaces))
	}
}

// A permission check the apiserver didn't answer is unknown, so the count is
// unavailable rather than reported as an RBAC denial.
func TestProxyAuth_TrivyReportCountsUnansweredCheckIsUnavailable(t *testing.T) {
	setupExposedSecretReports(t)
	env := newAuthTestServer(t)
	// No client and nothing remembered: the cluster-wide check goes unanswered.
	env.srv.permCache.Set("reader", nil, &auth.UserPermissions{AllowedNamespaces: nil})

	resp := env.authGet(t, "/api/resource-counts", "reader", "")
	var counts ResourceCountsResponse
	if err := json.Unmarshal([]byte(readBody(t, resp)), &counts); err != nil {
		t.Fatal(err)
	}
	const key = "aquasecurity.github.io/ExposedSecretReport"
	if !slices.Contains(counts.Unavailable, key) || slices.Contains(counts.Forbidden, key) {
		t.Fatalf("unavailable=%v forbidden=%v, want %s unavailable only", counts.Unavailable, counts.Forbidden, key)
	}
}
