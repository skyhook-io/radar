package k8score

import (
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestDurationShort(t *testing.T) {
	tests := []struct {
		d    time.Duration
		want string
	}{
		{0, "0s"},
		{-5 * time.Second, "0s"},
		{59 * time.Second, "59s"},
		{60 * time.Second, "1m"},
		{90 * time.Second, "1m30s"},
		{59*time.Minute + 59*time.Second, "59m59s"},
		{time.Hour, "1h"},
		{time.Hour + time.Minute, "1h1m"},
		{23*time.Hour + 59*time.Minute, "23h59m"},
		{24 * time.Hour, "1d"},
		{49 * time.Hour, "2d"},
	}
	for _, tt := range tests {
		if got := durationShort(tt.d); got != tt.want {
			t.Errorf("durationShort(%v) = %q, want %q", tt.d, got, tt.want)
		}
	}
}

func mkHealthObj(kind, condType, condStatus string, created, ltt time.Time) *unstructured.Unstructured {
	obj := &unstructured.Unstructured{Object: map[string]any{
		"kind":     kind,
		"metadata": map[string]any{"name": "w"},
		"status": map[string]any{
			"conditions": []any{
				map[string]any{
					"type":               condType,
					"status":             condStatus,
					"lastTransitionTime": ltt.UTC().Format(time.RFC3339),
				},
			},
		},
	}}
	obj.SetCreationTimestamp(metav1.NewTime(created))
	return obj
}

func TestWorkloadHealthWarning(t *testing.T) {
	now := time.Now()

	t.Run("never healthy since creation", func(t *testing.T) {
		// Condition went False essentially at creation → continuous-failure phrasing.
		obj := mkHealthObj("Deployment", "Available", "False", now.Add(-2*time.Minute), now.Add(-2*time.Minute+time.Second))
		w := workloadHealthWarning(obj)
		if !strings.Contains(w, "continuously since this resource was created") {
			t.Errorf("want never-healthy phrasing, got %q", w)
		}
	})

	t.Run("regressed long after creation", func(t *testing.T) {
		obj := mkHealthObj("Deployment", "Available", "False", now.Add(-time.Hour), now.Add(-5*time.Minute))
		w := workloadHealthWarning(obj)
		if strings.Contains(w, "continuously since this resource was created") {
			t.Errorf("should not use never-healthy phrasing, got %q", w)
		}
		if !strings.Contains(w, "(resource age:") {
			t.Errorf("want regressed phrasing with resource age, got %q", w)
		}
	})

	t.Run("available true yields nothing", func(t *testing.T) {
		obj := mkHealthObj("Deployment", "Available", "True", now.Add(-time.Hour), now.Add(-time.Hour))
		if w := workloadHealthWarning(obj); w != "" {
			t.Errorf("healthy workload should yield no warning, got %q", w)
		}
	})

	t.Run("pod uses Ready condition", func(t *testing.T) {
		obj := mkHealthObj("Pod", "Ready", "False", now.Add(-time.Hour), now.Add(-5*time.Minute))
		if w := workloadHealthWarning(obj); !strings.Contains(w, "`Ready=False`") {
			t.Errorf("pod should report Ready, got %q", w)
		}
		// A Pod's Available condition is irrelevant — only Ready drives the warning.
		other := mkHealthObj("Pod", "Available", "False", now.Add(-time.Hour), now.Add(-5*time.Minute))
		if w := workloadHealthWarning(other); w != "" {
			t.Errorf("pod with no Ready condition should yield nothing, got %q", w)
		}
	})

	t.Run("unhandled kind yields nothing", func(t *testing.T) {
		obj := mkHealthObj("DaemonSet", "Available", "False", now.Add(-time.Hour), now.Add(-5*time.Minute))
		if w := workloadHealthWarning(obj); w != "" {
			t.Errorf("DaemonSet has no Available condition; want nothing, got %q", w)
		}
	})

	t.Run("future lastTransitionTime is guarded", func(t *testing.T) {
		// Clock skew: condition stamped in the future → negative failingFor must
		// not produce a "~-Xs" warning.
		obj := mkHealthObj("StatefulSet", "Available", "False", now.Add(-time.Hour), now.Add(5*time.Minute))
		if w := workloadHealthWarning(obj); w != "" {
			t.Errorf("future lastTransitionTime should yield nothing, got %q", w)
		}
	})

	t.Run("no conditions yields nothing", func(t *testing.T) {
		obj := &unstructured.Unstructured{Object: map[string]any{"kind": "Deployment", "metadata": map[string]any{"name": "w"}}}
		if w := workloadHealthWarning(obj); w != "" {
			t.Errorf("no conditions should yield nothing, got %q", w)
		}
	})
}
