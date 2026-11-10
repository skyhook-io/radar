package prometheus

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/skyhook-io/radar/pkg/prom/promtest"
)

func TestCNPGDiskGrowthPromQL(t *testing.T) {
	image := os.Getenv("RADAR_TEST_PROMTOOL_IMAGE")
	if image == "" {
		t.Skip("set RADAR_TEST_PROMTOOL_IMAGE=prom/prometheus:v3.5.0 to evaluate queries with Docker")
	}
	q := &fakeCNPGQuerier{}
	if _, err := queryCNPGDiskGrowth(context.Background(), q, "db", []string{"fresh", "stopped", "zero", "negative"}, 6*time.Hour, `cluster_id="current"`); err != nil {
		t.Fatal(err)
	}
	if len(q.queries) != 1 {
		t.Fatalf("queries = %d, want one namespace query", len(q.queries))
	}
	input := []map[string]string{
		{"series": `kubelet_volume_stats_used_bytes{namespace="db",persistentvolumeclaim="fresh",cluster_id="current",job="current"}`, "values": "0+100x360"},
		{"series": `kubelet_volume_stats_used_bytes{namespace="db",persistentvolumeclaim="stopped",cluster_id="current"}`, "values": "0+200x120"},
		{"series": `kubelet_volume_stats_used_bytes{namespace="db",persistentvolumeclaim="zero",cluster_id="current"}`, "values": "0x360"},
		{"series": `kubelet_volume_stats_used_bytes{namespace="db",persistentvolumeclaim="negative",cluster_id="current"}`, "values": "100000-100x360"},
		{"series": `kubelet_volume_stats_used_bytes{namespace="db",persistentvolumeclaim="fresh",cluster_id="other"}`, "values": "0+1000x360"},
		{"series": `kubelet_volume_stats_used_bytes{namespace="db",persistentvolumeclaim="fresh",cluster_id="current",job="stopped"}`, "values": "0+400x120"},
	}
	tests := []map[string]any{{
		"expr":      q.queries[0],
		"eval_time": "6h",
		"exp_samples": []map[string]any{
			{"labels": `{persistentvolumeclaim="fresh"}`, "value": 6000},
			{"labels": `{persistentvolumeclaim="zero"}`, "value": 0},
			{"labels": `{persistentvolumeclaim="negative"}`, "value": -6000},
		},
	}}
	promtest.Run(t, image, input, tests)
}
