package policyreports

import (
	"fmt"
	"sort"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// makeReport constructs a synthetic PolicyReport (or ClusterPolicyReport)
// as the dynamic cache would surface it. namespace is set on the report's
// metadata for namespaced PolicyReports; pass "" for ClusterPolicyReport.
// scope is optional (pass nil to omit). created controls
// metadata.creationTimestamp for ordering tests.
func makeReport(t *testing.T, kind, namespace, name string, scope map[string]any, created time.Time, results []map[string]any) *unstructured.Unstructured {
	t.Helper()
	r := &unstructured.Unstructured{}
	r.SetKind(kind)
	r.SetAPIVersion("wgpolicyk8s.io/v1alpha2")
	r.SetName(name)
	if namespace != "" {
		r.SetNamespace(namespace)
	}
	r.SetCreationTimestamp(metav1.NewTime(created))

	// Build into Object so unstructured.Nested* sees nested keys.
	if scope != nil {
		if err := unstructured.SetNestedMap(r.Object, scope, "scope"); err != nil {
			t.Fatalf("set scope: %v", err)
		}
	}
	// `results` is []any of map[string]any when read via NestedSlice.
	resultsAny := make([]any, 0, len(results))
	for _, res := range results {
		resultsAny = append(resultsAny, res)
	}
	if err := unstructured.SetNestedSlice(r.Object, resultsAny, "results"); err != nil {
		t.Fatalf("set results: %v", err)
	}
	return r
}

func resourceRef(kind, ns, name string) map[string]any {
	m := map[string]any{"kind": kind, "name": name}
	if ns != "" {
		m["namespace"] = ns
	}
	return m
}

func TestBuildIndex_PerResourceFindings(t *testing.T) {
	// One report with two results, each targeting a distinct subject.
	report := makeReport(t, "PolicyReport", "prod", "kyverno-report-1", nil, time.Now(), []map[string]any{
		{
			"policy":   "disallow-privileged",
			"rule":     "no-privileged",
			"result":   "fail",
			"severity": "high",
			"category": "Pod Security",
			"message":  "container is privileged",
			"resources": []any{
				resourceRef("Pod", "prod", "web-abc123"),
			},
		},
		{
			"policy":   "require-labels",
			"rule":     "app-label",
			"result":   "warn",
			"severity": "low",
			"category": "Best Practices",
			"message":  "missing app label",
			"resources": []any{
				resourceRef("Deployment", "prod", "web"),
			},
		},
	})

	idx := BuildIndex([]*unstructured.Unstructured{report})

	podFindings := idx.FindingsFor("Pod", "prod", "web-abc123")
	if len(podFindings) != 1 {
		t.Fatalf("expected 1 finding for Pod/prod/web-abc123, got %d", len(podFindings))
	}
	if podFindings[0].Policy != "disallow-privileged" || podFindings[0].Result != "fail" {
		t.Errorf("unexpected pod finding: %+v", podFindings[0])
	}
	if podFindings[0].Severity != "high" || podFindings[0].Category != "Pod Security" {
		t.Errorf("severity/category not preserved: %+v", podFindings[0])
	}

	depFindings := idx.FindingsFor("Deployment", "prod", "web")
	if len(depFindings) != 1 {
		t.Fatalf("expected 1 finding for Deployment/prod/web, got %d", len(depFindings))
	}
	if depFindings[0].Rule != "app-label" || depFindings[0].Result != "warn" {
		t.Errorf("unexpected deployment finding: %+v", depFindings[0])
	}
}

func TestBuildIndex_MultipleResourcesPerResult(t *testing.T) {
	// One result targeting three pods → three index entries from one
	// finding row. This is the common shape for cluster-wide policies.
	report := makeReport(t, "PolicyReport", "prod", "rep-multi", nil, time.Now(), []map[string]any{
		{
			"policy": "require-cpu-limits",
			"rule":   "cpu-limits",
			"result": "fail",
			"resources": []any{
				resourceRef("Pod", "prod", "a"),
				resourceRef("Pod", "prod", "b"),
				resourceRef("Pod", "prod", "c"),
			},
		},
	})

	idx := BuildIndex([]*unstructured.Unstructured{report})

	for _, name := range []string{"a", "b", "c"} {
		got := idx.FindingsFor("Pod", "prod", name)
		if len(got) != 1 {
			t.Errorf("expected 1 finding for Pod/prod/%s, got %d", name, len(got))
		}
	}
	if idx.Size() != 3 {
		t.Errorf("expected 3 distinct subjects, got %d", idx.Size())
	}
}

func TestBuildIndex_ScopeOnlyReport(t *testing.T) {
	// Single-target report: no `results[].resources` array — the report
	// itself is scoped to one subject via `report.scope`. Each result row
	// should still produce a finding indexed under the scope subject.
	scope := map[string]any{
		"kind":      "Deployment",
		"namespace": "prod",
		"name":      "api",
	}
	report := makeReport(t, "PolicyReport", "prod", "scope-only", scope, time.Now(), []map[string]any{
		{
			"policy":   "disallow-latest-tag",
			"rule":     "no-latest",
			"result":   "fail",
			"severity": "medium",
			"message":  "image tag is latest",
			// no "resources" key
		},
		{
			"policy":  "require-readiness",
			"rule":    "readiness-probe",
			"result":  "warn",
			"message": "missing readinessProbe",
		},
	})

	idx := BuildIndex([]*unstructured.Unstructured{report})

	got := idx.FindingsFor("Deployment", "prod", "api")
	if len(got) != 2 {
		t.Fatalf("expected 2 findings under scope subject, got %d", len(got))
	}

	policies := []string{got[0].Policy, got[1].Policy}
	sort.Strings(policies)
	want := []string{"disallow-latest-tag", "require-readiness"}
	for i := range policies {
		if policies[i] != want[i] {
			t.Errorf("policy[%d]=%q want %q", i, policies[i], want[i])
		}
	}
}

func TestBuildIndex_ScopeFallback_NamespaceFromReportMetadata(t *testing.T) {
	// Scope omits namespace but the report is namespaced — common shape
	// when engines emit only kind+name in scope and rely on the report's
	// own metadata.namespace.
	scope := map[string]any{
		"kind": "Pod",
		"name": "lonely",
		// no namespace
	}
	report := makeReport(t, "PolicyReport", "dev", "rep-scope-no-ns", scope, time.Now(), []map[string]any{
		{
			"policy": "no-host-network",
			"result": "fail",
		},
	})

	idx := BuildIndex([]*unstructured.Unstructured{report})
	got := idx.FindingsFor("Pod", "dev", "lonely")
	if len(got) != 1 {
		t.Fatalf("expected 1 finding inherited from report namespace, got %d", len(got))
	}
}

func TestBuildIndex_ClusterPolicyReport(t *testing.T) {
	// ClusterPolicyReport: no report.metadata.namespace; cluster-scoped
	// resources (Node, ClusterRole) get empty-namespace keys.
	report := makeReport(t, "ClusterPolicyReport", "", "cluster-rep", nil, time.Now(), []map[string]any{
		{
			"policy": "node-ssh-disabled",
			"result": "fail",
			"resources": []any{
				resourceRef("Node", "", "gke-pool-a-1"),
			},
		},
	})

	idx := BuildIndex([]*unstructured.Unstructured{report})

	got := idx.FindingsFor("Node", "", "gke-pool-a-1")
	if len(got) != 1 {
		t.Fatalf("expected 1 finding on Node, got %d", len(got))
	}
	if got[0].Policy != "node-ssh-disabled" {
		t.Errorf("policy not preserved: %+v", got[0])
	}
}

func TestBuildIndex_NoScopeNoResources_DropsFinding(t *testing.T) {
	// Pathological / malformed report: no scope, no resources. There is
	// no subject to index against, so the finding is dropped silently.
	report := makeReport(t, "PolicyReport", "ns", "bad-rep", nil, time.Now(), []map[string]any{
		{
			"policy": "orphaned",
			"result": "error",
		},
	})

	idx := BuildIndex([]*unstructured.Unstructured{report})
	if idx.Size() != 0 {
		t.Errorf("expected empty index for orphaned finding, got %d entries", idx.Size())
	}
}

func TestBuildIndex_EmptyResourcesArray_FallsBackToScope(t *testing.T) {
	// `resources: []` (empty but present) is treated the same as
	// scope-only — both engines we've seen emit it interchangeably.
	scope := map[string]any{
		"kind":      "Service",
		"namespace": "default",
		"name":      "svc",
	}
	report := makeReport(t, "PolicyReport", "default", "empty-resources", scope, time.Now(), []map[string]any{
		{
			"policy":    "no-loadbalancer",
			"result":    "fail",
			"resources": []any{},
		},
	})

	idx := BuildIndex([]*unstructured.Unstructured{report})
	got := idx.FindingsFor("Service", "default", "svc")
	if len(got) != 1 {
		t.Fatalf("expected scope fallback to trigger on empty resources, got %d findings", len(got))
	}
}

// TestBuildIndex_MalformedResourcesEntries_FallsBackToScope pins the edge case
// reviewer caught: `resources: [{}]` is a non-empty slice that previously
// skipped scope fallback (because hasResources && len(subjects) > 0) but then
// got filtered to nothing in the index loop (every subject has empty kind/name).
// The finding was silently dropped even when scope could have rescued it.
func TestBuildIndex_MalformedResourcesEntries_FallsBackToScope(t *testing.T) {
	scope := map[string]any{
		"kind":      "Deployment",
		"namespace": "prod",
		"name":      "cart",
	}
	report := makeReport(t, "PolicyReport", "prod", "malformed-resources", scope, time.Now(), []map[string]any{
		{
			"policy": "require-resource-limits",
			"result": "fail",
			// Non-empty resources[] but every entry is empty — produced
			// by some buggy emitters when policy match conditions fail.
			"resources": []any{
				map[string]any{},
				map[string]any{},
			},
		},
	})

	idx := BuildIndex([]*unstructured.Unstructured{report})
	got := idx.FindingsFor("Deployment", "prod", "cart")
	if len(got) != 1 {
		t.Fatalf("expected scope fallback when all resources entries are empty, got %d findings", len(got))
	}
}

func TestBuildIndex_FindingsForUnknownSubject(t *testing.T) {
	report := makeReport(t, "PolicyReport", "prod", "rep", nil, time.Now(), []map[string]any{
		{
			"policy": "p",
			"result": "fail",
			"resources": []any{
				resourceRef("Pod", "prod", "a"),
			},
		},
	})
	idx := BuildIndex([]*unstructured.Unstructured{report})

	if got := idx.FindingsFor("Pod", "prod", "missing"); got != nil {
		t.Errorf("expected nil for unknown subject, got %v", got)
	}
}

func TestBuildIndex_Cap_OldestDropped(t *testing.T) {
	// Build with MaxIndexedReports + 5 reports, each targeting a unique
	// pod. The 5 oldest should be dropped — only the newest
	// MaxIndexedReports survive.
	base := time.Now()
	reports := make([]*unstructured.Unstructured, 0, MaxIndexedReports+5)
	for i := 0; i < MaxIndexedReports+5; i++ {
		// Older reports first; index sorts newest-first internally.
		created := base.Add(time.Duration(i) * time.Second)
		reports = append(reports, makeReport(t, "PolicyReport", "ns", fmt.Sprintf("rep-%d", i), nil, created, []map[string]any{
			{
				"policy": "p",
				"result": "fail",
				"resources": []any{
					resourceRef("Pod", "ns", fmt.Sprintf("pod-%d", i)),
				},
			},
		}))
	}

	idx := BuildIndex(reports)
	if idx.Size() != MaxIndexedReports {
		t.Fatalf("expected exactly MaxIndexedReports=%d subjects after cap, got %d", MaxIndexedReports, idx.Size())
	}
	// The 5 oldest (indexes 0..4) should be absent. The newest (index
	// MaxIndexedReports+4) should be present.
	if got := idx.FindingsFor("Pod", "ns", "pod-0"); got != nil {
		t.Errorf("pod-0 should have been dropped by cap, got %v", got)
	}
	newestName := fmt.Sprintf("pod-%d", MaxIndexedReports+4)
	if got := idx.FindingsFor("Pod", "ns", newestName); len(got) != 1 {
		t.Errorf("newest pod %s should be present, got %v", newestName, got)
	}
}

func TestIndex_ReplaceSwapsContents(t *testing.T) {
	// Live-update pattern: build, then replace contents on event.
	idx := NewIndex()

	idx.Replace([]*unstructured.Unstructured{
		makeReport(t, "PolicyReport", "ns", "first", nil, time.Now(), []map[string]any{
			{
				"policy": "p1",
				"result": "fail",
				"resources": []any{
					resourceRef("Pod", "ns", "first"),
				},
			},
		}),
	})
	if got := idx.FindingsFor("Pod", "ns", "first"); len(got) != 1 {
		t.Fatalf("first build missed: %v", got)
	}

	// Replace with a different report — old subject must disappear.
	idx.Replace([]*unstructured.Unstructured{
		makeReport(t, "PolicyReport", "ns", "second", nil, time.Now(), []map[string]any{
			{
				"policy": "p2",
				"result": "warn",
				"resources": []any{
					resourceRef("Pod", "ns", "second"),
				},
			},
		}),
	})

	if got := idx.FindingsFor("Pod", "ns", "first"); got != nil {
		t.Errorf("old subject leaked after Replace: %v", got)
	}
	if got := idx.FindingsFor("Pod", "ns", "second"); len(got) != 1 {
		t.Errorf("new subject missing after Replace: %v", got)
	}
}

func TestIndex_NilSafe(t *testing.T) {
	var idx *Index
	if got := idx.FindingsFor("Pod", "ns", "name"); got != nil {
		t.Errorf("nil index FindingsFor returned %v", got)
	}
	if got := idx.Size(); got != 0 {
		t.Errorf("nil index Size returned %d", got)
	}
	// Replace on nil receiver should not panic.
	idx.Replace(nil)
}
