package k8s

import (
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
)

// Added/removed containers must surface as a single row naming the container
// (with its image) — not vanish because per-field diffs only cover containers
// present in both templates.
func TestDiffPodTemplateConfig_ContainerAddRemove(t *testing.T) {
	oldSpec := corev1.PodSpec{Containers: []corev1.Container{
		{Name: "app", Image: "app:v1"},
		{Name: "legacy-sidecar", Image: "sidecar:v3"},
	}}
	newSpec := corev1.PodSpec{Containers: []corev1.Container{
		{Name: "app", Image: "app:v1"},
		{Name: "otel-agent", Image: "otel/agent:v2"},
	}}

	changes, summary := diffPodTemplateConfig(oldSpec, newSpec)
	joined := strings.Join(summary, "; ")
	if !strings.Contains(joined, "container otel-agent added") {
		t.Errorf("added container missing from summary: %q", joined)
	}
	if !strings.Contains(joined, "container legacy-sidecar removed") {
		t.Errorf("removed container missing from summary: %q", joined)
	}
	if len(changes) != 2 {
		t.Errorf("changes = %d, want 2 (one add, one remove): %+v", len(changes), changes)
	}
	// Unchanged shared container must not produce noise.
	if strings.Contains(joined, "app") {
		t.Errorf("unchanged container must not appear in summary: %q", joined)
	}
}
