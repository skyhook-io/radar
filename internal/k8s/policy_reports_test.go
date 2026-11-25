package k8s

import (
	"sync/atomic"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/skyhook-io/radar/pkg/policyreports"
)

// TestGetKyvernoStatus_LifecycleTransitions pins the four states callers
// can observe via the public GetKyvernoStatus accessor. The status backs
// the meta.kyverno field on /api/issues + the MCP issues tool, so a regression
// here flips the frontend/agent's behavior between "no findings yet" copy and
// "no violations" copy — both bad.
func TestGetKyvernoStatus_LifecycleTransitions(t *testing.T) {
	withFreshTestConnection(t)
	s := &Current().dynamicCacheState().reports

	// State 1: no decision recorded yet + no index → warmup.
	s.published.Store(nil)
	if got := GetKyvernoStatus(); got != KyvernoStatusWarmup {
		t.Errorf("uninitialized: got %q want %q", got, KyvernoStatusWarmup)
	}

	// State 2: decided not-installed, no index → not_installed.
	s.published.Store(&policyReportSnapshot{status: PolicyReportStatus{Status: KyvernoStatusNotInstalled}})
	if got := GetKyvernoStatus(); got != KyvernoStatusNotInstalled {
		t.Errorf("not_installed: got %q want %q", got, KyvernoStatusNotInstalled)
	}

	// State 3: decided deferred, no index → deferred.
	s.published.Store(&policyReportSnapshot{status: PolicyReportStatus{Status: KyvernoStatusDeferred}})
	if got := GetKyvernoStatus(); got != KyvernoStatusDeferred {
		t.Errorf("deferred: got %q want %q", got, KyvernoStatusDeferred)
	}

	// State 4: index exists → ready (even if recorded status is stale —
	// the index's presence is authoritative).
	idx := policyreports.NewIndex()
	s.published.Store(&policyReportSnapshot{index: idx, status: PolicyReportStatus{Status: KyvernoStatusDeferred}})
	if got := GetKyvernoStatus(); got != KyvernoStatusReady {
		t.Errorf("ready: got %q want %q", got, KyvernoStatusReady)
	}

	// Index + decision agree on ready.
	s.published.Store(&policyReportSnapshot{index: idx, status: PolicyReportStatus{Status: KyvernoStatusReady}})
	if got := GetKyvernoStatus(); got != KyvernoStatusReady {
		t.Errorf("ready (both): got %q want %q", got, KyvernoStatusReady)
	}
}

func TestResetPolicyReportIndex_ClearsKyvernoDecision(t *testing.T) {
	withFreshTestConnection(t)
	s := &Current().dynamicCacheState().reports

	// Simulate a prior cluster that decided "ready".
	s.published.Store(&policyReportSnapshot{index: policyreports.NewIndex(), status: PolicyReportStatus{Status: KyvernoStatusReady}})

	ResetDynamicResourceCache()

	// After reset: index nil, decision empty → status reports "warmup"
	// (not "ready", not "not_installed" inherited from prior cluster).
	if got := GetKyvernoStatus(); got != KyvernoStatusWarmup {
		t.Errorf("after reset: got %q want %q (must not inherit prior cluster's decision)", got, KyvernoStatusWarmup)
	}
}

func TestRebuildPolicyReportIndex_DropsStaleGeneration(t *testing.T) {
	withFreshTestConnection(t)
	s := &Current().dynamicCacheState().reports

	oldIndex := policyreports.NewIndex()
	s.published.Store(&policyReportSnapshot{index: oldIndex})
	s.watched = []schema.GroupVersionResource{orReports}

	staleReport := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "openreports.io/v1alpha1",
		"kind":       "Report",
		"metadata": map[string]any{
			"name":      "stale-report",
			"namespace": "default",
		},
		"results": []any{map[string]any{
			"policy": "stale-policy",
			"result": "fail",
			"resources": []any{map[string]any{
				"apiVersion": "v1",
				"kind":       "Pod",
				"namespace":  "default",
				"name":       "stale-pod",
			}},
		}},
	}}
	listStarted := make(chan struct{})
	allowList := make(chan struct{})
	rebuildDone := make(chan struct{})
	go func() {
		s.rebuild(func([]schema.GroupVersionResource) []*unstructured.Unstructured {
			close(listStarted)
			<-allowList
			return []*unstructured.Unstructured{staleReport}
		})
		close(rebuildDone)
	}()

	<-listStarted
	ResetDynamicResourceCache()
	restoreTestConnection(Current().fresh())
	fresh := &Current().dynamicCacheState().reports
	freshIndex := policyreports.NewIndex()
	fresh.published.Store(&policyReportSnapshot{index: freshIndex})
	fresh.watched = []schema.GroupVersionResource{orReports}
	close(allowList)
	<-rebuildDone

	if findings := oldIndex.FindingsFor("", "Pod", "default", "stale-pod"); len(findings) != 0 {
		t.Fatalf("stale rebuild mutated the old index after a context switch: %+v", findings)
	}
	if findings := freshIndex.FindingsFor("", "Pod", "default", "stale-pod"); len(findings) != 0 {
		t.Fatalf("stale rebuild published into the new index: %+v", findings)
	}
}

func TestRebuildPolicyReportIndex_PublishesCurrentGeneration(t *testing.T) {
	withFreshTestConnection(t)
	s := &Current().dynamicCacheState().reports

	idx := policyreports.NewIndex()
	s.published.Store(&policyReportSnapshot{index: idx})
	s.watched = []schema.GroupVersionResource{orReports}

	report := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "openreports.io/v1alpha1",
		"kind":       "Report",
		"metadata": map[string]any{
			"name":      "current-report",
			"namespace": "default",
		},
		"results": []any{map[string]any{
			"policy": "current-policy",
			"result": "fail",
			"resources": []any{map[string]any{
				"apiVersion": "v1",
				"kind":       "Pod",
				"namespace":  "default",
				"name":       "current-pod",
			}},
		}},
	}}
	s.rebuild(func([]schema.GroupVersionResource) []*unstructured.Unstructured {
		return []*unstructured.Unstructured{report}
	})

	if findings := idx.FindingsFor("", "Pod", "default", "current-pod"); len(findings) != 0 {
		t.Fatalf("rebuild mutated the previously published snapshot: %+v", findings)
	}
	current := GetPolicyReportIndex()
	if current == idx {
		t.Fatal("rebuild did not publish a new index snapshot")
	}
	findings := current.FindingsFor("", "Pod", "default", "current-pod")
	if len(findings) != 1 || findings[0].Policy != "current-policy" {
		t.Fatalf("current rebuild findings = %+v, want current-policy", findings)
	}
}

func TestRebuildPolicyReportIndex_CollapsesConcurrentRequests(t *testing.T) {
	withFreshTestConnection(t)
	s := &Current().dynamicCacheState().reports

	s.published.Store(&policyReportSnapshot{index: policyreports.NewIndex()})
	s.watched = []schema.GroupVersionResource{orReports}

	var listCalls atomic.Int64
	firstListStarted := make(chan struct{})
	allowFirstList := make(chan struct{})
	secondListStarted := make(chan struct{})
	rebuildDone := make(chan struct{})
	listReports := func([]schema.GroupVersionResource) []*unstructured.Unstructured {
		switch listCalls.Add(1) {
		case 1:
			close(firstListStarted)
			<-allowFirstList
		case 2:
			close(secondListStarted)
		}
		return nil
	}
	go func() {
		s.rebuild(listReports)
		close(rebuildDone)
	}()

	<-firstListStarted
	for range 100 {
		s.rebuild(listReports)
	}
	followupNotBefore := time.Now().Add(rebuildDebounce)
	close(allowFirstList)
	<-secondListStarted
	if time.Now().Before(followupNotBefore) {
		t.Fatal("collapsed follow-up started before the production debounce delay")
	}
	<-rebuildDone

	if got := listCalls.Load(); got != 2 {
		t.Fatalf("list calls = %d, want one in-flight rebuild plus one collapsed follow-up", got)
	}
}
