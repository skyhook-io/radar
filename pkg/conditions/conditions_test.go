package conditions

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestIsInProgressForIssues(t *testing.T) {
	// In-progress reasons are suppressed; genuine failures that merely LOOK
	// transient (ArtifactFailed/ChartNotReady) are NOT.
	cases := map[string]bool{
		"Progressing":    true,
		"Reconciling":    true,
		"Pending":        true,
		"ArtifactFailed": false, // genuine stuck failure — must surface
		"ChartNotReady":  false, // genuine stuck failure — must surface
		"BuildFailed":    false, // not transient at all
		"":               false,
	}
	for reason, want := range cases {
		if got := IsInProgressForIssues(reason); got != want {
			t.Errorf("IsInProgressForIssues(%q) = %v, want %v", reason, got, want)
		}
	}
	// The genuine-failure reasons ARE in the health-display transient set.
	for _, r := range []string{"ArtifactFailed", "ChartNotReady"} {
		if !IsTransientConditionReason(r) || !IsGenuineFailureReason(r) {
			t.Errorf("%q should be both transient (health) and genuine-failure (issues)", r)
		}
	}
}

func cond(status map[string]any) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{"status": status}}
}

func TestFindFalseCondition(t *testing.T) {
	readyFalse := cond(map[string]any{"conditions": []any{
		map[string]any{"type": "Ready", "status": "False", "reason": "InstallFailed", "message": "boom"},
	}})
	ct, reason, msg, _, ok := FindFalseCondition(readyFalse, "Ready")
	if !ok || ct != "Ready" || reason != "InstallFailed" || msg != "boom" {
		t.Fatalf("got (%q,%q,%q,%v), want Ready/InstallFailed/boom/true", ct, reason, msg, ok)
	}

	readyTrue := cond(map[string]any{"conditions": []any{
		map[string]any{"type": "Ready", "status": "True"},
	}})
	if _, _, _, _, ok := FindFalseCondition(readyTrue); ok {
		t.Error("a True condition must not be reported as a False-condition hit")
	}

	v1b2 := cond(map[string]any{"v1beta2": map[string]any{"conditions": []any{
		map[string]any{"type": "Available", "status": "False"},
	}}})
	if _, _, _, _, ok := FindFalseCondition(v1b2); !ok {
		t.Error("status.v1beta2.conditions should be read")
	}
}

func TestFindFalseCondition_V1beta2TakesPrecedence(t *testing.T) {
	// When both slices carry a False condition, v1beta2 is the authoritative
	// one (CAPI v1beta2 is the forward shape) and must win.
	both := cond(map[string]any{
		"v1beta2": map[string]any{"conditions": []any{
			map[string]any{"type": "Ready", "status": "False", "reason": "V1Beta2Reason"},
		}},
		"conditions": []any{
			map[string]any{"type": "Ready", "status": "False", "reason": "V1Beta1Reason"},
		},
	})
	_, reason, _, _, ok := FindFalseCondition(both, "Ready")
	if !ok || reason != "V1Beta2Reason" {
		t.Fatalf("got reason=%q ok=%v, want V1Beta2Reason/true (v1beta2 must take precedence)", reason, ok)
	}
}
