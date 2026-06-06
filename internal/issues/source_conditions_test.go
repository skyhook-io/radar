package issues

import (
	"testing"
	"time"

	"github.com/skyhook-io/radar/pkg/issuesapi"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
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
	if r, m, _, ok := argoRolloutFailure(ro); !ok || r != "InvalidSpec" || m != "bad stableService" {
		t.Errorf("InvalidSpec must win: got (%q,%q,%v)", r, m, ok)
	}

	// No InvalidSpec → fall to the progress-deadline stall.
	stalled := rolloutWithConditions([]map[string]any{
		{"type": "Healthy", "status": "False", "reason": "RolloutHealthy"},
		{"type": "Progressing", "status": "False", "reason": "ProgressDeadlineExceeded", "message": "timed out"},
	})
	if r, _, _, ok := argoRolloutFailure(stalled); !ok || r != "Progressing: ProgressDeadlineExceeded" {
		t.Errorf("ProgressDeadlineExceeded fallback: got (%q,%v)", r, ok)
	}

	// A rollout that's merely mid-progress (no definitive failure) must NOT be
	// overridden — leave the generic reason/severity alone.
	progressing := rolloutWithConditions([]map[string]any{
		{"type": "Healthy", "status": "False", "reason": "RolloutHealthy"},
		{"type": "Progressing", "status": "True", "reason": "ReplicaSetUpdated"},
	})
	if _, _, _, ok := argoRolloutFailure(progressing); ok {
		t.Error("a mid-progress rollout must not be flagged as a definitive failure")
	}

	// The override condition's lastTransitionTime drives first_seen and issue_timing —
	// a valid LTT must round-trip into the returned since.
	withLTT := rolloutWithConditions([]map[string]any{
		{"type": "Healthy", "status": "False", "reason": "RolloutHealthy"},
		{"type": "InvalidSpec", "status": "True", "reason": "InvalidSpec", "message": "bad", "lastTransitionTime": time.Now().Add(-5 * time.Minute).Format(time.RFC3339)},
	})
	if _, _, since, ok := argoRolloutFailure(withLTT); !ok || since < 4*time.Minute || since > 6*time.Minute {
		t.Errorf("valid LTT must produce since ≈ 5m, got (%v, %v)", since, ok)
	}

	// Malformed or missing LTT → since=0, which downstream means "omit issue_timing"
	// rather than falling back to a wrong timestamp.
	badLTT := rolloutWithConditions([]map[string]any{
		{"type": "InvalidSpec", "status": "True", "reason": "InvalidSpec", "lastTransitionTime": "not-a-timestamp"},
	})
	if _, _, since, ok := argoRolloutFailure(badLTT); !ok || since != 0 {
		t.Errorf("malformed LTT must produce since=0, got (%v, %v)", since, ok)
	}
}

// TestNewConditionIssue_IssueTimingSinceGuard pins the since=0 guard: a condition
// with no lastTransitionTime must not produce an issue_timing. Without the guard,
// lastSeen=now makes failingFor≈0 and any old resource looks like it was
// "healthy for ages then broke" — a false runtime classification.
func TestNewConditionIssue_IssueTimingSinceGuard(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "example.io", Version: "v1", Resource: "widgets"}
	createdAt := time.Now().Add(-2 * time.Hour)

	noLTT := newConditionIssue(gvr, "Widget", "ns", "w", SeverityWarning, "Ready: Bad", "msg", 0, "fp", createdAt)
	if noLTT.IssueTiming != "" || noLTT.IssueTimingBasis != "" {
		t.Errorf("since=0 must omit issue_timing, got (%q, %q)", noLTT.IssueTiming, noLTT.IssueTimingBasis)
	}

	withLTT := newConditionIssue(gvr, "Widget", "ns", "w", SeverityWarning, "Ready: Bad", "msg", 30*time.Minute, "fp", createdAt)
	if withLTT.IssueTiming != "started_after_resource_was_healthy" || withLTT.IssueTimingBasis != "condition" {
		t.Errorf("90m healthy then failing 30m must be started_after_resource_was_healthy/condition, got (%q, %q)", withLTT.IssueTiming, withLTT.IssueTimingBasis)
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
