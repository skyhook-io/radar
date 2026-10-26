package issues

import (
	"strings"
	"testing"
	"time"

	"github.com/skyhook-io/radar/pkg/issuesapi"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/utils/ptr"
)

func rolloutWithConditions(conds []map[string]any) *unstructured.Unstructured {
	raw := make([]any, len(conds))
	for i, c := range conds {
		raw[i] = c
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"status": map[string]any{"conditions": raw},
	}}
}

func TestGenericConditionSelectionKeepsActionableFailures(t *testing.T) {
	failedAt := time.Now().Add(-2 * time.Minute).UTC().Truncate(time.Second)
	creating := map[string]any{"type": "Ready", "status": "False", "reason": "Creating", "lastTransitionTime": failedAt.Add(-time.Minute).Format(time.RFC3339)}
	failure := map[string]any{"type": "Synced", "status": "False", "reason": "ReconcileError", "message": "credentials rejected", "lastTransitionTime": failedAt.Format(time.RFC3339)}
	readyFailure := map[string]any{"type": "Ready", "status": "False", "reason": "Unavailable", "lastTransitionTime": failedAt.Format(time.RFC3339)}
	for _, tc := range []struct {
		name        string
		legacy      []map[string]any
		current     []map[string]any
		suspended   bool
		lagging     bool
		wantReason  string
		wantMessage string
	}{
		{name: "expected first", legacy: []map[string]any{creating, failure}, wantReason: "Synced: ReconcileError", wantMessage: "credentials rejected"},
		{name: "failure first", legacy: []map[string]any{failure, creating}, wantReason: "Synced: ReconcileError", wantMessage: "credentials rejected"},
		{name: "only expected", legacy: []map[string]any{creating}},
		{name: "actionable order unchanged", legacy: []map[string]any{readyFailure, failure}, wantReason: "Ready: Unavailable"},
		{name: "current expected hides stale same type", current: []map[string]any{creating}, legacy: []map[string]any{readyFailure, failure}, wantReason: "Synced: ReconcileError", wantMessage: "credentials rejected"},
		{name: "current healthy hides stale same type", current: []map[string]any{{"type": "Ready", "status": "True"}}, legacy: []map[string]any{readyFailure}},
		{name: "current unknown hides stale same type", current: []map[string]any{{"type": "Ready", "status": "Unknown"}}, legacy: []map[string]any{readyFailure}},
		{name: "current healthy does not hide independent failure", current: []map[string]any{{"type": "Ready", "status": "True"}}, legacy: []map[string]any{readyFailure, failure}, wantReason: "Synced: ReconcileError", wantMessage: "credentials rejected"},
		{name: "current failing overrides older healthy", current: []map[string]any{readyFailure}, legacy: []map[string]any{{"type": "Ready", "status": "True"}}, wantReason: "Ready: Unavailable"},
		{name: "first duplicate healthy is authoritative", legacy: []map[string]any{{"type": "Ready", "status": "True"}, readyFailure}},
		{name: "first duplicate expected is authoritative", legacy: []map[string]any{creating, readyFailure}},
		{name: "suspend preserved", legacy: []map[string]any{creating, failure}, suspended: true},
		{name: "generation lag preserved", legacy: []map[string]any{creating, failure}, lagging: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u := rolloutWithConditions(tc.legacy)
			u.SetName("example")
			u.SetNamespace("test")
			if tc.current != nil {
				u.Object["status"].(map[string]any)["v1beta2"] = rolloutWithConditions(tc.current).Object["status"]
			}
			if tc.suspended {
				u.Object["spec"] = map[string]any{"suspend": true}
			}
			if tc.lagging {
				u.SetGeneration(2)
				u.Object["status"].(map[string]any)["observedGeneration"] = int64(1)
			}
			gvr := schema.GroupVersionResource{Group: "example.com", Version: "v1", Resource: "widgets"}
			p := &fakeProvider{
				dynamic:    map[schema.GroupVersionResource][]*unstructured.Unstructured{gvr: {u}},
				kinds:      map[schema.GroupVersionResource]string{gvr: "Widget"},
				namespaced: map[schema.GroupVersionResource]bool{gvr: true},
			}
			for _, grouped := range []bool{false, true} {
				got := Compose(p, Filters{Namespaces: []string{"test"}, Grouped: grouped})
				if tc.wantReason == "" {
					if len(got) != 0 {
						t.Fatalf("grouped=%v: expected no issue, got %+v", grouped, got)
					}
					continue
				}
				if len(got) != 1 || got[0].Reason != tc.wantReason || got[0].Severity != SeverityWarning {
					t.Fatalf("grouped=%v: got %+v, want one warning %q", grouped, got, tc.wantReason)
				}
				if got[0].Message != tc.wantMessage {
					t.Fatalf("message = %q, want %q", got[0].Message, tc.wantMessage)
				}
				if !got[0].FirstSeen.Equal(failedAt) {
					t.Fatalf("failure onset = %v, want %v", got[0].FirstSeen, failedAt)
				}
			}
		})
	}
}

func TestGenericConditionsRespectExplicitCrossplanePause(t *testing.T) {
	for _, tc := range []struct {
		name, annotation, condType, reason, want string
		failureFirst                             bool
		independentFailure                       bool
		current                                  []map[string]any
	}{
		{name: "intentional pause", annotation: "true", condType: "Synced", reason: "ReconcilePaused"},
		{name: "missing annotation", condType: "Synced", reason: "ReconcilePaused", want: "Synced: ReconcilePaused"},
		{name: "false annotation", annotation: "false", condType: "Synced", reason: "ReconcilePaused", want: "Synced: ReconcilePaused"},
		{name: "noncanonical annotation", annotation: "True", condType: "Synced", reason: "ReconcilePaused", want: "Synced: ReconcilePaused"},
		{name: "different condition", annotation: "true", condType: "Ready", reason: "ReconcilePaused", want: "Ready: ReconcilePaused"},
		{name: "error while paused", annotation: "true", condType: "Synced", reason: "ReconcileError", want: "Synced: ReconcileError"},
		{name: "independent error after pause", annotation: "true", condType: "Synced", reason: "ReconcilePaused", independentFailure: true, want: "Ready: Unavailable"},
		{name: "independent error before pause", annotation: "true", condType: "Synced", reason: "ReconcilePaused", independentFailure: true, failureFirst: true, want: "Ready: Unavailable"},
		{name: "current pause replaces older error", annotation: "true", condType: "Synced", reason: "ReconcileError", current: []map[string]any{{"type": "Synced", "status": "False", "reason": "ReconcilePaused"}}},
		{name: "current error replaces older pause", annotation: "true", condType: "Synced", reason: "ReconcilePaused", current: []map[string]any{{"type": "Synced", "status": "False", "reason": "ReconcileError"}}, want: "Synced: ReconcileError"},
		{name: "current unknown replaces older pause", condType: "Synced", reason: "ReconcilePaused", current: []map[string]any{{"type": "Synced", "status": "Unknown"}}},
		{name: "current true replaces older pause", condType: "Synced", reason: "ReconcilePaused", current: []map[string]any{{"type": "Synced", "status": "True"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			conds := []map[string]any{{"type": tc.condType, "status": "False", "reason": tc.reason}}
			if tc.independentFailure {
				failure := map[string]any{"type": "Ready", "status": "False", "reason": "Unavailable"}
				if tc.failureFirst {
					conds = append([]map[string]any{failure}, conds...)
				} else {
					conds = append(conds, failure)
				}
			}
			u := rolloutWithConditions(conds)
			u.SetName("example")
			u.SetNamespace("test")
			if tc.annotation != "" {
				u.SetAnnotations(map[string]string{"crossplane.io/paused": tc.annotation})
			}
			if tc.current != nil {
				u.Object["status"].(map[string]any)["v1beta2"] = rolloutWithConditions(tc.current).Object["status"]
			}
			gvr := schema.GroupVersionResource{Group: "platform.example.com", Version: "v1", Resource: "databases"}
			p := &fakeProvider{dynamic: map[schema.GroupVersionResource][]*unstructured.Unstructured{gvr: {u}}, kinds: map[schema.GroupVersionResource]string{gvr: "Database"}, namespaced: map[schema.GroupVersionResource]bool{gvr: true}}
			for _, grouped := range []bool{false, true} {
				got := Compose(p, Filters{Namespaces: []string{"test"}, Grouped: grouped})
				if tc.want == "" {
					if len(got) != 0 {
						t.Fatalf("expected quiet pause, got %+v", got)
					}
					continue
				}
				if len(got) != 1 || got[0].Reason != tc.want || got[0].Severity != SeverityWarning {
					t.Fatalf("want warning %q, got %+v", tc.want, got)
				}
			}
		})
	}
}

func TestCrossplanePauseKeepsIndependentFailureObservation(t *testing.T) {
	failedAt := time.Now().Add(-8 * time.Minute).UTC().Truncate(time.Second)
	for _, tc := range []struct {
		name                        string
		stale, unknownTime, lagging bool
	}{
		{name: "known onset"},
		{name: "older generation", stale: true},
		{name: "unknown onset", unknownTime: true},
		{name: "existing object generation gate", stale: true, lagging: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			failure := map[string]any{"type": "Ready", "status": "False", "reason": "Unavailable", "message": "last known availability failure"}
			if !tc.unknownTime {
				failure["lastTransitionTime"] = failedAt.Format(time.RFC3339)
			}
			if tc.stale {
				failure["observedGeneration"] = int64(1)
			}
			u := rolloutWithConditions([]map[string]any{{"type": "Synced", "status": "False", "reason": "ReconcilePaused", "lastTransitionTime": failedAt.Add(-time.Hour).Format(time.RFC3339)}, failure})
			u.SetName("example")
			u.SetNamespace("test")
			u.SetGeneration(2)
			u.SetAnnotations(map[string]string{"crossplane.io/paused": "true"})
			if tc.lagging {
				u.Object["status"].(map[string]any)["observedGeneration"] = int64(1)
			}
			gvr := schema.GroupVersionResource{Group: "platform.example.com", Version: "v1", Resource: "databases"}
			p := &fakeProvider{dynamic: map[schema.GroupVersionResource][]*unstructured.Unstructured{gvr: {u}}, kinds: map[schema.GroupVersionResource]string{gvr: "Database"}, namespaced: map[schema.GroupVersionResource]bool{gvr: true}}
			for _, grouped := range []bool{false, true} {
				got := Compose(p, Filters{Namespaces: []string{"test"}, Grouped: grouped})
				if tc.lagging {
					if len(got) != 0 {
						t.Fatalf("existing generation gate changed: %+v", got)
					}
					continue
				}
				message := "last known availability failure"
				if tc.stale {
					message = "Condition was last reported for generation 1; current resource generation is 2. " + message
				}
				if len(got) != 1 || got[0].Reason != "Ready: Unavailable" || got[0].Message != message || got[0].OnsetUnknown != tc.unknownTime {
					t.Fatalf("independent observation changed: %+v", got)
				}
				if !tc.unknownTime && !got[0].FirstSeen.Equal(failedAt) {
					t.Fatalf("failure inherited pause timestamp: %v, want %v", got[0].FirstSeen, failedAt)
				}
			}
		})
	}
}

func TestCuratedConditionsContinueAfterAnExpectedCondition(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "apiextensions.k8s.io", Version: "v1", Resource: "customresourcedefinitions"}
	u := rolloutWithConditions([]map[string]any{
		{"type": "Established", "status": "False", "reason": "Creating"},
		{"type": "NamesAccepted", "status": "False", "reason": "NameConflict", "message": "name is already in use"},
	})
	u.SetName("widgets.example.com")
	p := &fakeProvider{
		dynamic:    map[schema.GroupVersionResource][]*unstructured.Unstructured{gvr: {u}},
		kinds:      map[schema.GroupVersionResource]string{gvr: "CustomResourceDefinition"},
		namespaced: map[schema.GroupVersionResource]bool{gvr: false},
	}
	got := Compose(p, Filters{})
	if len(got) != 1 || got[0].Reason != "NamesAccepted: NameConflict" || got[0].Severity != SeverityCritical {
		t.Fatalf("expected critical name conflict, got %+v", got)
	}
	if !got[0].OnsetUnknown || !got[0].FirstSeen.IsZero() || got[0].IssueTiming != "" {
		t.Fatalf("missing transition time must remain unknown: %+v", got[0])
	}
}

// TestArgoRolloutFailure pins that the Rollout reader prefers the definitive
// root cause (InvalidSpec, then ProgressDeadlineExceeded) over the generic
// Healthy=False/RolloutHealthy that FindFalseCondition surfaces first.
func TestArgoRolloutFailure(t *testing.T) {
	// The real-cluster shape: Healthy=False appears first, but InvalidSpec=True
	// is the actionable cause and must win.
	ro := rolloutWithConditions([]map[string]any{
		{"type": "Healthy", "status": "False", "reason": "RolloutHealthy"},
		{"type": "Progressing", "status": "False", "reason": "ProgressDeadlineExceeded", "message": "deadline"},
		{"type": "InvalidSpec", "status": "True", "reason": "InvalidSpec", "message": "bad stableService"},
	})
	if r, m, _, _, ok := argoRolloutFailure(ro); !ok || r != "InvalidSpec" || m != "bad stableService" {
		t.Errorf("InvalidSpec must win: got (%q,%q,%v)", r, m, ok)
	}

	// No InvalidSpec → fall to the progress-deadline stall.
	stalled := rolloutWithConditions([]map[string]any{
		{"type": "Healthy", "status": "False", "reason": "RolloutHealthy"},
		{"type": "Progressing", "status": "False", "reason": "ProgressDeadlineExceeded", "message": "timed out"},
	})
	if r, _, _, _, ok := argoRolloutFailure(stalled); !ok || r != "Progressing: ProgressDeadlineExceeded" {
		t.Errorf("ProgressDeadlineExceeded fallback: got (%q,%v)", r, ok)
	}

	// A rollout that's merely mid-progress (no definitive failure) must NOT be
	// overridden — leave the generic reason/severity alone.
	progressing := rolloutWithConditions([]map[string]any{
		{"type": "Healthy", "status": "False", "reason": "RolloutHealthy"},
		{"type": "Progressing", "status": "True", "reason": "ReplicaSetUpdated"},
	})
	if _, _, _, _, ok := argoRolloutFailure(progressing); ok {
		t.Error("a mid-progress rollout must not be flagged as a definitive failure")
	}

	// The override condition's lastTransitionTime drives first_seen and issue_timing —
	// a valid LTT must round-trip into the returned since.
	withLTT := rolloutWithConditions([]map[string]any{
		{"type": "Healthy", "status": "False", "reason": "RolloutHealthy"},
		{"type": "InvalidSpec", "status": "True", "reason": "InvalidSpec", "message": "bad", "lastTransitionTime": time.Now().Add(-5 * time.Minute).Format(time.RFC3339)},
	})
	if _, _, transitionAt, known, ok := argoRolloutFailure(withLTT); !ok || !known || time.Since(transitionAt) < 4*time.Minute || time.Since(transitionAt) > 6*time.Minute {
		t.Errorf("valid LTT must produce transition ≈ 5m ago, got (%v, %v, %v)", transitionAt, known, ok)
	}

	// Malformed or missing LTT means "omit issue_timing"
	// rather than falling back to a wrong timestamp.
	badLTT := rolloutWithConditions([]map[string]any{
		{"type": "InvalidSpec", "status": "True", "reason": "InvalidSpec", "lastTransitionTime": "not-a-timestamp"},
	})
	if _, _, transitionAt, known, ok := argoRolloutFailure(badLTT); !ok || known || !transitionAt.IsZero() {
		t.Errorf("malformed LTT must produce unknown transition, got (%v, %v, %v)", transitionAt, known, ok)
	}
}

func TestIsTransientCRDConditionArgoRolloutPause(t *testing.T) {
	paused := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "argoproj.io/v1alpha1",
		"kind":       "Rollout",
		"status": map[string]any{
			"phase": "Paused",
			"conditions": []any{
				map[string]any{"type": "Healthy", "status": "False", "reason": "RolloutHealthy"},
			},
		},
	}}
	if !isTransientCRDCondition(paused, "RolloutHealthy") {
		t.Fatal("an intentional Argo Rollout pause must not become a stalled-rollout issue")
	}

	paused.Object["status"].(map[string]any)["phase"] = "Progressing"
	paused.Object["status"].(map[string]any)["pauseConditions"] = []any{
		map[string]any{"reason": "InconclusiveAnalysisRun"},
	}
	if !isTransientCRDCondition(paused, "RolloutHealthy") {
		t.Fatal("a Rollout paused mid-analysis must not become a stalled-rollout issue")
	}

	paused.Object["status"].(map[string]any)["conditions"] = []any{
		map[string]any{"type": "Healthy", "status": "False", "reason": "RolloutHealthy"},
		map[string]any{"type": "InvalidSpec", "status": "True", "reason": "InvalidSpec"},
	}
	if isTransientCRDCondition(paused, "RolloutHealthy") {
		t.Fatal("a paused Rollout with a definitive failure must remain an issue")
	}
}

// TestNewConditionIssue_IssueTimingSinceGuard pins the missing-timestamp guard: a condition
// with no lastTransitionTime must not produce an issue_timing. Without the guard,
// lastSeen=now makes failingFor≈0 and any old resource looks like it was
// "healthy for ages then broke" — a false runtime classification.
func TestNewConditionIssue_IssueTimingSinceGuard(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "example.io", Version: "v1", Resource: "widgets"}
	createdAt := time.Now().Add(-2 * time.Hour)

	noLTT := newConditionIssue(gvr, "Widget", "ns", "w", SeverityWarning, "Ready: Bad", "msg", time.Time{}, false, "fp", createdAt)
	if noLTT.IssueTiming != "" || noLTT.IssueTimingBasis != "" {
		t.Errorf("missing transition must omit issue_timing, got (%q, %q)", noLTT.IssueTiming, noLTT.IssueTimingBasis)
	}
	if !noLTT.OnsetUnknown || !noLTT.FirstSeen.IsZero() || noLTT.LastSeen.IsZero() {
		t.Errorf("missing transition onset = unknown:%v first:%v last:%v, want unknown with observation time only", noLTT.OnsetUnknown, noLTT.FirstSeen, noLTT.LastSeen)
	}

	withLTT := newConditionIssue(gvr, "Widget", "ns", "w", SeverityWarning, "Ready: Bad", "msg", time.Now().Add(-30*time.Minute), true, "fp", createdAt)
	if withLTT.IssueTiming != "started_after_resource_was_healthy" || withLTT.IssueTimingBasis != "condition" {
		t.Errorf("90m healthy then failing 30m must be started_after_resource_was_healthy/condition, got (%q, %q)", withLTT.IssueTiming, withLTT.IssueTimingBasis)
	}
	if withLTT.OnsetUnknown || withLTT.FirstSeen.IsZero() {
		t.Errorf("timestamped condition onset = unknown:%v first:%v, want known", withLTT.OnsetUnknown, withLTT.FirstSeen)
	}

	future := newConditionIssue(gvr, "Widget", "ns", "w", SeverityWarning, "Ready: Bad", "msg", time.Now().Add(time.Hour), true, "fp", createdAt)
	if !future.OnsetUnknown || !future.FirstSeen.IsZero() || future.IssueTiming != "" {
		t.Errorf("future condition timestamp must be rejected as onset evidence: %+v", future)
	}
}

func TestDetectGenericCRDIssues_UnknownOnsetWithoutTransitionTime(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "example.io", Version: "v1", Resource: "widgets"}
	knownLTT := time.Now().Add(-30 * time.Minute).UTC().Format(time.RFC3339)
	widget := func(name string, lastTransitionTime string) *unstructured.Unstructured {
		condition := map[string]any{
			"type": "Ready", "status": "False", "reason": "Broken", "message": "controller failed",
		}
		if lastTransitionTime != "" {
			condition["lastTransitionTime"] = lastTransitionTime
		}
		return &unstructured.Unstructured{Object: map[string]any{
			"metadata": map[string]any{"name": name, "namespace": "prod"},
			"status":   map[string]any{"conditions": []any{condition}},
		}}
	}
	p := &fakeProvider{
		dynamic: map[schema.GroupVersionResource][]*unstructured.Unstructured{
			gvr: {widget("unknown", ""), widget("known", knownLTT)},
		},
		kinds:      map[schema.GroupVersionResource]string{gvr: "Widget"},
		namespaced: map[schema.GroupVersionResource]bool{gvr: true},
	}

	out := Compose(p, Filters{Limit: NoLimit})
	if len(out) != 2 {
		t.Fatalf("generic condition issues = %d, want 2: %+v", len(out), out)
	}
	byName := map[string]Issue{}
	for _, issue := range out {
		byName[issue.Name] = issue
	}
	unknown := byName["unknown"]
	if !unknown.OnsetUnknown || !unknown.FirstSeen.IsZero() || unknown.LastSeen.IsZero() {
		t.Errorf("timestamp-less generic condition = unknown:%v first:%v last:%v", unknown.OnsetUnknown, unknown.FirstSeen, unknown.LastSeen)
	}
	known := byName["known"]
	if known.OnsetUnknown || known.FirstSeen.IsZero() {
		t.Errorf("timestamped generic condition = unknown:%v first:%v", known.OnsetUnknown, known.FirstSeen)
	}
}

// A Rollout override (InvalidSpec) without a parseable LTT must omit
// issue_timing but KEEP the generic Healthy condition's age anchor for
// FirstSeen — resetting it to compose-time would make a long-broken rollout
// look newly broken and jump the queue on every poll.
func TestDetectGenericCRDIssues_RolloutOverrideWithoutLTTKeepsAnchor(t *testing.T) {
	rolloutGVR := schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "rollouts"}
	healthyLTT := time.Now().Add(-2 * time.Hour).UTC().Format(time.RFC3339)
	p := &fakeProvider{
		dynamic: map[schema.GroupVersionResource][]*unstructured.Unstructured{
			rolloutGVR: {{
				Object: map[string]any{
					"metadata": map[string]any{"name": "checkout", "namespace": "prod"},
					"status": map[string]any{
						"conditions": []any{
							map[string]any{"type": "Healthy", "status": "False", "reason": "RolloutHealthy", "lastTransitionTime": healthyLTT},
							map[string]any{"type": "InvalidSpec", "status": "True", "reason": "InvalidSpec", "message": "bad stableService"},
						},
					},
				},
			}},
		},
		kinds:      map[schema.GroupVersionResource]string{rolloutGVR: "Rollout"},
		namespaced: map[schema.GroupVersionResource]bool{rolloutGVR: true},
	}

	got := Compose(p, Filters{})
	if len(got) != 1 {
		t.Fatalf("Compose() issues = %d, want 1: %+v", len(got), got)
	}
	iss := got[0]
	if iss.Reason != "InvalidSpec" || iss.Severity != SeverityCritical {
		t.Errorf("override must still win: reason=%q severity=%q", iss.Reason, iss.Severity)
	}
	if iss.IssueTiming != "" || iss.IssueTimingBasis != "" {
		t.Errorf("no override LTT → issue_timing must be omitted, got (%q, %q)", iss.IssueTiming, iss.IssueTimingBasis)
	}
	if age := time.Since(iss.FirstSeen); age < 90*time.Minute {
		t.Errorf("FirstSeen must keep the Healthy condition's 2h anchor, got %v ago", age)
	}
}

func TestDetectGenericCRDIssues_GatewayRouteParentConditions(t *testing.T) {
	routeGVR := schema.GroupVersionResource{Group: "gateway.networking.k8s.io", Version: "v1", Resource: "httproutes"}
	now := time.Now().Add(-10 * time.Minute).UTC().Format(time.RFC3339)
	p := &fakeProvider{
		dynamic: map[schema.GroupVersionResource][]*unstructured.Unstructured{
			routeGVR: {{
				Object: map[string]any{
					"metadata": map[string]any{"name": "web", "namespace": "prod"},
					"status": map[string]any{
						"parents": []any{
							map[string]any{
								"parentRef": map[string]any{"name": "edge", "namespace": "infra", "sectionName": "https"},
								"conditions": []any{
									map[string]any{
										"type":               "ResolvedRefs",
										"status":             "False",
										"reason":             "BackendNotFound",
										"message":            "Service prod/api does not exist",
										"lastTransitionTime": now,
									},
								},
							},
						},
					},
				},
			}},
		},
		kinds:      map[schema.GroupVersionResource]string{routeGVR: "HTTPRoute"},
		namespaced: map[schema.GroupVersionResource]bool{routeGVR: true},
	}

	got := Compose(p, Filters{})
	if len(got) != 1 {
		t.Fatalf("Compose() issues = %d, want 1: %+v", len(got), got)
	}
	if got[0].Category != issuesapi.CategoryGatewayRouteInvalid || got[0].Reason != "ResolvedRefs: BackendNotFound" {
		t.Fatalf("route issue category/reason = %q/%q, want %q/ResolvedRefs: BackendNotFound", got[0].Category, got[0].Reason, issuesapi.CategoryGatewayRouteInvalid)
	}
	if got[0].Message == "" || got[0].Message == "Service prod/api does not exist" {
		t.Fatalf("route issue should include parent context; got message %q", got[0].Message)
	}
}

// routeWithParents builds an HTTPRoute whose status.parents each carry one
// Accepted=False condition with the given (sectionName, reason, message).
func routeWithParents(gw string, listeners []struct{ section, reason, message string }) *unstructured.Unstructured {
	now := time.Now().Add(-10 * time.Minute).UTC().Format(time.RFC3339)
	parents := make([]any, len(listeners))
	for i, l := range listeners {
		parents[i] = map[string]any{
			"parentRef": map[string]any{"name": gw, "namespace": "infra", "sectionName": l.section},
			"conditions": []any{
				map[string]any{"type": "Accepted", "status": "False", "reason": l.reason, "message": l.message, "lastTransitionTime": now},
			},
		}
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"metadata": map[string]any{"name": "web", "namespace": "prod"},
		"status":   map[string]any{"parents": parents},
	}}
}

// A single gateway fault reported on several listeners with the SAME reason and
// message collapses to one gateway-level issue that NAMES the affected listeners
// (so an explicit per-listener attachment isn't silently flattened) rather than
// emitting one near-identical row per listener.
func TestDetectGatewayRouteParentIssues_CollapsesPerListenerDupes(t *testing.T) {
	routeGVR := schema.GroupVersionResource{Group: "gateway.networking.k8s.io", Version: "v1", Resource: "httproutes"}
	route := routeWithParents("primary-gateway", []struct{ section, reason, message string }{
		{"http", "NoMatchingListenerHostname", "no hostname intersections"},
		{"https", "NoMatchingListenerHostname", "no hostname intersections"},
	})
	p := &fakeProvider{
		dynamic:    map[schema.GroupVersionResource][]*unstructured.Unstructured{routeGVR: {route}},
		kinds:      map[schema.GroupVersionResource]string{routeGVR: "HTTPRoute"},
		namespaced: map[schema.GroupVersionResource]bool{routeGVR: true},
	}

	got := Compose(p, Filters{})
	if len(got) != 1 {
		t.Fatalf("per-listener dupes must collapse to 1 issue, got %d: %+v", len(got), got)
	}
	if got[0].Reason != "Accepted: NoMatchingListenerHostname" {
		t.Fatalf("reason = %q, want Accepted: NoMatchingListenerHostname", got[0].Reason)
	}
	if got[0].OnsetCoverage != nil {
		t.Fatalf("all-known listener coverage should be omitted, got %+v", got[0].OnsetCoverage)
	}
	// Names the gateway and BOTH affected listeners, and the shared detail once.
	for _, want := range []string{"Gateway infra/primary-gateway", "listeners http, https", "no hostname intersections"} {
		if !strings.Contains(got[0].Message, want) {
			t.Fatalf("collapsed message %q must contain %q", got[0].Message, want)
		}
	}
}

func TestDetectGatewayRouteParentIssues_PreservesMixedOnsetCoverage(t *testing.T) {
	routeGVR := schema.GroupVersionResource{Group: "gateway.networking.k8s.io", Version: "v1", Resource: "httproutes"}
	route := routeWithParents("primary-gateway", []struct{ section, reason, message string }{
		{"http", "NoMatchingListenerHostname", "no hostname intersections"},
		{"https", "NoMatchingListenerHostname", "no hostname intersections"},
	})
	parents, _, _ := unstructured.NestedSlice(route.Object, "status", "parents")
	second := parents[1].(map[string]any)
	condition := second["conditions"].([]any)[0].(map[string]any)
	delete(condition, "lastTransitionTime")
	if err := unstructured.SetNestedSlice(route.Object, parents, "status", "parents"); err != nil {
		t.Fatal(err)
	}
	p := &fakeProvider{
		dynamic:    map[schema.GroupVersionResource][]*unstructured.Unstructured{routeGVR: {route}},
		kinds:      map[schema.GroupVersionResource]string{routeGVR: "HTTPRoute"},
		namespaced: map[schema.GroupVersionResource]bool{routeGVR: true},
	}

	got := Compose(p, Filters{})
	if len(got) != 1 {
		t.Fatalf("mixed listener onset must collapse to 1 issue, got %d: %+v", len(got), got)
	}
	issue := got[0]
	if issue.FirstSeen.IsZero() || issue.OnsetUnknown || issue.OnsetCoverage == nil || issue.OnsetCoverage.Known != 1 || issue.OnsetCoverage.Unknown != 1 {
		t.Fatalf("mixed listener onset provenance = first:%v unknown:%v coverage:%+v", issue.FirstSeen, issue.OnsetUnknown, issue.OnsetCoverage)
	}
	if issue.IssueTiming != "" || issue.IssueTimingBasis != "" {
		t.Fatalf("mixed listener group must not claim one timing class: %q/%q", issue.IssueTiming, issue.IssueTimingBasis)
	}
}

// When listeners fail with the same reason but DIFFERENT messages, the collapse
// must keep all of them (attributed per listener) — never drop the others.
func TestDetectGatewayRouteParentIssues_DifferentMessagesAggregated(t *testing.T) {
	routeGVR := schema.GroupVersionResource{Group: "gateway.networking.k8s.io", Version: "v1", Resource: "httproutes"}
	route := routeWithParents("primary-gateway", []struct{ section, reason, message string }{
		{"http", "NoMatchingListenerHostname", "no host match for a.example.com"},
		{"https", "NoMatchingListenerHostname", "no host match for b.example.com"},
	})
	p := &fakeProvider{
		dynamic:    map[schema.GroupVersionResource][]*unstructured.Unstructured{routeGVR: {route}},
		kinds:      map[schema.GroupVersionResource]string{routeGVR: "HTTPRoute"},
		namespaced: map[schema.GroupVersionResource]bool{routeGVR: true},
	}

	got := Compose(p, Filters{})
	if len(got) != 1 {
		t.Fatalf("same-reason listeners collapse to 1 issue, got %d: %+v", len(got), got)
	}
	for _, want := range []string{"a.example.com", "b.example.com", "http", "https"} {
		if !strings.Contains(got[0].Message, want) {
			t.Fatalf("aggregated message %q must retain %q", got[0].Message, want)
		}
	}
}

// Listeners on the same gateway failing for DIFFERENT reasons are distinct
// problems and must stay as separate rows.
func TestDetectGatewayRouteParentIssues_DistinctReasonsStaySeparate(t *testing.T) {
	routeGVR := schema.GroupVersionResource{Group: "gateway.networking.k8s.io", Version: "v1", Resource: "httproutes"}
	route := routeWithParents("primary-gateway", []struct{ section, reason, message string }{
		{"http", "NoMatchingListenerHostname", "no hostname intersections"},
		{"https", "NoMatchingParent", "no matching parent"},
	})
	p := &fakeProvider{
		dynamic:    map[schema.GroupVersionResource][]*unstructured.Unstructured{routeGVR: {route}},
		kinds:      map[schema.GroupVersionResource]string{routeGVR: "HTTPRoute"},
		namespaced: map[schema.GroupVersionResource]bool{routeGVR: true},
	}

	got := Compose(p, Filters{})
	if len(got) != 2 {
		t.Fatalf("distinct reasons must stay separate, got %d issues: %+v", len(got), got)
	}
}

// The single-listener issue's identity must not change when a second listener
// starts failing the same way — otherwise acknowledgement/history is lost. The
// gateway-level fingerprint keeps the ID stable across the 1↔many boundary.
func TestDetectGatewayRouteParentIssues_StableIdentityAcrossListenerCount(t *testing.T) {
	routeGVR := schema.GroupVersionResource{Group: "gateway.networking.k8s.io", Version: "v1", Resource: "httproutes"}
	mk := func(listeners []struct{ section, reason, message string }) Issue {
		p := &fakeProvider{
			dynamic:    map[schema.GroupVersionResource][]*unstructured.Unstructured{routeGVR: {routeWithParents("primary-gateway", listeners)}},
			kinds:      map[schema.GroupVersionResource]string{routeGVR: "HTTPRoute"},
			namespaced: map[schema.GroupVersionResource]bool{routeGVR: true},
		}
		got := Compose(p, Filters{})
		if len(got) != 1 {
			t.Fatalf("want 1 issue, got %d: %+v", len(got), got)
		}
		return got[0]
	}
	one := mk([]struct{ section, reason, message string }{{"http", "NoMatchingListenerHostname", "x"}})
	two := mk([]struct{ section, reason, message string }{
		{"http", "NoMatchingListenerHostname", "x"},
		{"https", "NoMatchingListenerHostname", "x"},
	})
	if one.ID == "" || one.ID != two.ID {
		t.Fatalf("issue ID must be stable as listeners join the same fault: one=%q two=%q", one.ID, two.ID)
	}
}

func TestDetectGenericCRDIssues_PlatformConditions(t *testing.T) {
	apiGVR := schema.GroupVersionResource{Group: "apiregistration.k8s.io", Version: "v1", Resource: "apiservices"}
	crdGVR := schema.GroupVersionResource{Group: "apiextensions.k8s.io", Version: "v1", Resource: "customresourcedefinitions"}
	ts := time.Now().Add(-10 * time.Minute).UTC().Format(time.RFC3339)
	conditioned := func(name string, cond map[string]any) *unstructured.Unstructured {
		return &unstructured.Unstructured{Object: map[string]any{
			"metadata": map[string]any{"name": name},
			"status":   map[string]any{"conditions": []any{cond}},
		}}
	}
	p := &fakeProvider{
		dynamic: map[schema.GroupVersionResource][]*unstructured.Unstructured{
			apiGVR: {conditioned("v1beta1.metrics.k8s.io", map[string]any{
				"type": "Available", "status": "False", "reason": "MissingEndpoints", "message": "endpoints missing", "lastTransitionTime": ts,
			})},
			crdGVR: {conditioned("widgets.example.com", map[string]any{
				"type": "Established", "status": "False", "reason": "Installing", "message": "names not accepted", "lastTransitionTime": ts,
			})},
		},
		kinds: map[schema.GroupVersionResource]string{
			apiGVR: "APIService",
			crdGVR: "CustomResourceDefinition",
		},
		namespaced: map[schema.GroupVersionResource]bool{
			apiGVR: false,
			crdGVR: false,
		},
	}

	got := Compose(p, Filters{})
	byKind := map[string]Issue{}
	for _, iss := range got {
		byKind[iss.Kind] = iss
	}
	if byKind["APIService"].Category != issuesapi.CategoryAPIServiceUnavailable || byKind["APIService"].Severity != SeverityCritical {
		t.Fatalf("APIService issue = %+v, want critical %q", byKind["APIService"], issuesapi.CategoryAPIServiceUnavailable)
	}
	if byKind["CustomResourceDefinition"].Category != issuesapi.CategoryOperatorConditionFail || byKind["CustomResourceDefinition"].Severity != SeverityCritical {
		t.Fatalf("CRD issue = %+v, want critical operator condition", byKind["CustomResourceDefinition"])
	}
}

func TestGenericConditionSelectionHonorsCandidateGeneration(t *testing.T) {
	for _, tc := range []struct {
		name        string
		observed    *int64
		independent bool
		want        string
		message     string
	}{
		{name: "stale secondary error remains qualified", observed: ptr.To[int64](1), want: "Synced: ReconcileError", message: "Condition was last reported for generation 1; current resource generation is 2. original reconcile error"},
		{name: "current secondary error", observed: ptr.To[int64](2), want: "Synced: ReconcileError", message: "original reconcile error"},
		{name: "missing generation stays actionable", want: "Synced: ReconcileError", message: "original reconcile error"},
		{name: "zero generation stays actionable", observed: ptr.To[int64](0), want: "Synced: ReconcileError", message: "original reconcile error"},
		{name: "future generation is not known stale", observed: ptr.To[int64](3), want: "Synced: ReconcileError", message: "original reconcile error"},
		{name: "stale candidate keeps independent current error", observed: ptr.To[int64](1), independent: true, want: "Available: AvailabilityFailed", message: "current availability error"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			failure := map[string]any{"type": "Synced", "status": "False", "reason": "ReconcileError", "message": "original reconcile error"}
			if tc.observed != nil {
				failure["observedGeneration"] = *tc.observed
			}
			conds := []map[string]any{{"type": "Ready", "status": "False", "reason": "Creating", "observedGeneration": int64(2)}, failure}
			if tc.independent {
				conds = append(conds, map[string]any{"type": "Available", "status": "False", "reason": "AvailabilityFailed", "message": "current availability error", "observedGeneration": int64(2)})
			}
			u := rolloutWithConditions(conds)
			u.SetName("example")
			u.SetNamespace("test")
			u.SetGeneration(2)
			gvr := schema.GroupVersionResource{Group: "example.com", Version: "v1", Resource: "widgets"}
			p := &fakeProvider{dynamic: map[schema.GroupVersionResource][]*unstructured.Unstructured{gvr: {u}}, kinds: map[schema.GroupVersionResource]string{gvr: "Widget"}, namespaced: map[schema.GroupVersionResource]bool{gvr: true}}
			for _, grouped := range []bool{false, true} {
				got := Compose(p, Filters{Namespaces: []string{"test"}, Grouped: grouped})
				if len(got) != 1 || got[0].Reason != tc.want || got[0].Message != tc.message {
					t.Fatalf("want %s message %q got %+v", tc.want, tc.message, got)
				}
			}
		})
	}
}

func TestActionableConditionsPreserveRolloutPauseAndFailure(t *testing.T) {
	for _, failed := range []bool{false, true} {
		conds := []map[string]any{{"type": "Ready", "status": "False", "reason": "Creating"}, {"type": "Healthy", "status": "False", "reason": "RolloutHealthy"}}
		if failed {
			conds = append(conds, map[string]any{"type": "InvalidSpec", "status": "True", "reason": "InvalidSpec", "message": "bad rollout spec"})
		}
		u := rolloutWithConditions(conds)
		u.SetAPIVersion("argoproj.io/v1alpha1")
		u.SetKind("Rollout")
		u.SetName("rollout")
		u.SetNamespace("test")
		u.Object["status"].(map[string]any)["phase"] = "Paused"
		gvr := schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "rollouts"}
		p := &fakeProvider{dynamic: map[schema.GroupVersionResource][]*unstructured.Unstructured{gvr: {u}}, kinds: map[schema.GroupVersionResource]string{gvr: "Rollout"}, namespaced: map[schema.GroupVersionResource]bool{gvr: true}}
		got := Compose(p, Filters{})
		if !failed {
			if len(got) != 0 {
				t.Fatalf("paused rollout became noisy: %+v", got)
			}
		} else if len(got) != 1 || got[0].Severity != SeverityCritical || got[0].Reason != "InvalidSpec" || got[0].Message != "bad rollout spec" {
			t.Fatalf("failure lost after expected condition: %+v", got)
		}
	}
}

func TestCuratedGatewayConditionsHonorCandidateGeneration(t *testing.T) {
	for _, tc := range []struct {
		name, kind, plural                       string
		acceptedGeneration, programmedGeneration int64
		want                                     string
	}{
		{name: "all stale stays visible", kind: "Gateway", plural: "gateways", acceptedGeneration: 1, programmedGeneration: 1, want: "Accepted: ListenersNotValid"},
		{name: "stale accepted keeps current programmed failure", kind: "Gateway", plural: "gateways", acceptedGeneration: 1, programmedGeneration: 2, want: "Programmed: AddressNotAssigned"},
		{name: "current accepted failure", kind: "Gateway", plural: "gateways", acceptedGeneration: 2, programmedGeneration: 1, want: "Accepted: ListenersNotValid"},
		{name: "stale class failure stays visible", kind: "GatewayClass", plural: "gatewayclasses", acceptedGeneration: 1, want: "Accepted: InvalidParameters"},
		{name: "current class failure", kind: "GatewayClass", plural: "gatewayclasses", acceptedGeneration: 2, want: "Accepted: InvalidParameters"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reason := "ListenersNotValid"
			if tc.kind == "GatewayClass" {
				reason = "InvalidParameters"
			}
			conds := []map[string]any{{"type": "Accepted", "status": "False", "reason": reason, "observedGeneration": tc.acceptedGeneration}}
			if tc.programmedGeneration > 0 {
				conds = append(conds, map[string]any{"type": "Programmed", "status": "False", "reason": "AddressNotAssigned", "observedGeneration": tc.programmedGeneration})
			}
			u := rolloutWithConditions(conds)
			u.SetName("example")
			u.SetGeneration(2)
			u.Object["status"].(map[string]any)["observedGeneration"] = int64(2)
			gvr := schema.GroupVersionResource{Group: "gateway.networking.k8s.io", Version: "v1", Resource: tc.plural}
			p := &fakeProvider{dynamic: map[schema.GroupVersionResource][]*unstructured.Unstructured{gvr: {u}}, kinds: map[schema.GroupVersionResource]string{gvr: tc.kind}, namespaced: map[schema.GroupVersionResource]bool{gvr: tc.kind == "Gateway"}}
			got := Compose(p, Filters{})
			if len(got) != 1 || got[0].Reason != tc.want || got[0].Severity != SeverityWarning {
				t.Fatalf("want warning %s got %+v", tc.want, got)
			}
			if len(got) == 1 {
				staleSelected := strings.HasPrefix(tc.want, "Accepted:") && tc.acceptedGeneration == 1
				if strings.Contains(got[0].Message, "generation 1") != staleSelected {
					t.Fatalf("generation certainty wrong: %+v", got[0])
				}
			}
		})
	}
}

func TestCurrentConditionGenerationDoesNotReviveOlderRepresentation(t *testing.T) {
	u := rolloutWithConditions([]map[string]any{{"type": "Ready", "status": "False", "reason": "Unavailable", "observedGeneration": int64(2)}})
	u.SetName("example")
	u.SetGeneration(2)
	u.Object["status"].(map[string]any)["v1beta2"] = map[string]any{"conditions": []any{map[string]any{"type": "Ready", "status": "False", "reason": "Unavailable", "observedGeneration": int64(1)}}}
	if c, found := findActionableCondition(u); !found || c.ObservedGeneration != 1 || !strings.Contains(c.Message, "generation 1") {
		t.Fatalf("current representation must stay authoritative and qualified: %+v found=%v", c, found)
	}
}
