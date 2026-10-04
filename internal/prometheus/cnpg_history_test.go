package prometheus

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/auth"
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

func cnpgProbe(window time.Duration) scopeProbe {
	return scopeProbe{metric: "cnpg_collector_up", key: "pod", selectors: []string{`namespace="pg"`}, window: window}
}

func TestDecideScopeRefusesAmbiguousIdentity(t *testing.T) {
	q := &fakeCNPGQuerier{instant: func(string) (*prom.QueryResult, error) {
		return &prom.QueryResult{Series: []prom.Series{vec(nil, 2)}}, nil
	}}
	if _, _, err := decideScope(context.Background(), q, cnpgProbe(0), nil); !errors.Is(err, ErrScopeAmbiguous) {
		t.Fatalf("err = %v, want ambiguous", err)
	}
	q.instant = func(string) (*prom.QueryResult, error) {
		return &prom.QueryResult{Series: []prom.Series{vec(nil, 1)}}, nil
	}
	m, iso, err := decideScope(context.Background(), q, cnpgProbe(0), nil)
	if err != nil || m != "" || iso.Mode != SeriesIsolationUnverified {
		t.Fatalf("single identity: m=%q iso=%+v err=%v", m, iso, err)
	}
}

// An identity that stopped reporting minutes ago still fills a one-hour
// chart, so the check must span the chart's range, not the present instant.
func TestDecideScopeChecksIdentitiesOverTheWholeRange(t *testing.T) {
	q := &fakeCNPGQuerier{instant: func(query string) (*prom.QueryResult, error) {
		v := 1.0
		if strings.Contains(query, "count_over_time(cnpg_collector_up{") && strings.Contains(query, "[1h0m0s]") {
			v = 2
		}
		return &prom.QueryResult{Series: []prom.Series{vec(nil, v)}}, nil
	}}
	if _, _, err := decideScope(context.Background(), q, cnpgProbe(time.Hour), nil); !errors.Is(err, ErrScopeAmbiguous) {
		t.Fatalf("err = %v, want ambiguous over the range; queries %v", err, q.queries)
	}
}

// The CNPG exporter labels its own series cluster=<database cluster>; that
// is not a Kubernetes identity and must never be imposed on other families.
func TestDecideScopeNeverPinsExporterLabels(t *testing.T) {
	q := &fakeCNPGQuerier{instant: func(query string) (*prom.QueryResult, error) {
		if strings.HasPrefix(query, "max(count by (pod)") {
			return &prom.QueryResult{Series: []prom.Series{vec(nil, 1)}}, nil
		}
		return &prom.QueryResult{Series: []prom.Series{vec(map[string]string{"cluster": "pg"}, 1)}}, nil
	}}
	m, _, err := decideScope(context.Background(), q, scopeProbe{metric: "cnpg_collector_up", key: "pod", selectors: []string{CNPGInstanceSelector("db", "pg")}, window: time.Hour}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, def := range cnpgHistoryDefs(withScope(CNPGInstanceSelector("db", "pg"), m), "", 30*time.Second) {
		if def.id == "replicationLag" && strings.Contains(def.queries[0].expr, `cluster="pg"`) {
			t.Fatalf("exporter's cluster label imposed on replication metrics: %s", def.queries[0].expr)
		}
	}
}

// Two database clusters in one namespace carry different exporter cluster
// labels; each Pod still has one identity, so fleet lag is not ambiguous.
func TestDecideScopeTwoDatabasesInOneKubernetesCluster(t *testing.T) {
	q := &fakeCNPGQuerier{instant: func(query string) (*prom.QueryResult, error) {
		if strings.HasPrefix(query, "max(count by (pod)") {
			return &prom.QueryResult{Series: []prom.Series{vec(nil, 1)}}, nil
		}
		return &prom.QueryResult{Series: []prom.Series{vec(map[string]string{"cluster": "pg-a"}, 1), vec(map[string]string{"cluster": "pg-b"}, 1)}}, nil
	}}
	if _, _, err := decideScope(context.Background(), q, scopeProbe{metric: "cnpg_collector_up", key: "pod", selectors: []string{CNPGInstancesSelector("db", []string{"pg-a", "pg-b"})}, window: 10 * time.Minute}, nil); err != nil {
		t.Fatalf("two CNPG clusters in one Kubernetes cluster rejected: %v", err)
	}
}

func TestDecideScopeVerifiedLabelsMustReachProbedSeries(t *testing.T) {
	q := &fakeCNPGQuerier{instant: func(query string) (*prom.QueryResult, error) {
		if strings.Contains(query, `k8s_cluster_name="east"`) {
			return &prom.QueryResult{}, nil
		}
		return &prom.QueryResult{Series: []prom.Series{vec(nil, 3)}}, nil
	}}
	if _, _, err := decideScope(context.Background(), q, cnpgProbe(0), map[string]string{"k8s_cluster_name": "east"}); !errors.Is(err, ErrScopeMismatch) {
		t.Fatalf("err = %v, want mismatch", err)
	}
	q.instant = func(string) (*prom.QueryResult, error) {
		return &prom.QueryResult{Series: []prom.Series{vec(nil, 3)}}, nil
	}
	m, iso, err := decideScope(context.Background(), q, cnpgProbe(0), map[string]string{"k8s_cluster_name": "east"})
	if err != nil || m != `k8s_cluster_name="east"` || iso.Mode != SeriesIsolationVerified {
		t.Fatalf("verified: m=%q iso=%+v err=%v", m, iso, err)
	}
}

func TestQueryCNPGHistoryKeepsPVCScopeAndAmbiguity(t *testing.T) {
	q := &fakeCNPGQuerier{}
	req := CNPGHistoryRequest{Namespace: "pg", Cluster: "pg", Range: cnpgHistoryRanges[1], End: time.Unix(1700000000, 0), Claims: []string{"pg-1"}, PVCMatchers: `cluster="east"`}
	queryCNPGHistory(context.Background(), q, req)
	found := false
	for _, query := range q.queries {
		if strings.Contains(query, "kubelet_volume_stats_used_bytes") {
			found = true
			if !strings.Contains(query, `cluster="east"`) {
				t.Errorf("volume chart query lost the claims' scope: %s", query)
			}
		}
	}
	if !found {
		t.Fatal("no volume chart query ran")
	}
	req.PVCAmbiguous = "two identities"
	for _, c := range queryCNPGHistory(context.Background(), &fakeCNPGQuerier{}, req) {
		if c.ID == "pvcUsed" && (c.State != CNPGHistoryStateAmbiguous || len(c.Series) != 0) {
			t.Errorf("ambiguous claims chart = %+v", c)
		}
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
		PVCDenied: &auth.Grant{Verb: "get", Resource: "persistentvolumeclaims", Namespace: "pg"},
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
	if by["pvcUsed"].State != CNPGHistoryStateDenied || by["pvcUsed"].Grant == nil || *by["pvcUsed"].Grant != (auth.Grant{Verb: "get", Resource: "persistentvolumeclaims", Namespace: "pg"}) {
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
	charts := queryCNPGHistory(context.Background(), q, CNPGHistoryRequest{Namespace: "pg", Cluster: "pg", Range: r, End: time.Now()})
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

// A standby whose WAL receiver is down reads lag 0 (nothing received, nothing
// left to replay), so the receiver is read on its own and never inferred.
func TestQueryCNPGFleetLagReadsWALReceivers(t *testing.T) {
	q := &fakeCNPGQuerier{instant: func(query string) (*prom.QueryResult, error) {
		if strings.Contains(query, "max_over_time(cnpg_pg_replication_is_wal_receiver_up") {
			return &prom.QueryResult{}, nil
		}
		if strings.Contains(query, "cnpg_pg_replication_is_wal_receiver_up") {
			for _, want := range []string{"max by (pod) (cnpg_pg_replication_is_wal_receiver_up{", "(max by (pod) (cnpg_pg_replication_in_recovery{", ") == 1)", `cluster_id="a"`} {
				if !strings.Contains(query, want) {
					t.Errorf("receiver query lacks %q: %s", want, query)
				}
			}
			return &prom.QueryResult{Series: []prom.Series{
				vec(map[string]string{"pod": "a-3"}, 0),
				vec(map[string]string{"pod": "a-2"}, 1),
				vec(map[string]string{"pod": "a-1"}, 0),
				vec(map[string]string{"pod": "b-2"}, -1),
				vec(map[string]string{"pod": "b-3"}, -1),
				vec(map[string]string{"pod": "c-2"}, 1),
				vec(map[string]string{"pod": "c-3"}, -1),
				vec(map[string]string{"pod": "zzz-1"}, 0),
			}}, nil
		}
		return &prom.QueryResult{Series: []prom.Series{
			vec(map[string]string{"pod": "a-1"}, 0),
			vec(map[string]string{"pod": "a-2"}, 0),
			vec(map[string]string{"pod": "a-3"}, 2),
			vec(map[string]string{"pod": "d-1"}, -1),
		}}, nil
	}}
	got, err := queryCNPGFleetLag(context.Background(), q, "pg", []string{"a", "b", "c", "d"}, `cluster_id="a"`)
	if err != nil {
		t.Fatal(err)
	}
	if got.Lag["a"].Seconds != 2 || got.Lag["a"].Pod != "a-3" {
		t.Errorf("lag reading changed: %+v", got.Lag["a"])
	}
	a := got.Receivers["a"]
	if a.Standbys != 3 || a.Receiving != 1 || a.Unknown || strings.Join(a.Down, ",") != "a-1,a-3" {
		t.Errorf("a receivers = %+v, want 3 standbys, 1 receiving, a-1 and a-3 down (sorted)", a)
	}
	if b := got.Receivers["b"]; b.Standbys != 2 || !b.Unknown || b.Receiving != 0 || len(b.Down) != 0 {
		t.Errorf("b receivers = %+v, want unknown: no standby reports the receiver family", b)
	}
	if c := got.Receivers["c"]; c.Standbys != 2 || c.Unknown || c.Receiving != 1 || len(c.Down) != 0 {
		t.Errorf("c receivers = %+v, want one receiving and one unreported, not unknown", c)
	}
	if _, ok := got.Receivers["d"]; ok {
		t.Errorf("d has no standby but got receivers %+v", got.Receivers["d"])
	}
	if _, ok := got.Receivers["zzz"]; ok || got.ReceiversError != "" {
		t.Errorf("receivers = %+v err %q", got.Receivers, got.ReceiversError)
	}

	failing := &fakeCNPGQuerier{instant: func(query string) (*prom.QueryResult, error) {
		if strings.Contains(query, "is_wal_receiver_up") {
			return nil, errors.New("boom")
		}
		return &prom.QueryResult{Series: []prom.Series{vec(map[string]string{"pod": "a-2"}, 1)}}, nil
	}}
	got, err = queryCNPGFleetLag(context.Background(), failing, "pg", []string{"a"}, "")
	if err != nil || got.Lag["a"].Seconds != 1 {
		t.Fatalf("a failed receiver query must not fail the lag: %+v %v", got, err)
	}
	if got.Receivers != nil || !strings.Contains(got.ReceiversError, "boom") {
		t.Errorf("receivers = %+v err %q, want nil with the error", got.Receivers, got.ReceiversError)
	}
}

func slotRow(row string, labels map[string]string, v float64) prom.Series {
	l := map[string]string{cnpgSlotRowLabel: row}
	for k, val := range labels {
		l[k] = val
	}
	return vec(l, v)
}

func TestQueryCNPGFleetSlotsListsInactivePhysicalSlots(t *testing.T) {
	q := &fakeCNPGQuerier{instant: func(query string) (*prom.QueryResult, error) {
		for _, want := range []string{
			`cnpg_pg_replication_slots_active{namespace="pg",pod=~"^(a|b|c|d)-[0-9]+$",cluster_id="x",slot_type="physical"}) == 0)`,
			`cnpg_pg_replication_slots_pg_wal_lsn_diff{namespace="pg",pod=~"^(a|b|c|d)-[0-9]+$",cluster_id="x",slot_type="physical"}`,
			`"radar_row", "retained", "pod", ".*")`,
		} {
			if !strings.Contains(query, want) {
				t.Errorf("slots query lacks %q: %s", want, query)
			}
		}
		return &prom.QueryResult{Series: []prom.Series{
			// a: the primary a-6 keeps an inactive slot for a-1, and a-2 reports
			// its synchronized copy; a-1 itself reports no recovery state.
			slotRow("inactive", map[string]string{"pod": "a-6", "slot_name": "_cnpg_a_1"}, 0),
			slotRow("retained", map[string]string{"pod": "a-6", "slot_name": "_cnpg_a_1"}, 4.9e9),
			slotRow("inactive", map[string]string{"pod": "a-2", "slot_name": "_cnpg_a_1"}, 0),
			slotRow("retained", map[string]string{"pod": "a-2", "slot_name": "_cnpg_a_1"}, 4.8e9),
			slotRow("inactive", map[string]string{"pod": "a-1", "slot_name": "_cnpg_a_2"}, 0),
			slotRow("reported", map[string]string{"pod": "a-6"}, 2),
			slotRow("reported", map[string]string{"pod": "a-2"}, 1),
			slotRow("recovery", map[string]string{"pod": "a-6"}, 0),
			slotRow("recovery", map[string]string{"pod": "a-2"}, 1),
			slotRow("scraped", map[string]string{"pod": "a-6"}, 1),
			// b: slots reported, all active (the query filters active ones out).
			slotRow("reported", map[string]string{"pod": "b-1"}, 1),
			slotRow("scraped", map[string]string{"pod": "b-1"}, 1),
			// c: scraped, no slot family; d: not scraped at all.
			slotRow("scraped", map[string]string{"pod": "c-1"}, 1),
			slotRow("inactive", map[string]string{"pod": "zzz-1", "slot_name": "s"}, 0),
		}}, nil
	}}
	got, err := queryCNPGFleetSlots(context.Background(), q, "pg", []string{"a", "b", "c", "d"}, `cluster_id="x"`)
	if err != nil {
		t.Fatal(err)
	}
	a := got.Clusters["a"]
	if len(a.Inactive) != 3 || a.Omitted != 0 {
		t.Fatalf("a inactive = %+v", a)
	}
	want := []struct {
		pod, slot, role string
		bytes           float64
	}{{"a-6", "_cnpg_a_1", "primary", 4.9e9}, {"a-2", "_cnpg_a_1", "standby", 4.8e9}}
	for i, w := range want {
		s := a.Inactive[i]
		if s.Pod != w.pod || s.Slot != w.slot || s.Role != w.role || s.Bytes == nil || *s.Bytes != w.bytes {
			t.Errorf("a inactive[%d] = %+v, want %+v", i, s, w)
		}
	}
	if s := a.Inactive[2]; s.Pod != "a-1" || s.Role != "" || s.Bytes != nil {
		t.Errorf("a slot without retained bytes or recovery state = %+v, want last with no role and no bytes", s)
	}
	if b, ok := got.Clusters["b"]; !ok || b.Inactive == nil || len(b.Inactive) != 0 {
		t.Errorf("b = %+v (present %v), want read with an empty list", b, ok)
	}
	if _, ok := got.Clusters["c"]; ok || !got.Scraped["c"] {
		t.Errorf("c: clusters %+v scraped %v, want scraped without a slot reading", got.Clusters["c"], got.Scraped["c"])
	}
	if _, ok := got.Clusters["d"]; ok || got.Scraped["d"] {
		t.Errorf("d must be neither read nor scraped")
	}
	if _, ok := got.Clusters["zzz"]; ok {
		t.Error("a Pod of an unrequested cluster was attributed")
	}
}

func TestQueryCNPGFleetSlotsCapsBySize(t *testing.T) {
	q := &fakeCNPGQuerier{instant: func(string) (*prom.QueryResult, error) {
		var out []prom.Series
		for i := 1; i <= 13; i++ {
			pod := "a-" + strconv.Itoa(i)
			out = append(out,
				slotRow("inactive", map[string]string{"pod": pod, "slot_name": "_cnpg_a_99"}, 0),
				slotRow("retained", map[string]string{"pod": pod, "slot_name": "_cnpg_a_99"}, float64(i)),
			)
		}
		return &prom.QueryResult{Series: append(out, slotRow("reported", map[string]string{"pod": "a-1"}, 1))}, nil
	}}
	got, err := queryCNPGFleetSlots(context.Background(), q, "pg", []string{"a"}, "")
	if err != nil {
		t.Fatal(err)
	}
	a := got.Clusters["a"]
	if len(a.Inactive) != CNPGFleetSlotCap || a.Omitted != 13-CNPGFleetSlotCap {
		t.Fatalf("kept %d, omitted %d", len(a.Inactive), a.Omitted)
	}
	if *a.Inactive[0].Bytes != 13 || *a.Inactive[CNPGFleetSlotCap-1].Bytes != 4 {
		t.Errorf("not the largest first: first %v last %v", *a.Inactive[0].Bytes, *a.Inactive[CNPGFleetSlotCap-1].Bytes)
	}
}

func TestQueryCNPGFleetLagReportsSustainedLagSeparately(t *testing.T) {
	q := &fakeCNPGQuerier{instant: func(query string) (*prom.QueryResult, error) {
		if strings.Contains(query, "cnpg_pg_replication_is_wal_receiver_up") {
			return &prom.QueryResult{}, nil
		}
		if strings.Contains(query, "min_over_time") {
			// Every raw sample in the window, and a series that already
			// existed when the window began: a sample count cannot prove age.
			for _, want := range []string{"min by (pod) (min_over_time(cnpg_pg_replication_lag{", "[10m])", "} offset 10m"} {
				if !strings.Contains(query, want) {
					t.Errorf("sustained query lacks %q: %s", want, query)
				}
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

func TestQueryCNPGFleetLagSustainedReceiverLossNeedsAStandbyThroughout(t *testing.T) {
	q := &fakeCNPGQuerier{instant: func(query string) (*prom.QueryResult, error) {
		if !strings.Contains(query, "max_over_time(cnpg_pg_replication_is_wal_receiver_up") {
			return &prom.QueryResult{}, nil
		}
		// Down in every sample, a standby in every sample (a primary reports no
		// receiver, so a former primary must not qualify on its primary
		// samples), and reporting since before the window.
		for _, want := range []string{
			"max by (pod) (max_over_time(cnpg_pg_replication_is_wal_receiver_up{",
			"[5m])) == 0)",
			"min by (pod) (min_over_time(cnpg_pg_replication_in_recovery{",
			"[5m])) == 1)",
			"} offset 5m))",
		} {
			if !strings.Contains(query, want) {
				t.Errorf("sustained receiver query lacks %q: %s", want, query)
			}
		}
		return &prom.QueryResult{Series: []prom.Series{vec(map[string]string{"pod": "a-3"}, 0), vec(map[string]string{"pod": "a-2"}, 0), vec(map[string]string{"pod": "zzz-1"}, 0)}}, nil
	}}
	got, err := queryCNPGFleetLag(context.Background(), q, "pg", []string{"a", "b"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got.ReceiversDownSustained["a"], ",") != "a-2,a-3" {
		t.Errorf("a sustained down = %v, want a-2,a-3 (sorted)", got.ReceiversDownSustained["a"])
	}
	if _, ok := got.ReceiversDownSustained["b"]; ok {
		t.Error("b has no sustained receiver loss")
	}
}
