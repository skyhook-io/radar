package rollouts

import (
	"context"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func analysisRunForTest(namespace, name, ownerUID string, mutate func(map[string]any)) *unstructured.Unstructured {
	run := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "argoproj.io/v1alpha1",
		"kind":       "AnalysisRun",
		"metadata": map[string]any{
			"namespace": namespace,
			"name":      name,
			"labels":    map[string]any{"rollout-type": "Step", "step-index": "1"},
			"ownerReferences": []any{map[string]any{
				"apiVersion": "argoproj.io/v1alpha1",
				"kind":       "Rollout",
				"name":       "web",
				"uid":        ownerUID,
			}},
		},
		"status": map[string]any{
			"phase": "Successful",
			"metricResults": []any{
				map[string]any{"name": "success-rate", "phase": "Successful"},
				map[string]any{"name": "latency", "phase": "Failed"},
				map[string]any{"name": "dry-metric", "phase": "Successful", "dryRun": true},
			},
		},
	}}
	if mutate != nil {
		mutate(run.Object)
	}
	return run
}

func TestListAnalysisRunsFiltersByOwnerAndSortsNewestFirst(t *testing.T) {
	ro := rolloutForTest("prod", "web", nil)
	client := newFakeRollouts(
		ro,
		analysisRunForTest("prod", "web-run-1", "rollout-uid", func(o map[string]any) {
			o["metadata"].(map[string]any)["creationTimestamp"] = "2026-01-01T00:00:00Z"
		}),
		analysisRunForTest("prod", "web-run-2", "rollout-uid", func(o map[string]any) {
			o["metadata"].(map[string]any)["creationTimestamp"] = "2026-01-02T00:00:00Z"
		}),
		analysisRunForTest("prod", "other-run", "different-uid", nil),
	)

	summaries, err := ListAnalysisRuns(context.Background(), client, "prod", "web")
	if err != nil {
		t.Fatalf("ListAnalysisRuns: %v", err)
	}
	if len(summaries) != 2 {
		t.Fatalf("got %d summaries, want 2 (foreign AnalysisRun leaked in)", len(summaries))
	}
	if summaries[0].Name != "web-run-2" {
		t.Errorf("summaries not newest-first: %q", summaries[0].Name)
	}
}

func TestSummarizeAnalysisRunExcludesDryRunFromMetricCounts(t *testing.T) {
	run := analysisRunForTest("prod", "web-run-1", "rollout-uid", nil)
	summary := summarizeAnalysisRun(run)

	if summary.Phase != "Successful" {
		t.Errorf("Phase = %q, want Successful", summary.Phase)
	}
	if summary.Trigger != "Step" {
		t.Errorf("Trigger = %q, want Step", summary.Trigger)
	}
	if summary.StepIndex == nil || *summary.StepIndex != 1 {
		t.Errorf("StepIndex = %v, want 1", summary.StepIndex)
	}
	// 2 scored (success-rate, latency) + 1 dryRun excluded from the tally.
	if summary.MetricsTotal != 2 {
		t.Errorf("MetricsTotal = %d, want 2 (dryRun metric excluded)", summary.MetricsTotal)
	}
	if summary.MetricsPassing != 1 {
		t.Errorf("MetricsPassing = %d, want 1", summary.MetricsPassing)
	}
	if summary.MetricsNotPassing != 1 {
		t.Errorf("MetricsNotPassing = %d, want 1", summary.MetricsNotPassing)
	}
}

func TestListAnalysisRunsOrdersTiesDeterministically(t *testing.T) {
	// Argo creates a step run and a background run on the same reconcile, so
	// both carry the same creation second and time alone cannot order them.
	// A step-triggered run is the one an operator is looking for, so it leads.
	sameSecond := func(o map[string]any) {
		o["metadata"].(map[string]any)["creationTimestamp"] = "2026-01-01T00:00:00Z"
	}
	step := analysisRunForTest("prod", "web-step", "rollout-uid", sameSecond)
	background := analysisRunForTest("prod", "web-background", "rollout-uid", func(o map[string]any) {
		sameSecond(o)
		o["metadata"].(map[string]any)["labels"] = map[string]any{"rollout-type": "Background"}
	})

	summaries, err := ListAnalysisRuns(context.Background(), newFakeRollouts(rolloutForTest("prod", "web", nil), step, background), "prod", "web")
	if err != nil {
		t.Fatalf("ListAnalysisRuns: %v", err)
	}
	if len(summaries) != 2 {
		t.Fatalf("got %d summaries, want 2", len(summaries))
	}
	if summaries[0].Name != "web-step" || summaries[1].Name != "web-background" {
		t.Errorf("same-second runs ordered %q then %q, want web-step then web-background",
			summaries[0].Name, summaries[1].Name)
	}
}

func TestListAnalysisRunsOrdersTwoStepRunsBySameSecond(t *testing.T) {
	// Two step runs in the same second: the later step leads, so a retry of a
	// later step does not sort under an earlier one.
	sameSecond := func(step string) func(map[string]any) {
		return func(o map[string]any) {
			m := o["metadata"].(map[string]any)
			m["creationTimestamp"] = "2026-01-01T00:00:00Z"
			m["labels"] = map[string]any{"rollout-type": "Step", "step-index": step}
		}
	}
	first := analysisRunForTest("prod", "web-step-1", "rollout-uid", sameSecond("1"))
	third := analysisRunForTest("prod", "web-step-3", "rollout-uid", sameSecond("3"))

	summaries, err := ListAnalysisRuns(context.Background(), newFakeRollouts(rolloutForTest("prod", "web", nil), first, third), "prod", "web")
	if err != nil {
		t.Fatalf("ListAnalysisRuns: %v", err)
	}
	if len(summaries) != 2 {
		t.Fatalf("got %d summaries, want 2", len(summaries))
	}
	if summaries[0].Name != "web-step-3" || summaries[1].Name != "web-step-1" {
		t.Errorf("ordered %q then %q, want web-step-3 then web-step-1",
			summaries[0].Name, summaries[1].Name)
	}
}

func TestSummarizeAnalysisRunRejectsAMalformedStepIndex(t *testing.T) {
	// A truncated read would give this run a step index of 3 and sort it as a
	// step run, so the label has to be rejected rather than salvaged.
	run := analysisRunForTest("prod", "web-run-1", "rollout-uid", func(o map[string]any) {
		o["metadata"].(map[string]any)["labels"] = map[string]any{"rollout-type": "Step", "step-index": "3abc"}
	})

	if got := summarizeAnalysisRun(run); got.StepIndex != nil {
		t.Errorf("StepIndex = %d, want nil for a malformed label", *got.StepIndex)
	}
}
