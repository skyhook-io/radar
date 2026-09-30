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

	"github.com/skyhook-io/radar/internal/k8s"
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

	CNPGIsolationConfigured = "configured"
	CNPGIsolationVerified   = "verified"
	CNPGIsolationUnverified = "unverified"

	cnpgHistoryMaxSeries   = 12
	cnpgHistoryConcurrency = 4
)

// ErrCNPGScopeAmbiguous means the selected series exist under more than one
// cluster identity in this Prometheus, so any answer could mix clusters.
var ErrCNPGScopeAmbiguous = errors.New("cnpg metrics: the selected series appear under more than one cluster identity")

// ErrCNPGScopeMismatch means the verified cluster identity labels select none
// of the probed series although unscoped ones exist.
var ErrCNPGScopeMismatch = errors.New("cnpg metrics: cluster identity labels do not appear on the selected series")

type cnpgQuerier interface {
	Query(ctx context.Context, query string) (*prom.QueryResult, error)
	QueryRange(ctx context.Context, query string, start, end time.Time, step time.Duration) (*prom.QueryResult, error)
}

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

// CNPGIsolation says how the queries keep to this Kubernetes cluster's series.
type CNPGIsolation struct {
	Mode   string            `json:"mode"`
	Labels map[string]string `json:"labels,omitempty"`
	Note   string            `json:"note"`
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

func withScope(selector, matchers string) string {
	if matchers == "" {
		return selector
	}
	return selector + "," + matchers
}

// scopeProbe names the series whose identity labels decide a scope: one
// selector per batch and, for a chart over a range, the window every identity
// is collected over (an instant check would miss one that stopped reporting
// minutes ago but still fills the chart).
type scopeProbe struct {
	metric string
	// key is the label one Kubernetes object's series share (pod, claim).
	key       string
	selectors []string
	window    time.Duration
}

func (p scopeProbe) over(sel string) string {
	if p.window <= 0 {
		return p.metric + "{" + sel + "}"
	}
	return "count_over_time(" + p.metric + "{" + sel + "}[" + p.window.String() + "])"
}

// ResolveCNPGScope decides the cluster-identity matchers for one namespace's
// CNPG exporter series over window (0 for an instant read).
func ResolveCNPGScope(ctx context.Context, namespace, selector string, anchors []prom.WorkloadPodIdentity, window time.Duration) (string, CNPGIsolation, error) {
	return resolveScope(ctx, namespace, scopeProbe{metric: "cnpg_collector_up", key: "pod", selectors: []string{selector}, window: window}, anchors, nil)
}

// ResolvePVCScope decides the cluster-identity matchers for the named claims'
// kubelet volume stats over window (0 for an instant read).
func ResolvePVCScope(ctx context.Context, namespace string, claims []string, anchors []prom.WorkloadPodIdentity, window time.Duration) (string, CNPGIsolation, error) {
	return resolveScope(ctx, namespace, pvcScopeProbe(namespace, claims, window), anchors, nil)
}

func pvcScopeProbe(namespace string, claims []string, window time.Duration) scopeProbe {
	return scopeProbe{metric: "kubelet_volume_stats_capacity_bytes", key: "persistentvolumeclaim", selectors: CNPGClaimSelectors(namespace, claims), window: window}
}

// CNPGClaimSelectors selects the named claims of one namespace, in batches
// that keep each regex matcher small.
func CNPGClaimSelectors(namespace string, claims []string) []string {
	names := make([]string, len(claims))
	for i, c := range claims {
		names[i] = regexp.QuoteMeta(c)
	}
	sort.Strings(names)
	var out []string
	for start := 0; start < len(names); start += pvcUsageBatchSize {
		end := min(start+pvcUsageBatchSize, len(names))
		out = append(out, "namespace="+strconv.Quote(namespace)+",persistentvolumeclaim=~"+strconv.Quote(strings.Join(names[start:end], "|")))
	}
	return out
}

// resolveScope applies an operator-configured scope first, then identity
// labels proven by kube-state-metrics Pod UIDs. Only those two may add
// matchers: labels merely seen on the series are not identity (the CNPG
// exporter's own `cluster` label is the database cluster's name).
// With no anchors, a cache lets the proof use the namespace's current Pods.
func resolveScope(ctx context.Context, namespace string, probe scopeProbe, anchors []prom.WorkloadPodIdentity, cache *k8s.ResourceCache) (string, CNPGIsolation, error) {
	client := GetClient()
	if client == nil {
		return "", CNPGIsolation{}, errors.New("Prometheus client not initialized")
	}
	if config, _, configured := client.workloadMetricsConfig(); configured {
		m, err := config.Matchers()
		if err != nil {
			return "", CNPGIsolation{}, err
		}
		iso := CNPGIsolation{Mode: CNPGIsolationConfigured, Labels: config.ClusterLabels, Note: "cluster identity configured by the operator"}
		if config.SingleCluster {
			iso.Note = "the operator declared this Prometheus single-cluster"
		}
		return m, iso, nil
	}
	var verified map[string]string
	if len(anchors) > 0 || cache != nil {
		if cfg, err := client.historicalClusterScope(ctx, PodScope{Namespace: namespace, Identities: anchors}, cache); err == nil {
			verified = cfg.ClusterLabels
		}
	}
	return decideScope(ctx, client, probe, verified)
}

func decideScope(ctx context.Context, q cnpgQuerier, probe scopeProbe, verified map[string]string) (string, CNPGIsolation, error) {
	if len(verified) > 0 {
		m, err := prom.WorkloadMetricsScope{ClusterLabels: verified}.Matchers()
		if err != nil {
			return "", CNPGIsolation{}, err
		}
		for _, sel := range probe.selectors {
			scoped, err := q.Query(ctx, "count("+probe.over(withScope(sel, m))+")")
			if err != nil {
				return "", CNPGIsolation{}, err
			}
			if len(scoped.Series) > 0 {
				return m, cnpgVerifiedIsolation(verified), nil
			}
		}
		for _, sel := range probe.selectors {
			all, err := q.Query(ctx, "count("+probe.over(sel)+")")
			if err != nil {
				return "", CNPGIsolation{}, err
			}
			if len(all.Series) > 0 {
				return "", CNPGIsolation{}, ErrCNPGScopeMismatch
			}
		}
		return m, cnpgVerifiedIsolation(verified), nil
	}
	// Unproven: nothing is pinned. One object (Pod, claim) whose series carry
	// more than one set of partition labels over the range may be two
	// clusters' objects of the same name, so that is refused.
	for _, sel := range probe.selectors {
		res, err := q.Query(ctx, "max(count by ("+probe.key+") (count by ("+probe.key+","+strings.Join(partitionLabels, ",")+") ("+probe.over(sel)+")))")
		if err != nil {
			return "", CNPGIsolation{}, err
		}
		if len(res.Series) > 0 && len(res.Series[0].DataPoints) > 0 && res.Series[0].DataPoints[0].Value > 1 {
			return "", CNPGIsolation{}, ErrCNPGScopeAmbiguous
		}
	}
	return "", CNPGIsolation{Mode: CNPGIsolationUnverified, Note: "selected by namespace and names; this Prometheus shows one identity for each of them over the range, but Radar could not prove it is this cluster's"}, nil
}

func cnpgVerifiedIsolation(labels map[string]string) CNPGIsolation {
	return CNPGIsolation{Mode: CNPGIsolationVerified, Labels: labels, Note: "cluster identity labels matched to this cluster's Pod UIDs"}
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
	Grant      string                 `json:"grant,omitempty"`
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

// CNPGHistoryRequest is what the server decided the caller may read.
type CNPGHistoryRequest struct {
	Namespace string
	Cluster   string
	Range     CNPGHistoryRange
	End       time.Time
	// Matchers are cluster-identity matchers from ResolveCNPGScope for the
	// exporter series; PVCMatchers from ResolvePVCScope for the claims.
	Matchers    string
	PVCMatchers string
	// PVCAmbiguous says why the claims' identity could not be settled.
	PVCAmbiguous string
	// PodsDenied / PVCDenied carry the grant that is missing, empty when allowed.
	PodsDenied string
	PVCDenied  string
	// PVCReason explains an empty Claims when the claims were not denied.
	PVCReason string
	Claims    []string
}

// QueryCNPGHistory runs every chart query, a few at a time.
func QueryCNPGHistory(ctx context.Context, req CNPGHistoryRequest) ([]CNPGHistoryChart, error) {
	client := GetClient()
	if client == nil {
		return nil, errors.New("Prometheus client not initialized")
	}
	return queryCNPGHistory(ctx, client, req), nil
}

func queryCNPGHistory(ctx context.Context, q cnpgQuerier, req CNPGHistoryRequest) []CNPGHistoryChart {
	sel := withScope(CNPGInstanceSelector(req.Namespace, req.Cluster), req.Matchers)
	pvcSel := ""
	if len(req.Claims) > 0 {
		pvcSel = withScope(cnpgClaimSelector(req.Namespace, req.Claims), req.PVCMatchers)
	}
	end := req.End.Truncate(req.Range.Step)
	start := end.Add(-req.Range.Duration)
	steps := int(req.Range.Duration/req.Range.Step) + 1

	defs := cnpgHistoryDefs(sel, pvcSel, req.Range.Step)
	charts := make([]CNPGHistoryChart, len(defs))
	sem := make(chan struct{}, cnpgHistoryConcurrency)
	var wg sync.WaitGroup
	for i, d := range defs {
		c := &charts[i]
		*c = CNPGHistoryChart{ID: d.id, Title: d.title, Unit: d.unit, Source: d.source, SeriesBy: d.seriesBy, Thresholds: d.thresholds, Series: []prom.Series{}, Steps: steps}
		switch {
		case d.pvc && req.PVCDenied != "":
			c.State, c.Grant = CNPGHistoryStateDenied, req.PVCDenied
			continue
		case !d.pvc && req.PodsDenied != "":
			c.State, c.Grant = CNPGHistoryStateDenied, req.PodsDenied
			continue
		case d.pvc && req.PVCAmbiguous != "":
			c.State, c.Reason = CNPGHistoryStateAmbiguous, req.PVCAmbiguous
			continue
		case d.pvc && pvcSel == "":
			c.State, c.Reason = CNPGHistoryStateNotRead, req.PVCReason
			if c.Reason == "" {
				c.Reason = "no volumes owned by this cluster to chart"
			}
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

func runCNPGHistoryChart(ctx context.Context, q cnpgQuerier, d cnpgHistoryDef, start, end time.Time, step time.Duration, c *CNPGHistoryChart) {
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
	if len(series) == 0 && d.presence != "" {
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
type CNPGLagReading struct {
	Seconds float64
	Pod     string
}

// CNPGFleetLag holds, per Cluster, the largest standby replay lag, and which
// Clusters have exporter series at all, so "no standby reporting" is told
// apart from "not scraped".
type CNPGFleetLag struct {
	Lag     map[string]CNPGLagReading
	Scraped map[string]bool
	// Sustained is each Cluster's worst standby's lowest lag across the
	// one-minute checks of CNPGSustainedLagWindow, each backed by a sample;
	// absent when the window is not covered.
	Sustained map[string]CNPGLagReading
}

// CNPGSustainedLagWindow is how long a standby's replay lag must stay high
// before the fleet treats it as a problem rather than a transient spike.
const CNPGSustainedLagWindow = 10 * time.Minute

// cnpgSustainedLagChecks is how many one-minute checks the window holds. Each
// check needs a sample inside its own minute, so a standby that appeared
// moments ago, or one with scrape gaps, never reads as lagging "for the whole
// window".
const cnpgSustainedLagChecks = 10

// QueryCNPGFleetLag reads the current replay lag of every standby of the
// named Clusters in one namespace with one instant query.
func QueryCNPGFleetLag(ctx context.Context, namespace string, clusters []string, matchers string) (CNPGFleetLag, error) {
	client := GetClient()
	if client == nil {
		return CNPGFleetLag{}, errors.New("Prometheus client not initialized")
	}
	return queryCNPGFleetLag(ctx, client, namespace, clusters, matchers)
}

// A Pod scraped but not in recovery answers -1 through the `or` branch, so one
// query carries both lag and presence.
func queryCNPGFleetLag(ctx context.Context, q cnpgQuerier, namespace string, clusters []string, matchers string) (CNPGFleetLag, error) {
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
		if cur, ok := out.Lag[cluster]; !ok || v > cur.Seconds || (v == cur.Seconds && pod < cur.Pod) {
			out.Lag[cluster] = CNPGLagReading{Seconds: v, Pod: pod}
		}
	}
	out.Sustained = querySustainedCNPGLag(ctx, q, sel, known)
	return out, nil
}

// querySustainedCNPGLag is best effort: without it the fleet still shows the
// current lag, it just raises no sustained-lag problem.
func querySustainedCNPGLag(ctx context.Context, q cnpgQuerier, sel string, known map[string]bool) map[string]CNPGLagReading {
	perMinute := "(max by (pod) (last_over_time(cnpg_pg_replication_lag{" + sel + "}[1m])))"
	window := fmt.Sprintf("[%dm:1m]", int(CNPGSustainedLagWindow.Minutes()))
	query := "(min_over_time(" + perMinute + window + ")" +
		" and on (pod) (max by (pod) (cnpg_pg_replication_in_recovery{" + sel + "}) == 1)" +
		" and on (pod) (count_over_time(" + perMinute + window + ") >= " + strconv.Itoa(cnpgSustainedLagChecks) + "))"
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
func QueryCNPGDiskGrowth(ctx context.Context, namespace string, claims []string, window time.Duration, matchers string) (map[string]float64, error) {
	client := GetClient()
	if client == nil {
		return nil, errors.New("Prometheus client not initialized")
	}
	return queryCNPGDiskGrowth(ctx, client, namespace, claims, window, matchers)
}

func queryCNPGDiskGrowth(ctx context.Context, q cnpgQuerier, namespace string, claims []string, window time.Duration, matchers string) (map[string]float64, error) {
	out := map[string]float64{}
	if len(claims) == 0 {
		return out, nil
	}
	sel := withScope(cnpgClaimSelector(namespace, claims), matchers)
	res, err := q.Query(ctx, "3600 * max by (persistentvolumeclaim) (deriv(kubelet_volume_stats_used_bytes{"+sel+"}["+window.String()+"]))")
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
