package rollouts

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
)

var analysisRunGVR = schema.GroupVersionResource{
	Group:    "argoproj.io",
	Version:  "v1alpha1",
	Resource: "analysisruns",
}

// AnalysisRunSummary is one entry in a Rollout's AnalysisRun history — the
// full set, not just the 4 slots status.canary/blueGreen point at as
// "currently active" (see pkg/topology's activeAnalysisRuns, which is
// deliberately restricted to those for graphing; this listing has no such
// bound since it's a plain history view, not a graph that must stay small).
type AnalysisRunSummary struct {
	Name    string `json:"name"`
	Phase   string `json:"phase"`
	Message string `json:"message,omitempty"`
	// Trigger comes from the "rollout-type" label Argo Rollouts sets on every
	// run it creates: Step / Background / PrePromotion / PostPromotion.
	Trigger string `json:"trigger,omitempty"`
	// StepIndex is only set for a Step-triggered run (the "step-index" label).
	StepIndex *int64    `json:"stepIndex,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
	// Metric counts mirror resource-utils.ts's summarizeAnalysisMetrics:
	// dryRun results are excluded from the verdict tally.
	MetricsTotal      int `json:"metricsTotal"`
	MetricsPassing    int `json:"metricsPassing"`
	MetricsNotPassing int `json:"metricsNotPassing"`
}

// ListAnalysisRuns returns every AnalysisRun owned by the Rollout, newest first.
func ListAnalysisRuns(ctx context.Context, client dynamic.Interface, namespace, name string) ([]AnalysisRunSummary, error) {
	ro, err := client.Resource(GVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to get Rollout %s/%s: %w", namespace, name, err)
	}

	runList, err := client.Resource(analysisRunGVR).Namespace(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to list AnalysisRuns in %s: %w", namespace, err)
	}

	uid := string(ro.GetUID())
	var summaries []AnalysisRunSummary
	for i := range runList.Items {
		run := &runList.Items[i]
		if !ownedBy(run, uid) {
			continue
		}
		summaries = append(summaries, summarizeAnalysisRun(run))
	}
	// Runs the controller creates together (a step run and a background run
	// starting on the same reconcile) share a creation second, so time alone
	// leaves their order to chance and the list reshuffles between loads.
	sort.Slice(summaries, func(i, j int) bool {
		if !summaries[i].CreatedAt.Equal(summaries[j].CreatedAt) {
			return summaries[i].CreatedAt.After(summaries[j].CreatedAt)
		}
		si, sj := summaries[i].StepIndex, summaries[j].StepIndex
		if si != nil && sj != nil && *si != *sj {
			return *si > *sj
		}
		if (si == nil) != (sj == nil) {
			return si != nil
		}
		return summaries[i].Name < summaries[j].Name
	})
	return summaries, nil
}

func summarizeAnalysisRun(run *unstructured.Unstructured) AnalysisRunSummary {
	labels := run.GetLabels()
	summary := AnalysisRunSummary{
		Name:      run.GetName(),
		CreatedAt: run.GetCreationTimestamp().Time,
		Trigger:   labels["rollout-type"],
	}
	if step, ok := labels["step-index"]; ok {
		if n, err := parseInt64(step); err == nil {
			summary.StepIndex = &n
		}
	}

	phase, _, _ := unstructured.NestedString(run.Object, "status", "phase")
	summary.Phase = phase
	message, _, _ := unstructured.NestedString(run.Object, "status", "message")
	summary.Message = message

	results, _, _ := unstructured.NestedSlice(run.Object, "status", "metricResults")
	for _, r := range results {
		metric, ok := r.(map[string]any)
		if !ok {
			continue
		}
		if dryRun, _ := metric["dryRun"].(bool); dryRun {
			continue
		}
		summary.MetricsTotal++
		switch metric["phase"] {
		case "Successful":
			summary.MetricsPassing++
		case "Failed", "Error", "Inconclusive":
			summary.MetricsNotPassing++
		}
	}

	return summary
}

// The step index orders runs the controller created in the same second, so a
// label it cannot parse has to be rejected rather than salvaged: a partial read
// would sort the run under a step it does not belong to.
func parseInt64(s string) (int64, error) {
	return strconv.ParseInt(s, 10, 64)
}
