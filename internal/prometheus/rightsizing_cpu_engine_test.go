//go:build promengine

package prometheus

import (
	"context"
	"math"
	"testing"
	"time"
)

func TestEngineRightsizingUsesWholeContainerCPU(t *testing.T) {
	end := time.Now().UTC().Truncate(rightsizingStep)
	start := end.Add(-time.Hour)
	cpuSeries := func(container, cpu string, rate float64) engineSeries {
		labels := map[string]string{"namespace": "shop", "pod": "db-0", "container": container}
		if cpu != "" {
			labels["cpu"] = cpu
		}
		return engineSeries{labels: labels, rate: rate, start: start, end: end}
	}
	cpu := engineFamily{name: metricCPUUsage, counter: true, series: []engineSeries{
		cpuSeries("shards", "cpu00", .2), cpuSeries("shards", "cpu01", .3),
		cpuSeries("mixed", "cpu00", .2), cpuSeries("mixed", "cpu01", .3), cpuSeries("mixed", "total", .5),
		cpuSeries("unlabeled", "", .75),
	}}
	owner := engineFamily{name: metricPodOwner, series: []engineSeries{{
		labels: map[string]string{"namespace": "shop", "pod": "db-0", "owner_kind": "StatefulSet", "owner_name": "db", "owner_is_controller": "true"},
		value:  1, start: start, end: end,
	}}}
	c := engineClient(startEnginePrometheus(t, []engineFamily{cpu, owner}), nil)
	workload := scanTestWorkload("StatefulSet", "shop", "db", "shards")
	workload.workload.containers = append(workload.workload.containers, containerSpec{name: "mixed"}, containerSpec{name: "unlabeled"})
	batch := []scanWorkload{workload}
	selection := metricSelection{join: "* on (namespace,pod) group_left() (" + ownerSelection("StatefulSet", "shop", "db", ViewScope{}) + ")"}
	for label, query := range map[string]string{
		"detail":                buildRightsizingQueries("shop", selection)["cpu_stat"],
		"fleet":                 buildRightsizingScanQueries(batch, ViewScope{})["cpu"],
		"fleet memory recovery": buildScanQueries(batch, ViewScope{}, true)["cpu"],
	} {
		t.Run(label, func(t *testing.T) {
			r, err := requireComplete(c.QueryAt(context.Background(), query, end))
			if err != nil {
				t.Fatal(err)
			}
			values := resultByContainer(r)
			if len(values) != 2 || math.Abs(values["mixed"]-.5) > 1e-9 || math.Abs(values["unlabeled"]-.75) > 1e-9 {
				t.Errorf("whole-container CPU = %v, want mixed=.5, unlabeled=.75 and no shards", values)
			}
		})
	}
}
