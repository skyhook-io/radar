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
	if r, m, ok := argoRolloutFailure(ro); !ok || r != "InvalidSpec" || m != "bad stableService" {
		t.Errorf("InvalidSpec must win: got (%q,%q,%v)", r, m, ok)
	}

	// No InvalidSpec → fall to the progress-deadline stall.
	stalled := rolloutWithConditions([]map[string]any{
		{"type": "Healthy", "status": "False", "reason": "RolloutHealthy"},
		{"type": "Progressing", "status": "False", "reason": "ProgressDeadlineExceeded", "message": "timed out"},
	})
	if r, _, ok := argoRolloutFailure(stalled); !ok || r != "Progressing: ProgressDeadlineExceeded" {
		t.Errorf("ProgressDeadlineExceeded fallback: got (%q,%v)", r, ok)
	}

	// A rollout that's merely mid-progress (no definitive failure) must NOT be
	// overridden — leave the generic reason/severity alone.
	progressing := rolloutWithConditions([]map[string]any{
		{"type": "Healthy", "status": "False", "reason": "RolloutHealthy"},
		{"type": "Progressing", "status": "True", "reason": "ReplicaSetUpdated"},
	})
	if _, _, ok := argoRolloutFailure(progressing); ok {
		t.Error("a mid-progress rollout must not be flagged as a definitive failure")
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
