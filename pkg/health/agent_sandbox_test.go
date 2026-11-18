package health

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestAgentSandboxVectors(t *testing.T) {
	raw, err := os.ReadFile("testdata/agent_sandbox.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Name       string
		Object     unstructured.Unstructured
		Level      Level
		Text       string
		Settled    bool
		InProgress bool
		Mirrored   bool
	}
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	for _, tc := range cases {
		t.Run(tc.Name, func(t *testing.T) {
			r := ReadAgentSandbox(&tc.Object)
			if r.Verdict.Level != tc.Level || r.Verdict.Reason != tc.Text || r.Settled != tc.Settled || r.InProgress != tc.InProgress || r.Mirrored != tc.Mirrored {
				t.Fatalf("got %+v, want level=%s text=%s settled=%v inProgress=%v mirrored=%v", r, tc.Level, tc.Text, tc.Settled, tc.InProgress, tc.Mirrored)
			}
		})
	}
}

// Kubernetes keeps Ready's transition time when only the reason changes, so a
// resume from a long suspension must be timed from the Suspended flip.
func TestAgentSandboxResumeTimedFromSuspendedFlip(t *testing.T) {
	resumed := time.Now().Add(-2 * time.Minute).UTC().Truncate(time.Second)
	u := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "agents.x-k8s.io/v1beta1", "kind": "Sandbox",
		"metadata": map[string]any{"name": "a", "generation": int64(3)},
		"status": map[string]any{"conditions": []any{
			map[string]any{"type": "Ready", "status": "False", "reason": "DependenciesNotReady", "observedGeneration": int64(3), "lastTransitionTime": "2026-11-01T00:00:00Z"},
			map[string]any{"type": "Suspended", "status": "False", "reason": "NotSuspended", "observedGeneration": int64(3), "lastTransitionTime": resumed.Format(time.RFC3339)},
		}},
	}}
	r := ReadAgentSandbox(u)
	if !r.InProgress || !r.SinceKnown || !r.Since.Equal(resumed) {
		t.Fatalf("since = %v (known %v), want %v", r.Since, r.SinceKnown, resumed)
	}
}

// A suspension can leave both Ready and Suspended False, keeping transition
// times from before it began, so its start is unknown rather than days old.
func TestAgentSandboxSuspensionStartUnknown(t *testing.T) {
	u := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "agents.x-k8s.io/v1beta1", "kind": "Sandbox",
		"metadata": map[string]any{"name": "a", "generation": int64(4)},
		"spec":     map[string]any{"operatingMode": "Suspended"},
		"status": map[string]any{"conditions": []any{
			map[string]any{"type": "Ready", "status": "False", "reason": "SandboxSuspended", "observedGeneration": int64(4), "lastTransitionTime": "2026-11-01T00:00:00Z"},
			map[string]any{"type": "Suspended", "status": "False", "reason": "PodTerminating", "observedGeneration": int64(4), "lastTransitionTime": "2026-11-01T00:00:00Z"},
		}},
	}}
	r := ReadAgentSandbox(u)
	if r.Verdict.Reason != "Suspending" || !r.InProgress || r.SinceKnown {
		t.Fatalf("got %+v, want Suspending with an unknown start", r)
	}
}

func TestAgentSandboxRefusedSuspensionNamesTheTrackedPod(t *testing.T) {
	sandbox := func(annotations map[string]any) *unstructured.Unstructured {
		meta := map[string]any{"name": "a", "namespace": "agents", "generation": int64(1)}
		if annotations != nil {
			meta["annotations"] = annotations
		}
		return &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "agents.x-k8s.io/v1beta1", "kind": "Sandbox", "metadata": meta,
			"status": map[string]any{"conditions": []any{
				map[string]any{"type": "Ready", "status": "False", "reason": "SandboxSuspended", "message": "Sandbox is suspending", "observedGeneration": int64(1)},
				map[string]any{"type": "Suspended", "status": "False", "reason": "PodNotOwned", "message": "Refused to delete pod because it is not owned by this sandbox"},
			}},
		}}
	}
	for name, tc := range map[string]struct {
		annotations map[string]any
		want        string
	}{
		"own name":       {nil, `The conflicting Pod is "a".`},
		"annotated name": {map[string]any{"agents.x-k8s.io/pod-name": "a-pod-7f2"}, `The conflicting Pod is "a-pod-7f2".`},
	} {
		r := ReadAgentSandbox(sandbox(tc.annotations))
		if !r.SuspensionRefused || !strings.Contains(r.Verdict.Message, tc.want) {
			t.Errorf("%s: got %+v, want a refusal naming %s", name, r, tc.want)
		}
	}
	// The Pod may belong to something else: its owner is checked before anyone removes it.
	r := ReadAgentSandbox(sandbox(map[string]any{"agents.x-k8s.io/pod-name": "a-pod-7f2"}))
	if want := `Check who owns it (kubectl get pod a-pod-7f2 -n agents -o jsonpath='{.metadata.ownerReferences}') before removing it, or recreate the Sandbox under another name.`; !strings.HasSuffix(r.Verdict.Message, want) {
		t.Errorf("message %q, want it to end %q", r.Verdict.Message, want)
	}
}
