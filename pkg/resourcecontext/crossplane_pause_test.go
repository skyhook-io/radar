package resourcecontext

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestCrossplanePauseProjections(t *testing.T) {
	for _, tc := range []struct {
		name, annotation, syncedStatus, reason string
		want                                   CrossplanePauseState
	}{
		{"intent only", "true", "", "", CrossplanePauseRequested},
		{"acknowledged", "true", "False", "ReconcilePaused", CrossplanePauseAcknowledged},
		{"independent error", "true", "False", "ReconcileError", CrossplanePauseRequested},
		{"wrong status", "true", "True", "ReconcilePaused", CrossplanePauseRequested},
		{"unknown", "true", "Unknown", "ReconcilePaused", CrossplanePauseRequested},
		{"case mismatch", "True", "False", "ReconcilePaused", ""},
		{"false", "false", "False", "ReconcilePaused", ""},
		{"missing annotation or policy pause", "", "False", "ReconcilePaused", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u := &unstructured.Unstructured{Object: map[string]any{
				"apiVersion": "example.org/v1", "kind": "UnboundClaim",
				"metadata": map[string]any{"name": "example", "annotations": map[string]any{"crossplane.io/paused": tc.annotation}},
			}}
			if tc.syncedStatus != "" {
				u.Object["status"] = map[string]any{"conditions": []any{
					map[string]any{"type": "Ready", "status": "False", "reason": "Unavailable"},
					map[string]any{"type": "Synced", "status": tc.syncedStatus, "reason": tc.reason},
				}}
			}
			before := u.DeepCopy()
			if got := CrossplanePauseFor(u); got != tc.want {
				t.Fatalf("pause=%q, want %q", got, tc.want)
			}
			for _, tier := range []ContextTier{TierBasic, TierDiagnostic} {
				got := Build(context.Background(), u, Options{Tier: tier})
				if got.CrossplanePause != tc.want {
					t.Fatalf("tier %q pause=%q", tier, got.CrossplanePause)
				}
				if tc.syncedStatus != "" && len(got.StatusSummary.Conditions) != 2 {
					t.Fatal("raw conditions lost")
				}
			}
			got := BuildSummary(u, SummaryOptions{IssueCount: 2})
			if got.CrossplanePause != tc.want || got.IssueCount != 2 {
				t.Fatalf("summary=%+v", got)
			}
			if tc.syncedStatus != "" && got.Health != "unhealthy" {
				t.Fatalf("recorded health changed: %+v", got)
			}
			wire, err := json.Marshal(got)
			if err != nil {
				t.Fatal(err)
			}
			var fields map[string]any
			if err := json.Unmarshal(wire, &fields); err != nil {
				t.Fatal(err)
			}
			if _, present := fields["crossplanePause"]; present != (tc.want != "") {
				t.Fatalf("omitempty: %s", wire)
			}
			if !reflect.DeepEqual(before, u) {
				t.Fatal("source object mutated")
			}
		})
	}
	if got := CrossplanePauseFor(nil); got != "" {
		t.Fatalf("nil=%q", got)
	}
	var nilResource *unstructured.Unstructured
	if got := CrossplanePauseFor(nilResource); got != "" {
		t.Fatalf("typed nil=%q", got)
	}
}

func TestCrossplanePauseSummaryWithoutHealth(t *testing.T) {
	u := &unstructured.Unstructured{Object: map[string]any{"metadata": map[string]any{"annotations": map[string]any{"crossplane.io/paused": "true"}}}}
	got := BuildSummary(u, SummaryOptions{})
	if got == nil || got.CrossplanePause != CrossplanePauseRequested || got.Health != "" {
		t.Fatalf("intent-only context=%+v", got)
	}
	delete(u.Object, "metadata")
	if got := BuildSummary(u, SummaryOptions{}); got != nil {
		t.Fatalf("unrelated empty context=%+v", got)
	}
}

func TestCrossplanePauseExcludesExactBuiltinIdentity(t *testing.T) {
	for _, tc := range []struct {
		apiVersion, kind string
		want             CrossplanePauseState
	}{
		{"apps/v1", "Deployment", ""},
		{"v1", "Service", ""},
		{"apiregistration.k8s.io/v1", "APIService", ""},
		{"apiextensions.k8s.io/v1", "CustomResourceDefinition", ""},
		{"example.org/v1", "APIService", CrossplanePauseRequested},
		{"example.org/v1", "CustomResourceDefinition", CrossplanePauseRequested},
		{"example.org/v1", "Deployment", CrossplanePauseRequested},
		{"serving.knative.dev/v1", "Service", CrossplanePauseRequested},
	} {
		t.Run(tc.apiVersion+"/"+tc.kind, func(t *testing.T) {
			u := &unstructured.Unstructured{Object: map[string]any{"apiVersion": tc.apiVersion, "kind": tc.kind, "metadata": map[string]any{"annotations": map[string]any{"crossplane.io/paused": "true"}}}}
			if got := CrossplanePauseFor(u); got != tc.want {
				t.Fatalf("pause=%q, want%q", got, tc.want)
			}
			got := BuildSummary(u, SummaryOptions{})
			if tc.want == "" && got != nil {
				t.Fatalf("native conversion invented context: %+v", got)
			}
			if tc.want != "" && (got == nil || got.CrossplanePause != tc.want) {
				t.Fatalf("foreign collision lost context: %+v", got)
			}
		})
	}
}
