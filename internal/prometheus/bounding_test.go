package prometheus

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/skyhook-io/radar/pkg/prom"
)

func TestMaxResponseBytes(t *testing.T) {
	t.Setenv("RADAR_MCP_PROM_MAX_RESPONSE_BYTES", "")
	if got := MaxResponseBytes(); got != DefaultMaxResponseBytes {
		t.Fatalf("MaxResponseBytes() = %d, want default %d", got, DefaultMaxResponseBytes)
	}
	t.Setenv("RADAR_MCP_PROM_MAX_RESPONSE_BYTES", "50")
	if got := MaxResponseBytes(); got != 50 {
		t.Fatalf("MaxResponseBytes() = %d, want env override 50", got)
	}
	t.Setenv("RADAR_MCP_PROM_MAX_RESPONSE_BYTES", "-1")
	if got := MaxResponseBytes(); got != DefaultMaxResponseBytes {
		t.Fatalf("MaxResponseBytes() = %d, want default for a non-positive override", got)
	}
}

func TestSummarizeLargeResultOrdersLabelsByCardinality(t *testing.T) {
	result := &prom.QueryResult{Series: []prom.Series{
		{Labels: map[string]string{"pod": "a", "namespace": "x", "container": "app"}, DataPoints: []prom.DataPoint{{Timestamp: 1, Value: 1}}},
		{Labels: map[string]string{"pod": "b", "namespace": "y", "container": "app"}, DataPoints: []prom.DataPoint{{Timestamp: 1, Value: 1}}},
		{Labels: map[string]string{"pod": "c", "namespace": "x", "container": "app"}, DataPoints: []prom.DataPoint{{Timestamp: 1, Value: 1}, {Timestamp: 2, Value: 2}}},
	}}
	raw := string(SummarizeLargeResult(result, "wide_metric", false))

	var summary struct {
		SeriesCount      int            `json:"seriesCount"`
		TotalDataPoints  int            `json:"totalDataPoints"`
		LabelCardinality map[string]int `json:"labelCardinality"`
		Suggestion       string         `json:"suggestion"`
	}
	if err := json.Unmarshal([]byte(raw), &summary); err != nil {
		t.Fatalf("unmarshal summary: %v\n%s", err, raw)
	}
	if summary.SeriesCount != 3 || summary.TotalDataPoints != 4 {
		t.Errorf("counts = %d series / %d points, want 3/4", summary.SeriesCount, summary.TotalDataPoints)
	}
	if summary.LabelCardinality["pod"] != 3 || summary.LabelCardinality["namespace"] != 2 || summary.LabelCardinality["container"] != 1 {
		t.Errorf("labelCardinality = %v", summary.LabelCardinality)
	}
	if summary.Suggestion != "topk(3, wide_metric)" {
		t.Errorf("suggestion = %q", summary.Suggestion)
	}
	if !(strings.Index(raw, `"pod":`) < strings.Index(raw, `"namespace":`) && strings.Index(raw, `"namespace":`) < strings.Index(raw, `"container":`)) {
		t.Errorf("labelCardinality keys not in descending order: %s", raw)
	}

	rangeRaw := string(SummarizeLargeResult(result, "wide_metric", true))
	if !strings.Contains(rangeRaw, "points-per-series dominate") {
		t.Errorf("range summary with few series should say points dominate: %s", rangeRaw)
	}
}
