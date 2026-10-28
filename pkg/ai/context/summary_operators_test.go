package context

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestCollectorPipelineSummaryCoverage(t *testing.T) {
	for _, tc := range []struct {
		name   string
		config any
		known  bool
		count  int
	}{
		{"structured", map[string]any{"service": map[string]any{"pipelines": map[string]any{"traces": map[string]any{}}}}, true, 1},
		{"alpha YAML", "service:\n  pipelines:\n    traces: {}\n    metrics: {}\n", true, 2},
		{"no declared pipelines", map[string]any{}, true, 0},
		{"absent configuration", nil, false, 0},
		{"invalid configuration type", []any{}, false, 0},
		{"invalid service", map[string]any{"service": "invalid"}, false, 0},
		{"invalid pipelines", map[string]any{"service": map[string]any{"pipelines": []any{}}}, false, 0},
		{"malformed YAML", "service: [", false, 0},
		{"duplicate keys", "service:\n  pipelines:\n    traces: {}\n    traces: {}\n", false, 0},
		{"multiple documents", "service: {}\n---\nservice: {}", false, 0},
		{"large alpha config", "#" + strings.Repeat(" ", 64*1024), false, 0},
		{"empty alpha config", "", false, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u := &unstructured.Unstructured{Object: map[string]any{"spec": map[string]any{"config": tc.config}}}
			got := collectorPipelineSummary(u)
			if (got != nil) != tc.known {
				t.Fatalf("summary=%+v; known=%v", got, tc.known)
			}
			if got != nil && got.Count != tc.count {
				t.Fatalf("count=%d; want %d", got.Count, tc.count)
			}
		})
	}
}

func TestCollectorPipelineSummaryIsBoundedAndDeterministic(t *testing.T) {
	pipelines := map[string]any{}
	for n := 30; n >= 0; n-- {
		pipelines[fmt.Sprintf("traces/%02d", n)] = map[string]any{"exporters": []any{"private-exporter-value"}}
	}
	u := &unstructured.Unstructured{Object: map[string]any{"spec": map[string]any{"config": map[string]any{"service": map[string]any{"pipelines": pipelines}}}}}
	got := collectorPipelineSummary(u)
	if got.Count != 31 || len(got.Names) != 5 || got.Names[0] != "traces/00" || got.Names[4] != "traces/04" || !got.NamesTruncated {
		t.Fatalf("summary=%+v", got)
	}
	raw, _ := json.Marshal(got)
	if strings.Contains(string(raw), "private-") || len(raw) > 300 {
		t.Fatalf("unbounded or sensitive summary: %s", raw)
	}
	long := strings.Repeat("界", 100)
	pipelines = map[string]any{long: map[string]any{}}
	u.Object["spec"] = map[string]any{"config": map[string]any{"service": map[string]any{"pipelines": pipelines}}}
	got = collectorPipelineSummary(u)
	if !got.NamesTruncated || !utf8.ValidString(got.Names[0]) || len(got.Names[0]) > 163 {
		t.Fatalf("unsafe name truncation: %+v", got)
	}
}

func TestFlaggerSummaryPreservesReportedZeroAndStrategy(t *testing.T) {
	for _, tc := range []struct {
		name, strategy string
		analysis       map[string]any
		weighted       bool
	}{
		{"native default without analysis", "Canary", nil, true},
		{"canary", "Canary", map[string]any{}, true},
		{"mirrored canary", "Canary with mirroring", map[string]any{"mirror": true}, true},
		{"blue green", "Blue/green", map[string]any{"iterations": int64(3)}, false},
		{"mirrored blue green", "Blue/green with mirroring", map[string]any{"iterations": int64(3), "mirror": true}, false},
		{"AB", "A/B testing", map[string]any{"iterations": int64(3), "match": []any{map[string]any{}}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u := &unstructured.Unstructured{Object: map[string]any{
				"apiVersion": "flagger.app/v1beta1", "kind": "Canary",
				"spec":   map[string]any{"analysis": tc.analysis},
				"status": map[string]any{"phase": "Failed", "canaryWeight": int64(0), "iterations": int64(0), "failedChecks": int64(0)},
			}}
			got := summarizeUnstructured(u)
			if got.Strategy != tc.strategy || got.Canary == nil || got.Canary.ReportedFailedChecks == nil || *got.Canary.ReportedFailedChecks != 0 {
				t.Fatalf("bad snapshot: %+v", got)
			}
			if (got.Canary.ReportedWeightPercent != nil) != tc.weighted || (got.Canary.ReportedIterations != nil) == tc.weighted {
				t.Fatalf("strategy-inappropriate counters: %+v", got.Canary)
			}
			delete(u.Object, "status")
			if absent := summarizeUnstructured(u); absent.Canary != nil {
				t.Fatalf("unreported counters became zero: %+v", absent.Canary)
			}
		})
	}
}

func TestInstrumentationSummaryOnlyNamesBoundedRecordedLanguages(t *testing.T) {
	blocked := map[string]any{}
	for n := 20; n >= 0; n-- {
		blocked[fmt.Sprintf("language-%02d", n)] = "private-status-value"
	}
	u := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "opentelemetry.io/v1alpha1", "kind": "Instrumentation", "status": map[string]any{"upgradeBlockedVersions": blocked}}}
	got := summarizeUnstructured(u)
	if got.Instrumentation == nil || got.Instrumentation.BlockedUpgradeLanguageCount != 21 || len(got.Instrumentation.BlockedUpgradeLanguages) != 10 || !got.Instrumentation.BlockedUpgradeLanguagesTruncated {
		t.Fatalf("bad language summary: %+v", got.Instrumentation)
	}
	raw, _ := json.Marshal(got)
	if strings.Contains(string(raw), "private-") {
		t.Fatalf("status values leaked: %s", raw)
	}
	u.SetAPIVersion("other.example/v1")
	if summarizeUnstructured(u).Instrumentation != nil {
		t.Fatal("foreign API group received instrumentation facts")
	}
}

func TestOperatorSummaryDispatchAndSnapshots(t *testing.T) {
	collector := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "opentelemetry.io/v1beta1", "kind": "OpenTelemetryCollector",
		"spec":   map[string]any{"config": map[string]any{}},
		"status": map[string]any{"scale": map[string]any{"statusReplicas": "1/2"}},
	}}
	got := summarizeUnstructured(collector)
	if got.Collector == nil || got.Collector.ReportedPodReadiness != "1/2" || got.Ready != "" {
		t.Fatalf("operator snapshot became runtime readiness: %+v", got)
	}
	collector.SetAPIVersion("other.example/v1")
	if summarizeUnstructured(collector).Collector != nil {
		t.Fatal("foreign Collector received native facts")
	}
	canary := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "flagger.app/v1beta1", "kind": "Canary",
		"spec":   map[string]any{"canaryAnalysis": map[string]any{"iterations": int64(4)}},
		"status": map[string]any{"iterations": int64(2)},
	}}
	got = summarizeUnstructured(canary)
	if got.Strategy != "Blue/green" || got.Canary == nil || got.Canary.ReportedIterations == nil || *got.Canary.ReportedIterations != 2 {
		t.Fatalf("deprecated native analysis was ignored: %+v", got)
	}
	canary.SetAPIVersion("other.example/v1")
	if summarizeUnstructured(canary).Canary != nil {
		t.Fatal("foreign Canary received native facts")
	}
}
