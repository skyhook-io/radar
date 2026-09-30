package prometheus

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/skyhook-io/radar/pkg/prom"
)

type fakeCNPGQuerier struct {
	mu      sync.Mutex
	queries []string
	instant func(q string) (*prom.QueryResult, error)
	rng     func(q string) (*prom.QueryResult, error)
}

func (f *fakeCNPGQuerier) Query(_ context.Context, q string) (*prom.QueryResult, error) {
	f.mu.Lock()
	f.queries = append(f.queries, q)
	f.mu.Unlock()
	if f.instant == nil {
		return &prom.QueryResult{}, nil
	}
	return f.instant(q)
}

func (f *fakeCNPGQuerier) QueryRange(_ context.Context, q string, _, _ time.Time, _ time.Duration) (*prom.QueryResult, error) {
	f.mu.Lock()
	f.queries = append(f.queries, q)
	f.mu.Unlock()
	if f.rng == nil {
		return &prom.QueryResult{}, nil
	}
	return f.rng(q)
}

func vec(labels map[string]string, v float64) prom.Series {
	return prom.Series{Labels: labels, DataPoints: []prom.DataPoint{{Timestamp: 1, Value: v}}}
}

func TestCNPGInstanceSelectorAnchorsPodNames(t *testing.T) {
	got := CNPGInstanceSelector("pg", "pg.main")
	want := `namespace="pg",pod=~"^pg\\.main-[0-9]+$"`
	if got != want {
		t.Fatalf("selector = %s, want %s", got, want)
	}
	multi := CNPGInstancesSelector("pg", []string{"b", "a"})
	if !strings.Contains(multi, `pod=~"^(a|b)-[0-9]+$"`) {
		t.Fatalf("multi selector = %s", multi)
	}
	known := map[string]bool{"pg": true, "pg-runtime": true}
	for pod, want := range map[string]string{"pg-1": "pg", "pg-runtime-12": "pg-runtime", "pg-runtime-x": "", "other-1": ""} {
		if got := CNPGClusterOfPod(pod, known); got != want {
			t.Errorf("CNPGClusterOfPod(%s) = %q, want %q", pod, got, want)
		}
	}
}

func TestParseCNPGHistoryRangeBoundsPoints(t *testing.T) {
	for _, name := range []string{"15m", "1h", "6h", "24h", ""} {
		r, ok := ParseCNPGHistoryRange(name)
		if !ok {
			t.Fatalf("range %q rejected", name)
		}
		if points := int(r.Duration / r.Step); points > 150 || points < 50 {
			t.Errorf("range %q has %d points", name, points)
		}
	}
	if _, ok := ParseCNPGHistoryRange("7d"); ok {
		t.Fatal("7d accepted")
	}
}

func TestDecideCNPGScopeRefusesAmbiguousIdentity(t *testing.T) {
	q := &fakeCNPGQuerier{instant: func(string) (*prom.QueryResult, error) {
		return &prom.QueryResult{Series: []prom.Series{vec(nil, 2)}}, nil
	}}
	if _, _, err := decideCNPGScope(context.Background(), q, `namespace="pg"`, nil); !errors.Is(err, ErrCNPGScopeAmbiguous) {
		t.Fatalf("err = %v, want ambiguous", err)
	}
	q.instant = func(string) (*prom.QueryResult, error) {
		return &prom.QueryResult{Series: []prom.Series{vec(nil, 1)}}, nil
	}
	m, iso, err := decideCNPGScope(context.Background(), q, `namespace="pg"`, nil)
	if err != nil || m != "" || iso.Mode != CNPGIsolationUnverified {
		t.Fatalf("single identity: m=%q iso=%+v err=%v", m, iso, err)
	}
}

func TestDecideCNPGScopeVerifiedLabelsMustReachExporterSeries(t *testing.T) {
	q := &fakeCNPGQuerier{instant: func(query string) (*prom.QueryResult, error) {
		if strings.Contains(query, `k8s_cluster_name="east"`) {
			return &prom.QueryResult{}, nil
		}
		return &prom.QueryResult{Series: []prom.Series{vec(nil, 3)}}, nil
	}}
	if _, _, err := decideCNPGScope(context.Background(), q, `namespace="pg"`, map[string]string{"k8s_cluster_name": "east"}); !errors.Is(err, ErrCNPGScopeMismatch) {
		t.Fatalf("err = %v, want mismatch", err)
	}
	q.instant = func(string) (*prom.QueryResult, error) {
		return &prom.QueryResult{Series: []prom.Series{vec(nil, 3)}}, nil
	}
	m, iso, err := decideCNPGScope(context.Background(), q, `namespace="pg"`, map[string]string{"k8s_cluster_name": "east"})
	if err != nil || m != `k8s_cluster_name="east"` || iso.Mode != CNPGIsolationVerified {
		t.Fatalf("verified: m=%q iso=%+v err=%v", m, iso, err)
	}
}

func TestQueryCNPGHistoryStatesPerChart(t *testing.T) {
	r, _ := ParseCNPGHistoryRange("15m")
	q := &fakeCNPGQuerier{
		rng: func(query string) (*prom.QueryResult, error) {
			switch {
			case strings.Contains(query, "cnpg_pg_replication_lag"):
				return &prom.QueryResult{Series: []prom.Series{
					{Labels: map[string]string{"pod": "pg-2", "instance": "x"}, DataPoints: []prom.DataPoint{{Timestamp: 100, Value: 1}, {Timestamp: 115, Value: 2}}},
				}}, nil
			case strings.Contains(query, "cnpg_pg_stat_database_deadlocks"):
				return nil, errors.New("boom")
			}
			return &prom.QueryResult{}, nil
		},
		instant: func(query string) (*prom.QueryResult, error) {
			if strings.Contains(query, "checkpoints") {
				return &prom.QueryResult{Series: []prom.Series{vec(nil, 2)}}, nil
			}
			return &prom.QueryResult{}, nil
		},
	}
	charts := queryCNPGHistory(context.Background(), q, CNPGHistoryRequest{
		Namespace: "pg", Cluster: "pg", Range: r, End: time.Unix(1_700_000_000, 0), Matchers: `cluster_id="a"`,
		PVCDenied: "get persistentvolumeclaims in pg",
	})
	by := map[string]CNPGHistoryChart{}
	for _, c := range charts {
		by[c.ID] = c
	}
	lag := by["replicationLag"]
	if lag.State != CNPGHistoryStateOK || len(lag.Series) != 1 || lag.Series[0].Labels["pod"] != "pg-2" || lag.Series[0].Labels["instance"] != "" || lag.Covered != 2 || lag.Steps != 61 {
		t.Fatalf("lag chart = %+v", lag)
	}
	if by["deadlocks"].State != CNPGHistoryStateError {
		t.Errorf("deadlocks state = %s", by["deadlocks"].State)
	}
	if by["pvcUsed"].State != CNPGHistoryStateDenied || by["pvcUsed"].Grant == "" {
		t.Errorf("pvc chart = %+v", by["pvcUsed"])
	}
	if by["walSize"].State != CNPGHistoryStateNoSeries || !strings.HasPrefix(by["walSize"].Reason, "not scraped") {
		t.Errorf("walSize chart = %+v", by["walSize"])
	}
	if by["checkpoints"].State != CNPGHistoryStateEmpty {
		t.Errorf("checkpoints chart = %+v", by["checkpoints"])
	}
	for _, query := range q.queries {
		if strings.Contains(query, "cnpg_") && strings.Contains(query, "{namespace=") && !strings.Contains(query, `cluster_id="a"`) {
			t.Errorf("query without cluster identity: %s", query)
		}
		if strings.Contains(query, "kubelet_volume_stats") {
			t.Errorf("denied PVC chart still queried: %s", query)
		}
	}
}

func TestQueryCNPGHistoryCapsSeries(t *testing.T) {
	r, _ := ParseCNPGHistoryRange("1h")
	q := &fakeCNPGQuerier{rng: func(query string) (*prom.QueryResult, error) {
		if !strings.Contains(query, "cnpg_pg_database_size_bytes") {
			return &prom.QueryResult{}, nil
		}
		var out []prom.Series
		for i := 0; i < 20; i++ {
			out = append(out, prom.Series{Labels: map[string]string{"datname": string(rune('a' + i))}, DataPoints: []prom.DataPoint{{Timestamp: 1, Value: 1}}})
		}
		return &prom.QueryResult{Series: out}, nil
	}}
	charts := queryCNPGHistory(context.Background(), q, CNPGHistoryRequest{Namespace: "pg", Cluster: "pg", Range: r, End: time.Now(), PodsDenied: ""})
	for _, c := range charts {
		if c.ID == "databaseSize" && (len(c.Series) != cnpgHistoryMaxSeries || c.Omitted != 20-cnpgHistoryMaxSeries) {
			t.Fatalf("databaseSize kept %d, omitted %d", len(c.Series), c.Omitted)
		}
	}
}

func TestQueryCNPGFleetLagSeparatesNoStandbyFromUnscraped(t *testing.T) {
	q := &fakeCNPGQuerier{instant: func(string) (*prom.QueryResult, error) {
		return &prom.QueryResult{Series: []prom.Series{
			vec(map[string]string{"pod": "a-1"}, -1),
			vec(map[string]string{"pod": "a-2"}, 3.5),
			vec(map[string]string{"pod": "a-3"}, 1),
			vec(map[string]string{"pod": "b-1"}, -1),
			vec(map[string]string{"pod": "zzz-1"}, 9),
		}}, nil
	}}
	got, err := queryCNPGFleetLag(context.Background(), q, "pg", []string{"a", "b", "c"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if got.Lag["a"].Seconds != 3.5 || got.Lag["a"].Pod != "a-2" {
		t.Errorf("a lag = %+v", got.Lag["a"])
	}
	if _, ok := got.Lag["b"]; ok || !got.Scraped["b"] {
		t.Errorf("b: lag %v scraped %v", got.Lag["b"], got.Scraped["b"])
	}
	if got.Scraped["c"] || got.Scraped["zzz"] {
		t.Errorf("unexpected scraped: %v", got.Scraped)
	}
}

func TestQueryCNPGFleetLagReportsSustainedLagSeparately(t *testing.T) {
	q := &fakeCNPGQuerier{instant: func(query string) (*prom.QueryResult, error) {
		if strings.Contains(query, "min_over_time") {
			if !strings.Contains(query, "count_over_time") {
				t.Errorf("sustained query does not require samples across the window: %s", query)
			}
			return &prom.QueryResult{Series: []prom.Series{vec(map[string]string{"pod": "a-2"}, 42)}}, nil
		}
		return &prom.QueryResult{Series: []prom.Series{
			vec(map[string]string{"pod": "a-2"}, 60),
			vec(map[string]string{"pod": "b-2"}, 90),
		}}, nil
	}}
	got, err := queryCNPGFleetLag(context.Background(), q, "pg", []string{"a", "b"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if got.Sustained["a"].Seconds != 42 || got.Sustained["a"].Pod != "a-2" {
		t.Errorf("a sustained = %+v", got.Sustained["a"])
	}
	if _, ok := got.Sustained["b"]; ok {
		t.Error("b spiked to 90 s now but has no sustained reading; it must not get one")
	}
}
