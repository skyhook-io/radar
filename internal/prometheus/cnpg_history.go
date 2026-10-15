package prometheus

import (
	"context"
	"errors"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/pkg/prom"
)

// CNPG history reads only server-built PromQL over the CloudNativePG exporter
// and kubelet volume stats. A Cluster is selected by namespace plus its
// instance Pod names, `<cluster>-<n>`, never by a `cluster` label: shared
// Prometheus setups use `cluster` for the Kubernetes cluster, while a CNPG
// scrape config may relabel it to the CNPG Cluster name, so its meaning is not
// knowable from the series. The pod pattern keeps deleted instances in range.

const (
	CNPGHistoryStateOK        = "ok"
	CNPGHistoryStateNoSeries  = "noSeries"
	CNPGHistoryStateEmpty     = "empty"
	CNPGHistoryStateDenied    = "denied"
	CNPGHistoryStateError     = "error"
	CNPGHistoryStateNotRead   = "notRead"
	CNPGHistoryStateAmbiguous = "ambiguous"
	CNPGMetricLookback        = 5 * time.Minute

	cnpgHistoryMaxSeries   = 12
	cnpgHistoryConcurrency = 4
)

// CNPGHistoryRange is one of the ranges the history endpoint accepts. Steps
// keep every chart at 60–144 points.
type CNPGHistoryRange struct {
	Name     string
	Duration time.Duration
	Step     time.Duration
}

var cnpgHistoryRanges = []CNPGHistoryRange{
	{"15m", 15 * time.Minute, 15 * time.Second},
	{"1h", time.Hour, 30 * time.Second},
	{"6h", 6 * time.Hour, 3 * time.Minute},
	{"24h", 24 * time.Hour, 10 * time.Minute},
}

// ParseCNPGHistoryRange accepts 15m, 1h, 6h or 24h; empty means 1h.
func ParseCNPGHistoryRange(raw string) (CNPGHistoryRange, bool) {
	if raw == "" {
		raw = "1h"
	}
	for _, r := range cnpgHistoryRanges {
		if r.Name == raw {
			return r, true
		}
	}
	return CNPGHistoryRange{}, false
}

// CNPGInstanceSelector is the matcher set for a Cluster's instance series.
func CNPGInstanceSelector(namespace, cluster string) string {
	return CNPGInstancesSelector(namespace, []string{cluster})
}

// CNPGInstancesSelector selects the instance Pods of several Clusters of one
// namespace in one matcher.
func CNPGInstancesSelector(namespace string, clusters []string) string {
	names := make([]string, len(clusters))
	for i, c := range clusters {
		names[i] = regexp.QuoteMeta(c)
	}
	sort.Strings(names)
	alt := strings.Join(names, "|")
	if len(names) > 1 {
		alt = "(" + alt + ")"
	}
	return "namespace=" + strconv.Quote(namespace) + ",pod=~" + strconv.Quote("^"+alt+"-[0-9]+$")
}

var cnpgInstancePod = regexp.MustCompile(`^(.+)-[0-9]+$`)

// CNPGClusterOfPod returns the Cluster an instance Pod name belongs to among
// the given names, or "" when none matches exactly.
func CNPGClusterOfPod(pod string, clusters map[string]bool) string {
	m := cnpgInstancePod.FindStringSubmatch(pod)
	if m == nil || !clusters[m[1]] {
		return ""
	}
	return m[1]
}

// ResolveCNPGScope decides the cluster-identity matchers for one namespace's
// CNPG exporter series over window (0 for an instant read).
func (client *Client) ResolveCNPGScope(ctx context.Context, namespace, selector string, anchors []prom.WorkloadPodIdentity, window time.Duration) (string, SeriesIsolation, error) {
	return client.resolveScope(ctx, namespace, scopeProbe{metric: "cnpg_collector_up", key: "pod", selectors: []string{selector}, window: window}, anchors, nil)
}

// CNPGHistoryThreshold is a reference line on a chart.
type CNPGHistoryThreshold struct {
	Value float64 `json:"value"`
	Label string  `json:"label"`
}

// CNPGHistoryChart is one chart: its series, or why there are none.
// Coverage counts evaluation steps with at least one sample.
type CNPGHistoryChart struct {
	ID         string                 `json:"id"`
	Title      string                 `json:"title"`
	Unit       string                 `json:"unit"`
	Source     string                 `json:"source"`
	SeriesBy   string                 `json:"seriesBy"`
	State      string                 `json:"state"`
	Reason     string                 `json:"reason,omitempty"`
	Grant      *auth.Grant            `json:"grant,omitempty"`
	Thresholds []CNPGHistoryThreshold `json:"thresholds,omitempty"`
	Series     []prom.Series          `json:"series"`
	Omitted    int                    `json:"omitted,omitempty"`
	Steps      int                    `json:"steps"`
	Covered    int                    `json:"covered"`
}

type cnpgHistoryQuery struct {
	label string
	expr  string
}

type cnpgHistoryDef struct {
	id, title, unit, source, seriesBy string
	thresholds                        []CNPGHistoryThreshold
	queries                           []cnpgHistoryQuery
	pvc                               bool
	// presence selects the family the chart derives from; when the chart is
	// empty but this has samples, the family is scraped and emptyReason holds.
	presence    string
	emptyReason string
}

func cnpgRateWindow(step time.Duration) string {
	return max(2*time.Minute, 2*step).Round(time.Second).String()
}

// cnpgHistoryDefs builds the chart queries. `max by` before any sum collapses
// duplicate scrapes of one Pod (HA Prometheus pairs, two scrape jobs). sel is
// the instance selector with any cluster-identity matchers; pvcSel selects the
// Cluster's claims and is empty when they are not readable.
func cnpgHistoryDefs(sel, pvcSel string, step time.Duration) []cnpgHistoryDef {
	w := cnpgRateWindow(step)
	clients := sel + `,usename!="streaming_replica",application_name!="cnpg_metrics_exporter"`
	perPodRate := func(metric string) string {
		return "sum by (pod) (max by (pod,datname) (rate(" + metric + "{" + sel + "}[" + w + "])))"
	}
	clusterRate := func(metric, by string) string {
		return "sum(max by (" + by + ") (rate(" + metric + "{" + sel + "}[" + w + "])))"
	}
	up := "(0 * max(cnpg_collector_up{" + sel + "} == 1))"
	family := func(name string) string { return "{__name__=~" + strconv.Quote(name) + "," + sel + "}" }
	checkpoints := func(kind string) string {
		return "(" + clusterRate("cnpg_pg_stat_checkpointer_checkpoints_"+kind, "pod") + " or " + clusterRate("cnpg_pg_stat_bgwriter_checkpoints_"+kind, "pod") + ") * 60"
	}
	defs := []cnpgHistoryDef{
		{
			id: "replicationLag", title: "Replay lag per standby", unit: "seconds", seriesBy: "pod",
			source:      "cnpg_pg_replication_lag, while cnpg_pg_replication_in_recovery = 1",
			thresholds:  []CNPGHistoryThreshold{{Value: 5, Label: "5 s"}, {Value: 30, Label: "30 s"}},
			queries:     []cnpgHistoryQuery{{expr: "max by (pod) (cnpg_pg_replication_lag{" + sel + "}) and on (pod) (max by (pod) (cnpg_pg_replication_in_recovery{" + sel + "}) == 1)"}},
			presence:    family("cnpg_pg_replication_lag"),
			emptyReason: "no instance was a standby in this range",
		},
		{
			id: "sessions", title: "Client sessions by state (all instances)", unit: "count", seriesBy: "state",
			source: "cnpg_backends_total, excluding streaming_replica and the metrics exporter",
			queries: []cnpgHistoryQuery{
				{expr: "sum by (state) (max by (pod,state,datname,usename,application_name) (cnpg_backends_total{" + clients + "}))"},
				{label: "all states", expr: "sum(max by (pod,state,datname,usename,application_name) (cnpg_backends_total{" + clients + "})) or " + up},
			},
		},
		{
			id: "waiting", title: "Sessions waiting on locks", unit: "count", seriesBy: "pod",
			source:  "cnpg_backends_waiting_total",
			queries: []cnpgHistoryQuery{{expr: "max by (pod) (cnpg_backends_waiting_total{" + sel + "})"}},
		},
		{
			id: "tps", title: "Transactions per second", unit: "per second", seriesBy: "series",
			source: "rate of cnpg_pg_stat_database_xact_commit / xact_rollback, all instances and databases",
			queries: []cnpgHistoryQuery{
				{label: "commits", expr: clusterRate("cnpg_pg_stat_database_xact_commit", "pod,datname")},
				{label: "rollbacks", expr: clusterRate("cnpg_pg_stat_database_xact_rollback", "pod,datname")},
			},
		},
		{
			id: "walArchive", title: "WAL archiving per minute", unit: "per minute", seriesBy: "series",
			source: "rate of cnpg_pg_stat_archiver_archived_count / failed_count",
			queries: []cnpgHistoryQuery{
				{label: "archived", expr: clusterRate("cnpg_pg_stat_archiver_archived_count", "pod") + " * 60"},
				{label: "failed", expr: clusterRate("cnpg_pg_stat_archiver_failed_count", "pod") + " * 60"},
			},
		},
		{
			id: "walSize", title: "WAL on disk", unit: "bytes", seriesBy: "pod",
			source:  `cnpg_collector_pg_wal{value="size"} (bytes of WAL segments, not filesystem use)`,
			queries: []cnpgHistoryQuery{{expr: "max by (pod) (cnpg_collector_pg_wal{" + sel + `,value="size"})`}},
		},
		{
			id: "pvcUsed", title: "Volume used", unit: "percent", seriesBy: "persistentvolumeclaim", pvc: true,
			source:     "kubelet_volume_stats_used_bytes / kubelet_volume_stats_capacity_bytes",
			thresholds: []CNPGHistoryThreshold{{Value: 80, Label: "80%"}, {Value: 90, Label: "90%"}},
			queries:    []cnpgHistoryQuery{{expr: "100 * max by (persistentvolumeclaim) (kubelet_volume_stats_used_bytes{" + pvcSel + "}) / max by (persistentvolumeclaim) (kubelet_volume_stats_capacity_bytes{" + pvcSel + "})"}},
		},
		{
			id: "databaseSize", title: "Database size", unit: "bytes", seriesBy: "datname",
			source:  "cnpg_pg_database_size_bytes (largest reported by any instance)",
			queries: []cnpgHistoryQuery{{expr: "max by (datname) (cnpg_pg_database_size_bytes{" + sel + `,datname!~"template0|template1"})`}},
		},
		{
			id: "tempBytes", title: "Temporary file writes", unit: "bytes/s", seriesBy: "pod",
			source:  "rate of cnpg_pg_stat_database_temp_bytes",
			queries: []cnpgHistoryQuery{{expr: perPodRate("cnpg_pg_stat_database_temp_bytes")}},
		},
		{
			id: "deadlocks", title: "Deadlocks per minute", unit: "per minute", seriesBy: "pod",
			source:  "rate of cnpg_pg_stat_database_deadlocks",
			queries: []cnpgHistoryQuery{{expr: perPodRate("cnpg_pg_stat_database_deadlocks") + " * 60"}},
		},
		{
			id: "checkpoints", title: "Checkpoints per minute", unit: "per minute", seriesBy: "series",
			source: "rate of cnpg_pg_stat_checkpointer_checkpoints_{timed,req} (PostgreSQL 17+) or cnpg_pg_stat_bgwriter_checkpoints_{timed,req}",
			queries: []cnpgHistoryQuery{
				{label: "requested", expr: checkpoints("req")},
				{label: "timed", expr: checkpoints("timed")},
			},
			presence:    family("cnpg_pg_stat_(checkpointer|bgwriter)_checkpoints_(req|timed)"),
			emptyReason: "fewer than two samples in each rate window",
		},
	}
	return defs
}

// Bounds excludes reused Pod names from a previous Cluster incarnation. The
// lookback margin also excludes predecessor samples from rates and instant
// selectors (Prometheus' default five-minute lookback).
func (r CNPGHistoryRange) Bounds(end, createdAt time.Time) (time.Time, time.Time) {
	end = end.Truncate(r.Step)
	start := end.Add(-r.Duration)
	if !createdAt.IsZero() {
		earliest := createdAt.Add(max(CNPGMetricLookback, 2*r.Step)).UTC()
		aligned := earliest.Truncate(r.Step)
		if aligned.Before(earliest) {
			aligned = aligned.Add(r.Step)
		}
		if aligned.After(start) {
			start = aligned
		}
	}
	return start, end
}

// CNPGHistoryRequest is what the server decided the caller may read.
type CNPGHistoryRequest struct {
	Namespace string
	Cluster   string
	Range     CNPGHistoryRange
	End       time.Time
	CreatedAt time.Time
	// Matchers are cluster-identity matchers from ResolveCNPGScope for the
	// exporter series; PVCMatchers from ResolvePVCScope for the claims.
	Matchers      string
	PVCMatchers   string
	PVCScopeState string
	// PodsDenied / PVCDenied carry the grant that is missing, empty when allowed.
	PodsDenied *auth.Grant
	PVCDenied  *auth.Grant
	// PVCReason explains an empty Claims when the claims were not denied.
	PVCReason string
	Claims    []string
}

// QueryCNPGHistory runs every chart query, a few at a time.
func (client *Client) QueryCNPGHistory(ctx context.Context, req CNPGHistoryRequest) ([]CNPGHistoryChart, error) {
	if client == nil {
		return nil, errors.New("Prometheus client not initialized")
	}
	return queryCNPGHistory(ctx, client, req), nil
}

func queryCNPGHistory(ctx context.Context, q seriesQuerier, req CNPGHistoryRequest) []CNPGHistoryChart {
	sel := withScope(CNPGInstanceSelector(req.Namespace, req.Cluster), req.Matchers)
	pvcSel := ""
	if len(req.Claims) > 0 {
		pvcSel = withScope(cnpgClaimSelector(req.Namespace, req.Claims), req.PVCMatchers)
	}
	start, end := req.Range.Bounds(req.End, req.CreatedAt)
	steps := max(0, int(end.Sub(start)/req.Range.Step)+1)

	defs := cnpgHistoryDefs(sel, pvcSel, req.Range.Step)
	charts := make([]CNPGHistoryChart, len(defs))
	sem := make(chan struct{}, cnpgHistoryConcurrency)
	var wg sync.WaitGroup
	for i, d := range defs {
		c := &charts[i]
		*c = CNPGHistoryChart{ID: d.id, Title: d.title, Unit: d.unit, Source: d.source, SeriesBy: d.seriesBy, Thresholds: d.thresholds, Series: []prom.Series{}, Steps: steps}
		switch {
		case d.pvc && req.PVCDenied != nil:
			c.State, c.Grant = CNPGHistoryStateDenied, req.PVCDenied
			continue
		case !d.pvc && req.PodsDenied != nil:
			c.State, c.Grant = CNPGHistoryStateDenied, req.PodsDenied
			continue
		case d.pvc && req.PVCScopeState != "":
			c.State, c.Reason = req.PVCScopeState, req.PVCReason
			continue
		case d.pvc && pvcSel == "":
			c.State, c.Reason = CNPGHistoryStateNotRead, req.PVCReason
			if c.Reason == "" {
				c.Reason = "no volumes owned by this cluster to chart"
			}
			continue
		}
		if start.After(end) {
			c.State, c.Reason = CNPGHistoryStateEmpty, "Waiting for samples after this Cluster was created"
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				c.State, c.Reason = CNPGHistoryStateError, ctx.Err().Error()
				return
			}
			defer func() { <-sem }()
			runCNPGHistoryChart(ctx, q, d, start, end, req.Range.Step, c)
		}()
	}
	wg.Wait()
	return charts
}

func runCNPGHistoryChart(ctx context.Context, q seriesQuerier, d cnpgHistoryDef, start, end time.Time, step time.Duration, c *CNPGHistoryChart) {
	series := []prom.Series{}
	for _, query := range d.queries {
		res, err := q.QueryRange(ctx, query.expr, start, end, step)
		if err != nil {
			c.State, c.Reason = CNPGHistoryStateError, "Prometheus query failed: "+truncate(err.Error(), 200)
			c.Series = []prom.Series{}
			return
		}
		for _, s := range res.Series {
			labels := map[string]string{}
			if query.label != "" {
				labels[d.seriesBy] = query.label
			} else if v, ok := s.Labels[d.seriesBy]; ok {
				labels[d.seriesBy] = v
			}
			series = append(series, prom.Series{Labels: labels, DataPoints: s.DataPoints})
		}
	}
	sort.SliceStable(series, func(i, j int) bool { return series[i].Labels[d.seriesBy] < series[j].Labels[d.seriesBy] })
	if len(series) > cnpgHistoryMaxSeries {
		c.Omitted = len(series) - cnpgHistoryMaxSeries
		series = series[:cnpgHistoryMaxSeries]
	}
	covered := map[int64]bool{}
	for _, s := range series {
		for _, p := range s.DataPoints {
			if !math.IsNaN(p.Value) && !math.IsInf(p.Value, 0) {
				covered[p.Timestamp] = true
			}
		}
	}
	c.Series, c.Covered = series, len(covered)
	if len(series) == 0 && d.presence != "" && end.After(start) {
		res, err := q.Query(ctx, "count(last_over_time("+d.presence+"["+end.Sub(start).String()+"]))")
		if err == nil && len(res.Series) > 0 {
			c.State, c.Reason = CNPGHistoryStateEmpty, d.emptyReason
			return
		}
	}
	if len(series) == 0 {
		c.State, c.Reason = CNPGHistoryStateNoSeries, "not scraped: Prometheus has no series for this cluster in this range ("+d.source+")"
		if d.pvc {
			c.Reason = "not scraped: Prometheus has no kubelet volume stats for this cluster's claims in this range (some volume drivers, such as hostPath, report none)"
		}
		return
	}
	c.State = CNPGHistoryStateOK
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// CNPGLagReading is a Cluster's largest current standby replay lag.
// Reporting counts the standbys whose lag was read, so a lag that covers only
// some of them is not mistaken for all of them.
type CNPGLagReading struct {
	Seconds   float64
	Pod       string
	Reporting int
}

// CNPGFleetLag holds, per Cluster, the largest standby replay lag, and which
// Clusters have exporter series at all, so "no standby reporting" is told
// apart from "not scraped".
type CNPGFleetLag struct {
	Lag     map[string]CNPGLagReading
	Scraped map[string]bool
	// Sustained is each Cluster's worst standby's lowest recorded lag over
	// CNPGSustainedLagWindow, for standbys already reporting when the window
	// began; absent otherwise.
	Sustained map[string]CNPGLagReading
	// Receivers is keyed by Cluster, for Clusters with an instance in
	// recovery. Nil, with ReceiversError set, when the query failed.
	Receivers      map[string]CNPGReceiverReading
	ReceiversError string
	// ReceiversDownSustained is keyed by Cluster: standbys whose WAL receiver
	// was down in every sample over CNPGReceiverDownWindow. Nil when that
	// best-effort query failed.
	ReceiversDownSustained map[string][]string
}

// CNPGReceiverDownWindow is how long a standby's WAL receiver must stay down
// before it is a problem: a restarting standby is briefly down on every
// rolling update and reconnects on its own.
const CNPGReceiverDownWindow = 5 * time.Minute

// CNPGReceiverReading is a Cluster's standbys (instances reporting
// cnpg_pg_replication_in_recovery = 1) and their WAL receivers. Unknown is
// set, and Receiving and Down are empty, when no standby reports
// cnpg_pg_replication_is_wal_receiver_up. A standby that alone lacks the
// family is counted in Standbys only.
type CNPGReceiverReading struct {
	Standbys  int
	Receiving int
	Down      []string
	Unknown   bool
}

// CNPGSustainedLagWindow is how long a standby's replay lag must stay high
// before the fleet treats it as a problem rather than a transient spike.
const CNPGSustainedLagWindow = 10 * time.Minute

// QueryCNPGFleetLag reads the current replay lag of every standby of the
// named Clusters in one namespace with one instant query.
func (client *Client) QueryCNPGFleetLag(ctx context.Context, namespace string, clusters []string, matchers string) (CNPGFleetLag, error) {
	if client == nil {
		return CNPGFleetLag{}, errors.New("Prometheus client not initialized")
	}
	return queryCNPGFleetLag(ctx, client, namespace, clusters, matchers)
}

// A Pod scraped but not in recovery answers -1 through the `or` branch, so one
// query carries both lag and presence.
func queryCNPGFleetLag(ctx context.Context, q seriesQuerier, namespace string, clusters []string, matchers string) (CNPGFleetLag, error) {
	sel := withScope(CNPGInstancesSelector(namespace, clusters), matchers)
	query := "(max by (pod) (cnpg_pg_replication_lag{" + sel + "}) and on (pod) (max by (pod) (cnpg_pg_replication_in_recovery{" + sel + "}) == 1)) or (-1 * count by (pod) (cnpg_collector_up{" + sel + "}))"
	res, err := q.Query(ctx, query)
	if err != nil {
		return CNPGFleetLag{}, err
	}
	known := map[string]bool{}
	for _, c := range clusters {
		known[c] = true
	}
	out := CNPGFleetLag{Lag: map[string]CNPGLagReading{}, Scraped: map[string]bool{}}
	for _, s := range res.Series {
		if len(s.DataPoints) == 0 {
			continue
		}
		pod := s.Labels["pod"]
		cluster := CNPGClusterOfPod(pod, known)
		if cluster == "" {
			continue
		}
		out.Scraped[cluster] = true
		v := s.DataPoints[0].Value
		if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
			continue
		}
		cur, ok := out.Lag[cluster]
		reporting := cur.Reporting + 1
		if !ok || v > cur.Seconds || (v == cur.Seconds && pod < cur.Pod) {
			cur = CNPGLagReading{Seconds: v, Pod: pod}
		}
		cur.Reporting = reporting
		out.Lag[cluster] = cur
	}
	out.Sustained = querySustainedCNPGLag(ctx, q, sel, known)
	out.Receivers, out.ReceiversError = queryCNPGReceivers(ctx, q, sel, known)
	out.ReceiversDownSustained = querySustainedReceiverDown(ctx, q, sel, known)
	return out, nil
}

// querySustainedReceiverDown is best effort like the sustained lag: every raw
// receiver sample in the window was 0, the instance was a standby in every
// sample of the window (a primary reports no receiver, so a former primary
// just after a switchover must not qualify on its primary samples), and the
// series already existed when the window began.
func querySustainedReceiverDown(ctx context.Context, q seriesQuerier, sel string, known map[string]bool) map[string][]string {
	window := fmt.Sprintf("%dm", int(CNPGReceiverDownWindow.Minutes()))
	query := "(max by (pod) (max_over_time(cnpg_pg_replication_is_wal_receiver_up{" + sel + "}[" + window + "])) == 0)" +
		" and on (pod) (min by (pod) (min_over_time(cnpg_pg_replication_in_recovery{" + sel + "}[" + window + "])) == 1)" +
		" and on (pod) (max by (pod) (cnpg_pg_replication_is_wal_receiver_up{" + sel + "} offset " + window + "))"
	res, err := q.Query(ctx, query)
	if err != nil {
		return nil
	}
	out := map[string][]string{}
	for _, s := range res.Series {
		if len(s.DataPoints) == 0 {
			continue
		}
		pod := s.Labels["pod"]
		if cluster := CNPGClusterOfPod(pod, known); cluster != "" {
			out[cluster] = append(out[cluster], pod)
		}
	}
	for c := range out {
		sort.Strings(out[c])
	}
	return out
}

// queryCNPGReceivers reads whether each standby's WAL receiver is up. Lag
// cannot answer that: cnpg_pg_replication_lag is 0 whenever a standby's
// receive and replay positions match, which is also what a standby that
// receives nothing reports. A standby without the receiver family answers -1
// through the `or` branch.
func queryCNPGReceivers(ctx context.Context, q seriesQuerier, sel string, known map[string]bool) (map[string]CNPGReceiverReading, string) {
	standby := "(max by (pod) (cnpg_pg_replication_in_recovery{" + sel + "}) == 1)"
	query := "(max by (pod) (cnpg_pg_replication_is_wal_receiver_up{" + sel + "}) and on (pod) " + standby + ") or (-1 * " + standby + ")"
	res, err := q.Query(ctx, query)
	if err != nil {
		return nil, "Prometheus query failed: " + truncate(err.Error(), 200)
	}
	return parseCNPGReceivers(res, known), ""
}

func parseCNPGReceivers(res *prom.QueryResult, known map[string]bool) map[string]CNPGReceiverReading {
	out := map[string]CNPGReceiverReading{}
	unreported := map[string]int{}
	for _, s := range res.Series {
		if len(s.DataPoints) == 0 {
			continue
		}
		pod := s.Labels["pod"]
		cluster := CNPGClusterOfPod(pod, known)
		if cluster == "" {
			continue
		}
		r := out[cluster]
		r.Standbys++
		switch v := s.DataPoints[0].Value; {
		case v > 0:
			r.Receiving++
		case v == 0:
			r.Down = append(r.Down, pod)
		default:
			unreported[cluster]++
		}
		out[cluster] = r
	}
	for cluster, r := range out {
		sort.Strings(r.Down)
		r.Unknown = unreported[cluster] == r.Standbys
		out[cluster] = r
	}
	return out
}

// CNPGFleetSlotCap bounds the inactive slots kept per Cluster.
const CNPGFleetSlotCap = 10

const cnpgSlotRowLabel = "radar_row"

// CNPGSlotReading is one inactive physical replication slot as one instance
// reports it. Role is that instance's: "primary", "standby" (CloudNativePG
// copies HA slots to standbys, where no WAL sender uses them), or "" when it
// reports no recovery state. Bytes is the WAL the slot retains on that
// instance, nil when not reported.
type CNPGSlotReading struct {
	Slot  string
	Pod   string
	Role  string
	Bytes *float64
}

// CNPGClusterSlots is one Cluster's inactive physical slots, most retained
// WAL first, at most CNPGFleetSlotCap; Omitted counts the rest.
type CNPGClusterSlots struct {
	Inactive []CNPGSlotReading
	Omitted  int
}

// CNPGFleetSlots holds the Clusters whose instances report the slot family
// at all, and the Clusters with any exporter series, so "no slot reported"
// is told apart from "not scraped".
type CNPGFleetSlots struct {
	Clusters map[string]CNPGClusterSlots
	Scraped  map[string]bool
}

// QueryCNPGFleetSlots reads the inactive physical replication slots of the
// named Clusters in one namespace with one instant query.
func (client *Client) QueryCNPGFleetSlots(ctx context.Context, namespace string, clusters []string, matchers string) (CNPGFleetSlots, error) {
	if client == nil {
		return CNPGFleetSlots{}, errors.New("Prometheus client not initialized")
	}
	return queryCNPGFleetSlots(ctx, client, namespace, clusters, matchers)
}

// Each branch is tagged with its own radar_row value so `or` keeps all of
// them: inactive slots, the WAL those retain, which instances report the slot
// family at all, each instance's recovery state (its role), and which
// instances are scraped.
func queryCNPGFleetSlots(ctx context.Context, q seriesQuerier, namespace string, clusters []string, matchers string) (CNPGFleetSlots, error) {
	sel := withScope(CNPGInstancesSelector(namespace, clusters), matchers)
	physical := sel + `,slot_type="physical"`
	inactive := "(max by (pod, slot_name) (cnpg_pg_replication_slots_active{" + physical + "}) == 0)"
	tag := func(expr, row string) string {
		return "label_replace(" + expr + `, "` + cnpgSlotRowLabel + `", "` + row + `", "pod", ".*")`
	}
	query := strings.Join([]string{
		tag(inactive, "inactive"),
		tag("max by (pod, slot_name) (cnpg_pg_replication_slots_pg_wal_lsn_diff{"+physical+"}) and on (pod, slot_name) "+inactive, "retained"),
		tag("count by (pod) (cnpg_pg_replication_slots_active{"+sel+"})", "reported"),
		tag("max by (pod) (cnpg_pg_replication_in_recovery{"+sel+"})", "recovery"),
		tag("count by (pod) (cnpg_collector_up{"+sel+"})", "scraped"),
	}, " or ")
	res, err := q.Query(ctx, query)
	if err != nil {
		return CNPGFleetSlots{}, err
	}
	known := map[string]bool{}
	for _, c := range clusters {
		known[c] = true
	}
	return parseCNPGFleetSlots(res, known), nil
}

func parseCNPGFleetSlots(res *prom.QueryResult, known map[string]bool) CNPGFleetSlots {
	type slotKey struct{ pod, slot string }
	inactive := map[slotKey]bool{}
	retained := map[slotKey]float64{}
	role := map[string]string{}
	out := CNPGFleetSlots{Clusters: map[string]CNPGClusterSlots{}, Scraped: map[string]bool{}}
	for _, s := range res.Series {
		if len(s.DataPoints) == 0 {
			continue
		}
		pod := s.Labels["pod"]
		cluster := CNPGClusterOfPod(pod, known)
		if cluster == "" {
			continue
		}
		v := s.DataPoints[0].Value
		key := slotKey{pod, s.Labels["slot_name"]}
		switch s.Labels[cnpgSlotRowLabel] {
		case "inactive":
			if key.slot != "" {
				inactive[key] = true
			}
		case "retained":
			if !math.IsNaN(v) && !math.IsInf(v, 0) {
				retained[key] = v
			}
		case "reported":
			out.Scraped[cluster] = true
			if _, ok := out.Clusters[cluster]; !ok {
				out.Clusters[cluster] = CNPGClusterSlots{Inactive: []CNPGSlotReading{}}
			}
		case "recovery":
			switch v {
			case 1:
				role[pod] = "standby"
			case 0:
				role[pod] = "primary"
			}
		case "scraped":
			out.Scraped[cluster] = true
		}
	}
	for key := range inactive {
		cluster := CNPGClusterOfPod(key.pod, known)
		reading := CNPGSlotReading{Slot: key.slot, Pod: key.pod, Role: role[key.pod]}
		if b, ok := retained[key]; ok {
			reading.Bytes = &b
		}
		cs := out.Clusters[cluster]
		cs.Inactive = append(cs.Inactive, reading)
		out.Clusters[cluster] = cs
	}
	for cluster, cs := range out.Clusters {
		sort.Slice(cs.Inactive, func(i, j int) bool {
			a, b := cs.Inactive[i], cs.Inactive[j]
			if (a.Bytes == nil) != (b.Bytes == nil) {
				return a.Bytes != nil
			}
			if a.Bytes != nil && *a.Bytes != *b.Bytes {
				return *a.Bytes > *b.Bytes
			}
			if a.Pod != b.Pod {
				return a.Pod < b.Pod
			}
			return a.Slot < b.Slot
		})
		if len(cs.Inactive) > CNPGFleetSlotCap {
			cs.Omitted = len(cs.Inactive) - CNPGFleetSlotCap
			cs.Inactive = cs.Inactive[:CNPGFleetSlotCap]
		}
		out.Clusters[cluster] = cs
	}
	return out
}

// querySustainedCNPGLag is best effort: without it the fleet still shows the
// current lag, it just raises no sustained-lag problem.
func querySustainedCNPGLag(ctx context.Context, q seriesQuerier, sel string, known map[string]bool) map[string]CNPGLagReading {
	// Exact on Prometheus 2.x and 3.x alike: every raw sample in the window is
	// at least the reported floor, and the series already existed when the
	// window began (it answers at offset 10m), so a standby that appeared a
	// minute ago cannot qualify. min by (pod): when two jobs scrape the same
	// Pod, a low sample in either counts. Scrape gaps are not filled in; nothing here
	// claims a sample at every moment.
	window := fmt.Sprintf("%dm", int(CNPGSustainedLagWindow.Minutes()))
	query := "(min by (pod) (min_over_time(cnpg_pg_replication_lag{" + sel + "}[" + window + "]))" +
		" and on (pod) (max by (pod) (cnpg_pg_replication_in_recovery{" + sel + "}) == 1)" +
		" and on (pod) (max by (pod) (cnpg_pg_replication_lag{" + sel + "} offset " + window + ")))"
	res, err := q.Query(ctx, query)
	if err != nil {
		return nil
	}
	out := map[string]CNPGLagReading{}
	for _, s := range res.Series {
		if len(s.DataPoints) == 0 {
			continue
		}
		pod := s.Labels["pod"]
		cluster := CNPGClusterOfPod(pod, known)
		v := s.DataPoints[0].Value
		if cluster == "" || math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
			continue
		}
		if cur, ok := out[cluster]; !ok || v > cur.Seconds || (v == cur.Seconds && pod < cur.Pod) {
			out[cluster] = CNPGLagReading{Seconds: v, Pod: pod}
		}
	}
	return out
}

// QueryCNPGDiskGrowth reads each claim's used-bytes trend over the window as
// bytes per hour (linear regression, so a single deletion does not dominate).
func (client *Client) QueryCNPGDiskGrowth(ctx context.Context, namespace string, claims []string, window time.Duration, matchers string) (map[string]float64, error) {
	if client == nil {
		return nil, errors.New("Prometheus client not initialized")
	}
	return queryCNPGDiskGrowth(ctx, client, namespace, claims, window, matchers)
}

func queryCNPGDiskGrowth(ctx context.Context, q seriesQuerier, namespace string, claims []string, window time.Duration, matchers string) (map[string]float64, error) {
	out := map[string]float64{}
	if len(claims) == 0 {
		return out, nil
	}
	sel := withScope(cnpgClaimSelector(namespace, claims), matchers)
	metric := "kubelet_volume_stats_used_bytes{" + sel + "}"
	// A fresh scrape must not certify a stopped series for the same claim.
	res, err := q.Query(ctx, "3600 * max by (persistentvolumeclaim) (deriv("+metric+"["+window.String()+"]) and "+metric+")")
	if err != nil {
		return nil, err
	}
	for _, s := range res.Series {
		if len(s.DataPoints) == 0 {
			continue
		}
		v := s.DataPoints[0].Value
		if math.IsNaN(v) || math.IsInf(v, 0) {
			continue
		}
		out[s.Labels["persistentvolumeclaim"]] = v
	}
	return out, nil
}

// cnpgClaimSelector selects all the named claims in one matcher.
func cnpgClaimSelector(namespace string, claims []string) string {
	escaped := make([]string, len(claims))
	for i, c := range claims {
		escaped[i] = regexp.QuoteMeta(c)
	}
	sort.Strings(escaped)
	return "namespace=" + strconv.Quote(namespace) + ",persistentvolumeclaim=~" + strconv.Quote(strings.Join(escaped, "|"))
}
