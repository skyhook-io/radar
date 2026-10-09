package context

import (
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func argoObj(kind string, labels map[string]string, spec, status map[string]any) *unstructured.Unstructured {
	obj := map[string]any{
		"apiVersion": "argoproj.io/v1alpha1",
		"kind":       kind,
		"metadata":   map[string]any{"name": "web", "namespace": "prod"},
	}
	if labels != nil {
		metadata := obj["metadata"].(map[string]any)
		asAny := map[string]any{}
		for k, v := range labels {
			asAny[k] = v
		}
		metadata["labels"] = asAny
	}
	if spec != nil {
		obj["spec"] = spec
	}
	if status != nil {
		obj["status"] = status
	}
	return &unstructured.Unstructured{Object: obj}
}

func TestSummarizeArgoRollout_ExplainsWhyItIsStuck(t *testing.T) {
	tests := []struct {
		name      string
		status    map[string]any
		wantIssue string
	}{
		{
			name:      "manual pause step",
			status:    map[string]any{"phase": "Paused", "pauseConditions": []any{map[string]any{"reason": "CanaryPauseStep"}}},
			wantIssue: "CanaryPauseStep",
		},
		{
			name: "inconclusive analysis names the run",
			status: map[string]any{"phase": "Paused",
				"pauseConditions": []any{map[string]any{"reason": "InconclusiveAnalysisRun"}},
				"canary": map[string]any{"currentStepAnalysisRunStatus": map[string]any{
					"name": "web-6c4f-2", "status": "Inconclusive"}}},
			wantIssue: "analysis inconclusive (AnalysisRun web-6c4f-2)",
		},
		{
			name:      "abort takes precedence over pause reasons",
			status:    map[string]any{"phase": "Degraded", "abort": true, "pauseConditions": []any{map[string]any{"reason": "CanaryPauseStep"}}},
			wantIssue: "aborted",
		},
		{
			name:      "falls back to the controller message",
			status:    map[string]any{"phase": "Degraded", "message": "ProgressDeadlineExceeded"},
			wantIssue: "ProgressDeadlineExceeded",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			summary := summarizeUnstructured(argoObj("Rollout", nil, map[string]any{"replicas": int64(3)}, tt.status))
			if summary.Kind != "Rollout" {
				t.Fatalf("kind = %q, want Rollout", summary.Kind)
			}
			if !strings.Contains(summary.Issue, tt.wantIssue) {
				t.Fatalf("issue = %q, want it to contain %q", summary.Issue, tt.wantIssue)
			}
		})
	}
}

func TestSummarizeArgoRollout_HealthyHasNoIssue(t *testing.T) {
	summary := summarizeUnstructured(argoObj("Rollout", nil,
		map[string]any{"replicas": int64(3)},
		map[string]any{"phase": "Healthy", "readyReplicas": int64(3)}))
	if summary.Issue != "" {
		t.Fatalf("healthy Rollout reported issue %q", summary.Issue)
	}
	if summary.Ready != "3/3" {
		t.Fatalf("ready = %q, want 3/3", summary.Ready)
	}
}

// Argo narrates ordinary progress in status.message, which is not an issue.
func TestSummarizeArgoRollout_ProgressNarrationIsNotAnIssue(t *testing.T) {
	for _, message := range []string{"more replicas need to be updated", "old replicas are pending termination"} {
		summary := summarizeUnstructured(argoObj("Rollout", nil,
			map[string]any{"replicas": int64(3)},
			map[string]any{"phase": "Progressing", "message": message}))
		if summary.Issue != "" {
			t.Errorf("progressing Rollout reported issue %q for message %q", summary.Issue, message)
		}
	}
}

func TestSummarizeArgoAnalysisRun_NamesTheFailingMetric(t *testing.T) {
	summary := summarizeUnstructured(argoObj("AnalysisRun",
		map[string]string{"rollout-type": "Step", "step-index": "2"}, nil,
		map[string]any{
			"phase": "Failed",
			"metricResults": []any{
				map[string]any{"name": "success-rate", "phase": "Failed"},
				map[string]any{"name": "latency", "phase": "Successful"},
			},
		}))

	if summary.Kind != "AnalysisRun" {
		t.Fatalf("kind = %q, want AnalysisRun", summary.Kind)
	}
	if summary.Status != "Failed" {
		t.Fatalf("status = %q, want Failed", summary.Status)
	}
	if summary.Type != "Step" {
		t.Fatalf("type = %q, want Step", summary.Type)
	}
	if !strings.Contains(summary.Issue, "success-rate failed") {
		t.Fatalf("issue = %q, want the failing metric named", summary.Issue)
	}
	if strings.Contains(summary.Issue, "latency") {
		t.Fatalf("issue = %q, should not list passing metrics", summary.Issue)
	}
}

// A dryRun metric cannot fail the run, so naming it as the issue would have an
// agent abort a rollout Argo considers Successful.
func TestSummarizeArgoAnalysisRun_IgnoresDryRunMetrics(t *testing.T) {
	summary := summarizeUnstructured(argoObj("AnalysisRun", nil, nil,
		map[string]any{
			"phase": "Successful",
			"metricResults": []any{
				map[string]any{"name": "success-rate", "phase": "Successful"},
				map[string]any{"name": "shadow-check", "phase": "Failed", "dryRun": true},
			},
		}))

	if summary.Issue != "" {
		t.Fatalf("issue = %q, want empty (only a dry-run metric failed)", summary.Issue)
	}
}

// The dryRun field is omitempty, so an absent value must read as a scored metric.
func TestSummarizeArgoAnalysisRun_AbsentDryRunStillCounts(t *testing.T) {
	summary := summarizeUnstructured(argoObj("AnalysisRun", nil, nil,
		map[string]any{
			"phase": "Failed",
			"metricResults": []any{
				map[string]any{"name": "success-rate", "phase": "Failed"},
			},
		}))

	if !strings.Contains(summary.Issue, "success-rate failed") {
		t.Fatalf("issue = %q, want the failing metric named", summary.Issue)
	}
}

func TestSummarizeArgoAnalysisRun_MessageIsOnlyAnIssueOnABadVerdict(t *testing.T) {
	tests := []struct {
		name      string
		status    map[string]any
		wantIssue string
	}{
		{
			name:      "failed run with no scored metric falls back to the message",
			status:    map[string]any{"phase": "Failed", "message": "Run Failed"},
			wantIssue: "Run Failed",
		},
		{
			name:      "errored run falls back to the message",
			status:    map[string]any{"phase": "Error", "message": `Unable to resolve metric arguments: secrets "db-creds" not found`},
			wantIssue: "Unable to resolve metric arguments",
		},
		{
			name:      "inconclusive run falls back to the message",
			status:    map[string]any{"phase": "Inconclusive", "message": "Metric assessed Inconclusive"},
			wantIssue: "Metric assessed Inconclusive",
		},
		{
			name:      "run stopped by the controller is not an issue",
			status:    map[string]any{"phase": "Successful", "message": "Run Terminated"},
			wantIssue: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			summary := summarizeUnstructured(argoObj("AnalysisRun", nil, nil, tt.status))
			if tt.wantIssue == "" {
				if summary.Issue != "" {
					t.Fatalf("issue = %q, want empty", summary.Issue)
				}
				return
			}
			if !strings.Contains(summary.Issue, tt.wantIssue) {
				t.Fatalf("issue = %q, want it to contain %q", summary.Issue, tt.wantIssue)
			}
		})
	}
}
