package issues

import (
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"testing"
	"time"
)

func TestFlaggerFailedRolloutEvidence(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "flagger.app", Version: "v1beta1", Resource: "canaries"}
	u := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "flagger.app/v1beta1", "kind": "Canary", "metadata": map[string]any{"name": "app", "namespace": "test"}, "spec": map[string]any{"suspend": true}, "status": map[string]any{"phase": "Failed", "lastTransitionTime": "2026-10-27T00:00:00Z", "conditions": []any{map[string]any{"type": "Promoted", "status": "False", "reason": "Failed", "lastTransitionTime": "2026-01-01T00:00:00Z"}}}}}
	issues := detectCuratedConditionIssues(gvr, "Canary", u)
	if len(issues) != 1 || issues[0].Severity != SeverityWarning || issues[0].Reason != "FlaggerCanaryFailed" || issues[0].OnsetUnknown {
		t.Fatalf("bad failed outcome: %+v", issues)
	}
	want, _ := time.Parse(time.RFC3339, "2026-01-01T00:00:00Z")
	if !issues[0].FirstSeen.Equal(want) {
		t.Fatal("onset must be controller transition time")
	}
	for _, value := range []string{"", "invalid", "2999-01-01T00:00:00Z"} {
		_ = unstructured.SetNestedSlice(u.Object, []any{map[string]any{"type": "Promoted", "status": "False", "reason": "Failed", "lastTransitionTime": value}}, "status", "conditions")
		if got := detectFlaggerCanaryIssues(gvr, u); len(got) != 1 || !got[0].OnsetUnknown {
			t.Fatalf("invalid onset became known: %+v", got)
		}
	}
	for _, phase := range []string{"Progressing", "Waiting", "Succeeded", "Unknown", ""} {
		_ = unstructured.SetNestedField(u.Object, phase, "status", "phase")
		_ = unstructured.SetNestedSlice(u.Object, []any{map[string]any{"type": "Promoted", "status": "False"}}, "status", "conditions")
		if got := detectFlaggerCanaryIssues(gvr, u); len(got) != 0 {
			t.Fatalf("normal %s generated a finding", phase)
		}
	}
	_ = unstructured.SetNestedField(u.Object, "Failed", "status", "phase")
	gvr.Group = "other.example"
	if got := detectCuratedConditionIssues(gvr, "Canary", u); len(got) != 0 {
		t.Fatal("foreign Canary received Flagger finding")
	}
}

func TestFlaggerComposeDetectsFailureWithoutNormalPhaseNoise(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "flagger.app", Version: "v1beta1", Resource: "canaries"}
	for _, phase := range []string{"Failed", "Progressing", "Waiting", "WaitingPromotion", "Succeeded"} {
		t.Run(phase, func(t *testing.T) {
			u := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "flagger.app/v1beta1", "kind": "Canary", "metadata": map[string]any{"name": "app", "namespace": "test"}, "status": map[string]any{"phase": phase, "conditions": []any{map[string]any{"type": "Promoted", "status": "Unknown"}}}}}
			p := &fakeProvider{dynamic: map[schema.GroupVersionResource][]*unstructured.Unstructured{gvr: {u}}, kinds: map[schema.GroupVersionResource]string{gvr: "Canary"}, namespaced: map[schema.GroupVersionResource]bool{gvr: true}}
			got := Compose(p, Filters{})
			want := 0
			if phase == "Failed" {
				want = 1
			}
			if len(got) != want {
				t.Fatalf("got %+v, want %d", got, want)
			}
			if want == 1 && !got[0].OnsetUnknown {
				t.Fatal("top-level/unknown condition cannot establish failure onset")
			}
		})
	}
}
