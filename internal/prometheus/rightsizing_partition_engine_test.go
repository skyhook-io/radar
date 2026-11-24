//go:build promengine

package prometheus

import (
	"context"
	"testing"
	"time"

	"github.com/skyhook-io/radar/pkg/prom"
)

// Two stores reuse an exporter identity. The local store has overlapping Node
// lifetimes across its replacement, while the other has disjoint hardware.
func TestEngineRightsizingReporterContinuity(t *testing.T) {
	end := time.Now().Truncate(5 * time.Minute).Add(-time.Minute)
	start := end.Add(-2 * time.Hour)
	var ownership, nodes []engineSeries
	for _, cluster := range []string{"rollout", "recycled"} {
		base := func() map[string]string {
			return map[string]string{"job": "ksm", "instance": "ksm:8080", "cluster": cluster}
		}
		pod := base()
		pod["pod"] = "db-0"
		ownership = append(ownership, engineSeries{labels: pod, start: start, end: end, value: 1})
		current := base()
		current["provider_id"] = "provider://live"
		retired := base()
		retired["provider_id"] = "provider://old"
		oldEnd := start.Add(time.Hour + 10*time.Minute)
		if cluster == "recycled" {
			oldEnd = start.Add(time.Hour - 15*time.Minute)
		}
		nodes = append(nodes, engineSeries{labels: retired, start: start, end: oldEnd, value: 1}, engineSeries{labels: current, start: start.Add(time.Hour), end: end, value: 1})
	}
	url := startEnginePrometheus(t, []engineFamily{{name: "kube_pod_info", series: ownership}, {name: "kube_node_info", series: nodes}})
	client := prom.NewClient(prom.NewHTTPTransport(url, "", nil))
	for _, cluster := range []string{"rollout", "recycled"} {
		ok, err := rightsizingReportersContinuous(context.Background(), client, end, 2*time.Hour, `cluster="`+cluster+`"`, []string{"provider://live"})
		if err != nil || ok != (cluster == "rollout") {
			t.Fatalf("cluster=%s continuous=%v err=%v", cluster, ok, err)
		}
	}
}
