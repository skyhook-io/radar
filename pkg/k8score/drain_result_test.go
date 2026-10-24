package k8score

import (
	"encoding/json"
	"testing"

	corev1 "k8s.io/api/core/v1"
)

func TestDrainCompletionClassifiesActualSkips(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*corev1.Pod)
		want   DrainCompletionOutcome
	}{
		{"completed", withPhase(corev1.PodSucceeded), DrainComplete},
		{"failed record", withPhase(corev1.PodFailed), DrainComplete},
		{"static", withMirror, DrainComplete},
		{"DaemonSet", withOwner("DaemonSet", true), DrainComplete},
		{"unmanaged", withoutOwner, DrainIncomplete},
		{"emptyDir", withEmptyDir, DrainIncomplete},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pod := drainTestPod("pod", tc.mutate)
			decision := ClassifyPodForDrain(pod, DrainOptions{IgnoreDaemonSets: true}, nil)
			if decision.Outcome != DrainOutcomeSkip {
				t.Fatalf("fixture must be skipped: %+v", decision)
			}
			result := &DrainResult{SkippedPods: []PodDrainDecision{decision}}
			if got := result.CompletionOutcome(); got != tc.want {
				t.Fatalf("got %q, want %q; decision=%+v", got, tc.want, decision)
			}
			encoded, err := json.Marshal(decision)
			if err != nil {
				t.Fatal(err)
			}
			var wire map[string]json.RawMessage
			if err := json.Unmarshal(encoded, &wire); err != nil {
				t.Fatal(err)
			}
			if string(wire["expectedSkip"]) != "true" && string(wire["expectedSkip"]) != "false" {
				t.Fatalf("skip classification must be explicit on the wire: %s", encoded)
			}
		})
	}
}

func TestDrainCompletionPreservesUncertainty(t *testing.T) {
	for _, tc := range []struct {
		name   string
		result DrainResult
		want   DrainCompletionOutcome
	}{
		{"empty", DrainResult{}, DrainComplete},
		{"started", DrainResult{EvictedPods: []string{"shop/web"}}, DrainStarted},
		{"departed", DrainResult{EvictedPods: []string{"shop/web"}, WaitedForDeletion: true}, DrainComplete},
		{"observed remaining", DrainResult{PendingPods: []string{"shop/web"}}, DrainIncomplete},
		{"unconfirmed", DrainResult{UnconfirmedPods: []string{"shop/web"}, VerificationError: "list forbidden"}, DrainUnconfirmed},
		{"known failure and unconfirmed sibling", DrainResult{Errors: []string{"shop/db: eviction refused"}, UnconfirmedPods: []string{"shop/web"}, VerificationError: "list forbidden"}, DrainIncomplete},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.result.CompletionOutcome(); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}
