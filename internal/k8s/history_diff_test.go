package k8s

import (
	"reflect"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
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

func TestDiffPodTemplateConfig_ImagePullPolicy(t *testing.T) {
	oldSpec := corev1.PodSpec{Containers: []corev1.Container{{
		Name: "app", Image: "app:v1", ImagePullPolicy: corev1.PullIfNotPresent,
	}}}
	newSpec := corev1.PodSpec{Containers: []corev1.Container{{
		Name: "app", Image: "app:v1", ImagePullPolicy: corev1.PullAlways,
	}}}

	changes, summary := diffPodTemplateConfig(oldSpec, newSpec)
	if !hasChangePath(changes, "spec.template.spec.containers[app].imagePullPolicy") {
		t.Fatalf("expected imagePullPolicy change, got %+v", changes)
	}
	if joined := strings.Join(summary, "; "); !strings.Contains(joined, "imagePullPolicy(app)") {
		t.Fatalf("expected imagePullPolicy summary, got %q", joined)
	}
}

func TestDiffPodTemplateConfig_CommandArgsRedaction(t *testing.T) {
	oldSpec := corev1.PodSpec{Containers: []corev1.Container{{
		Name:    "app",
		Image:   "app:v1",
		Command: []string{"server", "--client-secret=old-command-secret"},
		Args:    []string{"--api-key", "old-api-key", "--mode=prod", "password=old-password"},
	}}}
	newSpec := corev1.PodSpec{Containers: []corev1.Container{{
		Name:    "app",
		Image:   "app:v1",
		Command: []string{"server", "--client-secret=new-command-secret"},
		Args:    []string{"--api-key", "new-api-key", "--mode=prod", "password=new-password"},
	}}}

	changes, _ := diffPodTemplateConfig(oldSpec, newSpec)
	for _, path := range []string{
		"spec.template.spec.containers[app].command",
		"spec.template.spec.containers[app].args",
	} {
		change, ok := findChangePath(changes, path)
		if !ok {
			t.Fatalf("expected %s change, got %+v", path, changes)
		}
		values := strings.Join([]string{
			strings.Join(change.OldValue.([]string), " "),
			strings.Join(change.NewValue.([]string), " "),
		}, " ")
		for _, leaked := range []string{"old-command-secret", "new-command-secret", "old-api-key", "new-api-key", "old-password", "new-password"} {
			if strings.Contains(values, leaked) {
				t.Fatalf("%s leaked secret value %q in %+v", path, leaked, change)
			}
		}
		if !strings.Contains(values, "[REDACTED]") {
			t.Fatalf("%s did not redact secret-looking values: %+v", path, change)
		}
	}
}

// Named probe ports must round-trip through the diff — IntVal renders them all
// as 0, hiding edits and conflating distinct names.
func TestNormalizedProbe_NamedPorts(t *testing.T) {
	httpNamed := func(port intstr.IntOrString) *corev1.Probe {
		return &corev1.Probe{ProbeHandler: corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Port: port, Path: "/healthz"}}}
	}
	a := normalizedProbe(httpNamed(intstr.FromString("admin")))
	b := normalizedProbe(httpNamed(intstr.FromString("metrics")))
	c := normalizedProbe(httpNamed(intstr.FromInt32(9090)))
	if reflect.DeepEqual(a, b) {
		t.Errorf("distinct named ports must not normalize identically: %v", a)
	}
	if reflect.DeepEqual(a, c) {
		t.Errorf("named vs numeric port must differ: %v vs %v", a, c)
	}
}

func hasChangePath(changes []FieldChange, path string) bool {
	_, ok := findChangePath(changes, path)
	return ok
}

func findChangePath(changes []FieldChange, path string) (FieldChange, bool) {
	for _, change := range changes {
		if change.Path == path {
			return change, true
		}
	}
	return FieldChange{}, false
}
