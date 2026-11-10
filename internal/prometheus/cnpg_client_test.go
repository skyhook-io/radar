package prometheus

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/skyhook-io/radar/pkg/prom"
)

func TestCNPGReadsKeepCapturedMetricsClient(t *testing.T) {
	var queries atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query().Get("query")
		if query != "up" {
			queries.Add(1)
			if !strings.Contains(query, `cluster="east"`) {
				t.Errorf("query lost captured scope: %s", query)
			}
		}
		if r.URL.Path == "/api/v1/query_range" {
			_, _ = fmt.Fprint(w, `{"status":"success","data":{"resultType":"matrix","result":[]}}`)
			return
		}
		value := "1"
		if strings.Contains(query, "used_bytes") {
			value = "25"
		} else if strings.Contains(query, "capacity_bytes") {
			value = "100"
		}
		_, _ = fmt.Fprintf(w, `{"status":"success","data":{"resultType":"vector","result":[{"metric":{"persistentvolumeclaim":"pg-1"},"value":[1700000000,%q]}]}}`, value)
	}))
	defer upstream.Close()
	client := &Client{
		manualURL: upstream.URL, httpClient: upstream.Client(),
		workloadScope: &prom.WorkloadMetricsScope{ClusterLabels: map[string]string{"cluster": "east"}},
	}
	clientMu.Lock()
	previous := globalClient
	globalClient = &Client{workloadScope: &prom.WorkloadMetricsScope{ClusterLabels: map[string]string{"cluster": "west"}}}
	clientMu.Unlock()
	t.Cleanup(func() { clientMu.Lock(); globalClient = previous; clientMu.Unlock() })

	ctx := context.Background()
	matchers, iso, err := client.ResolveCNPGScope(ctx, "pg", CNPGInstanceSelector("pg", "pg"), nil, time.Minute)
	if err != nil || matchers != `cluster="east"` || iso.Labels["cluster"] != "east" {
		t.Fatalf("exporter scope = %q %+v %v", matchers, iso, err)
	}
	pvcMatchers, _, err := client.ResolvePVCScope(ctx, "pg", []string{"pg-1"}, nil, time.Minute)
	if err != nil || pvcMatchers != matchers {
		t.Fatalf("PVC scope = %q %v", pvcMatchers, err)
	}
	usage := client.QueryPVCUsage(ctx, "pg", []string{"pg-1"}, nil)
	if usage.Status != PVCUsageAvailable || usage.Usage["pg-1"].Ratio != .25 {
		t.Fatalf("PVC usage = %+v", usage)
	}
	rng, _ := ParseCNPGHistoryRange("15m")
	charts, err := client.QueryCNPGHistory(ctx, CNPGHistoryRequest{Namespace: "pg", Cluster: "pg", Range: rng, End: time.Now(), Matchers: matchers, PVCMatchers: pvcMatchers, Claims: []string{"pg-1"}})
	if err != nil || len(charts) == 0 {
		t.Fatalf("history = %d charts, %v", len(charts), err)
	}
	if _, err := client.QueryCNPGFleetLag(ctx, "pg", []string{"pg"}, matchers); err != nil {
		t.Fatal(err)
	}
	if _, err := client.QueryCNPGFleetSlots(ctx, "pg", []string{"pg"}, matchers); err != nil {
		t.Fatal(err)
	}
	if _, err := client.QueryCNPGDiskGrowth(ctx, "pg", []string{"pg-1"}, time.Hour, pvcMatchers); err != nil {
		t.Fatal(err)
	}
	if queries.Load() == 0 {
		t.Fatal("no queries reached captured client")
	}

	client.mu.Lock()
	client.retired = true
	client.mu.Unlock()
	before := queries.Load()
	if usage := client.QueryPVCUsage(ctx, "pg", []string{"pg-1"}, nil); usage.Status != PVCUsageNoPrometheus {
		t.Fatalf("retired client's usage = %+v", usage)
	}
	if queries.Load() != before {
		t.Fatal("retired client continued querying")
	}
}
