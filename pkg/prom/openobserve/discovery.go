package openobserve

// Discovery reads cluster identity labels across every cluster writing to the
// organization, so it runs on the raw, unscoped transport. Only Hub's and local
// Radar's discovery code may call these helpers, and nothing they read is ever
// shown as a metric: the results only decide which scope (RewriteOptions)
// a cluster's real queries are confined to.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/skyhook-io/radar/pkg/prom"
)

// PodRef is one Pod of a cluster's sample.
type PodRef struct{ Name, UID string }

// PodSample is up to MaxSamplePods Pods from one namespace of the cluster.
type PodSample struct {
	Namespace string
	Pods      []PodRef
}

// MaxSamplePods bounds a PodSample.
const MaxSamplePods = 100

// Family is a group of charts that reads one kind of metric.
type Family string

const (
	FamilyCPU        Family = "cpu"
	FamilyMemory     Family = "memory"
	FamilyThrottling Family = "throttling"
	FamilyNetwork    Family = "network"
	FamilyFilesystem Family = "filesystem"
	FamilyRestarts   Family = "restarts"
	FamilyVolumes    Family = "volumes"
	FamilyNodes      Family = "nodes"
	FamilyHPA        Family = "hpa"
)

// AllFamilies in display order.
var AllFamilies = []Family{FamilyCPU, FamilyMemory, FamilyThrottling, FamilyNetwork, FamilyFilesystem, FamilyRestarts, FamilyVolumes, FamilyNodes, FamilyHPA}

// familyMetrics is the Prometheus-named metric whose presence decides a family.
var familyMetrics = map[Family]string{
	FamilyCPU:        "container_cpu_usage_seconds_total",
	FamilyMemory:     "container_memory_working_set_bytes",
	FamilyThrottling: "container_cpu_cfs_throttled_periods_total",
	FamilyNetwork:    "container_network_receive_bytes_total",
	FamilyFilesystem: "container_fs_writes_bytes_total",
	FamilyRestarts:   "kube_pod_container_status_restarts_total",
	FamilyVolumes:    "kubelet_volume_stats_used_bytes",
	FamilyNodes:      "node_cpu_seconds_total",
	FamilyHPA:        "kube_horizontalpodautoscaler_status_current_replicas",
}

// PartitionLabels are the labels a Prometheus setup commonly names its
// cluster with, in suggestion preference order.
var PartitionLabels = []string{"cluster", "cluster_name", "k8s_cluster", "k8s_cluster_name", "kubernetes_cluster", "cluster_id"}

// IDLabel is the label a Prometheus setup adds to identify a cluster by its
// kube-system namespace UID.
const IDLabel = "radar_cluster_uid"

// UnsafeDefaultName is the cluster name the OpenObserve collector chart (and
// many examples) ship with; it never identifies a cluster.
const UnsafeDefaultName = "cluster1"

// VersionWarning is shown when LegacyFilterBehaviour reports true.
const VersionWarning = "This OpenObserve version ignores filters on labels a metric doesn't have, so Radar only shows metrics whose data carries the cluster label; others show as not collected. Upgrade to v1.2 or later."

// NameMatchWarning is the standing warning on a cluster matched by name.
const NameMatchWarning = "Matched by cluster name. If another cluster writes metrics under the same name, Radar cannot tell them apart. Add a radar_cluster_uid label with this cluster's kube-system namespace UID to match by ID."

// ErrUnsafeDefault refuses the collector chart's default cluster name as a filter.
var ErrUnsafeDefault = errors.New(`openobserve: "cluster1" is a default cluster name shared by many clusters and cannot identify this one`)

// ErrNameBelongsToOtherCluster refuses a confirmed name whose collector data
// carries a different cluster's ID.
var ErrNameBelongsToOtherCluster = errors.New("openobserve: the cluster name belongs to another cluster")

// ErrNoProbeData is returned by LegacyFilterBehaviour when the organization
// holds no data to probe with.
var ErrNoProbeData = errors.New("openobserve: no metrics to probe the store's filter behaviour with")

// ClusterMatch is how one cluster's metrics are found in the organization.
type ClusterMatch struct {
	By      string // "id" | "name"
	Dialect Dialect
	// Options is the ready-to-use scope for Rewrite, RewritingTransport and the relay.
	Options RewriteOptions
	// Label and Value identify the cluster for display (radar_cluster_uid,
	// k8s_cluster_uid, cluster, k8s_cluster...).
	Label, Value string
	// Name is the cluster name label value seen with the ID, if any.
	Name    string
	Missing []Family
	// Warning is standing text for name matches and refused gateway metrics.
	Warning string
}

// Suggestion is the label/value the sampled Pods were found under.
type Suggestion struct {
	Label, Value string
	// UnsafeDefault: Value is the shared default name and must not be used.
	UnsafeDefault bool
	// Conflict: another cluster writes Pods with the same namespace and name
	// under the same value.
	Conflict bool
	Reason   string
}

const (
	discoveryConcurrency = 4
	discoveryRetries     = 2
	uidBatchSize         = 64
	lookback             = "15m"
)

var (
	uidPattern       = regexp.MustCompile(`^[A-Za-z0-9-]{1,64}$`)
	labelNamePattern = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)
)

// retryDelay is the wait before retry attempt n (1-based); a variable for tests.
var retryDelay = func(n int) time.Duration {
	base := time.Duration(n) * 250 * time.Millisecond
	return base + rand.N(250*time.Millisecond)
}

// discoveryTransport caps concurrency and retries OpenObserve's overload
// answer, which is HTTP 400 "bad_data: transport error" rather than 429.
type discoveryTransport struct {
	inner prom.Transport
	sem   chan struct{}
}

func newDiscoveryTransport(t prom.Transport) *discoveryTransport {
	if d, ok := t.(*discoveryTransport); ok {
		return d
	}
	return &discoveryTransport{inner: t, sem: make(chan struct{}, discoveryConcurrency)}
}

func (d *discoveryTransport) Do(ctx context.Context, method, path string, params url.Values) ([]byte, error) {
	select {
	case d.sem <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	defer func() { <-d.sem }()
	for attempt := 0; ; attempt++ {
		body, err := d.inner.Do(ctx, method, path, params)
		if err == nil || attempt >= discoveryRetries || !isOverloaded(err) {
			return body, err
		}
		select {
		case <-time.After(retryDelay(attempt + 1)):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

func (d *discoveryTransport) Address() string { return d.inner.Address() }

func isOverloaded(err error) bool {
	var httpErr *prom.HTTPError
	return errors.As(err, &httpErr) && httpErr.StatusCode == http.StatusBadRequest &&
		bytes.Contains(httpErr.Body, []byte("transport error"))
}

func query(ctx context.Context, t prom.Transport, q string) ([]prom.Series, error) {
	res, err := prom.NewClient(t).Query(ctx, q)
	if err != nil {
		return nil, err
	}
	return res.Series, nil
}

func uidAlternation(uids []string) string {
	return strings.Join(uids, "|")
}

// DiscoverIDs finds clusters by ID in one batched pass: radar_cluster_uid on
// Prometheus remote-write data (kube_pod_info, then up) and k8s_cluster_uid on
// the OpenObserve collector agent's data. Clusters not found are absent from
// the map. A cluster found both ways is matched on its standard data.
//
// Missing is filled only with the families the dialect cannot have; run
// DetectFamilies for what actually has data.
func DiscoverIDs(ctx context.Context, t prom.Transport, uids []string) (map[string]ClusterMatch, error) {
	want := map[string]bool{}
	var list []string
	for _, u := range uids {
		if !uidPattern.MatchString(u) {
			return nil, fmt.Errorf("openobserve: invalid cluster UID %q", u)
		}
		if !want[u] {
			want[u] = true
			list = append(list, u)
		}
	}
	dt := newDiscoveryTransport(t)
	out := map[string]ClusterMatch{}
	if len(list) == 0 {
		return out, nil
	}

	group := IDLabel + ", " + strings.Join(PartitionLabels, ", ")
	collectorFound := map[string]string{}
	for start := 0; start < len(list); start += uidBatchSize {
		batch := list[start:min(start+uidBatchSize, len(list))]
		alt := uidAlternation(batch)
		queries := []string{
			fmt.Sprintf(`count by (%s) (last_over_time(kube_pod_info{%s=~"%s"}[%s]))`, group, IDLabel, alt, lookback),
			fmt.Sprintf(`count by (%s) (last_over_time(up{%s=~"%s"}[%s]))`, group, IDLabel, alt, lookback),
			fmt.Sprintf(`count by (%s, k8s_cluster) (last_over_time(k8s_pod_cpu_time{%s=~"%s"}[%s]))`, collectorUIDLabel, collectorUIDLabel, alt, lookback),
		}
		results := make([][]prom.Series, len(queries))
		errs := make([]error, len(queries))
		var wg sync.WaitGroup
		for i, q := range queries {
			wg.Add(1)
			go func() {
				defer wg.Done()
				results[i], errs[i] = query(ctx, dt, q)
			}()
		}
		wg.Wait()
		if err := errors.Join(errs...); err != nil {
			return nil, err
		}
		for _, series := range results[:2] {
			for _, s := range series {
				// The store may ignore the matcher (OpenObserve < v1.2), so
				// trust only the label value read back.
				uid := s.Labels[IDLabel]
				if !want[uid] {
					continue
				}
				if _, done := out[uid]; done {
					continue
				}
				name := ""
				for _, l := range PartitionLabels {
					if v := s.Labels[l]; v != "" {
						name = v
						break
					}
				}
				out[uid] = ClusterMatch{
					By:      "id",
					Dialect: DialectStandard,
					Options: RewriteOptions{Matchers: []Matcher{{Name: IDLabel, Value: uid}}, Dialect: DialectStandard},
					Label:   IDLabel,
					Value:   uid,
					Name:    name,
				}
			}
		}
		for _, s := range results[2] {
			uid := s.Labels[collectorUIDLabel]
			if want[uid] && s.Labels["k8s_cluster"] != "" {
				collectorFound[uid] = s.Labels["k8s_cluster"]
			}
		}
	}

	var pending []string
	for uid := range collectorFound {
		if _, done := out[uid]; !done {
			pending = append(pending, uid)
		}
	}
	if len(pending) == 0 {
		return out, nil
	}
	usage, err := collectorNameUsage(ctx, dt)
	if err != nil {
		return nil, err
	}
	for _, uid := range pending {
		out[uid] = collectorMatch(uid, collectorFound[uid], usage)
	}
	return out, nil
}

// collectorNameUsage maps each collector cluster name to the k8s_cluster_uid
// values (including "" for agents that send none) seen with it.
func collectorNameUsage(ctx context.Context, t prom.Transport) (map[string]map[string]bool, error) {
	series, err := query(ctx, t, fmt.Sprintf(`count by (%s, k8s_cluster) (last_over_time(k8s_pod_cpu_time[%s]))`, collectorUIDLabel, lookback))
	if err != nil {
		return nil, err
	}
	usage := map[string]map[string]bool{}
	for _, s := range series {
		name := s.Labels["k8s_cluster"]
		if name == "" {
			continue
		}
		if usage[name] == nil {
			usage[name] = map[string]bool{}
		}
		usage[name][s.Labels[collectorUIDLabel]] = true
	}
	return usage, nil
}

func collectorMatch(uid, name string, usage map[string]map[string]bool) ClusterMatch {
	unique := name != UnsafeDefaultName && len(usage[name]) == 1 && usage[name][uid]
	m := ClusterMatch{
		By:      "id",
		Dialect: DialectCollector,
		Options: RewriteOptions{
			Matchers:              []Matcher{{Name: "k8s_cluster", Value: name}},
			Dialect:               DialectCollector,
			CollectorUID:          uid,
			GatewayMetricsAllowed: unique,
		},
		Label: collectorUIDLabel,
		Value: uid,
		Name:  name,
	}
	m.Missing = staticMissing(m.Options)
	if !unique {
		if name == UnsafeDefaultName {
			m.Warning = fmt.Sprintf("This cluster's OpenObserve collector sends the default cluster name %q, which other clusters can share. Radar refuses its kube-state metrics (restarts, HPA, workload ownership) and scraped disk metrics, which carry only the name, until the collector is given a unique cluster name or a radar_cluster_uid label.", name)
		} else {
			m.Warning = fmt.Sprintf("More than one OpenObserve collector sends the cluster name %q. Radar refuses this cluster's kube-state metrics (restarts, HPA, workload ownership) and scraped disk metrics, which carry only the name, until the collector is given a unique cluster name or a radar_cluster_uid label.", name)
		}
	}
	return m
}

// staticMissing is the families a scope can never answer.
func staticMissing(opts RewriteOptions) []Family {
	if opts.Dialect != DialectCollector {
		return nil
	}
	var missing []Family
	for _, f := range AllFamilies {
		if _, err := Rewrite(familyMetrics[f], opts); errors.Is(err, ErrNotCollected) {
			missing = append(missing, f)
		}
	}
	return missing
}

// SuggestName is the light Pod check: it finds the label and value the
// sampled Pods are stored under in kube_pod_info. It never matches; a
// suggestion must be confirmed (ConfirmedNameMatch) before it scopes anything.
func SuggestName(ctx context.Context, t prom.Transport, sample PodSample, overrideLabel string) (Suggestion, error) {
	if sample.Namespace == "" {
		return Suggestion{}, errors.New("openobserve: Pod sample has no namespace")
	}
	if len(sample.Pods) == 0 || len(sample.Pods) > MaxSamplePods {
		return Suggestion{}, fmt.Errorf("openobserve: Pod sample must hold 1 to %d Pods", MaxSamplePods)
	}
	sampleUIDs := map[string]bool{}
	sampleNames := map[string]bool{}
	var uids []string
	for _, p := range sample.Pods {
		if !uidPattern.MatchString(p.UID) {
			return Suggestion{}, fmt.Errorf("openobserve: invalid Pod UID %q", p.UID)
		}
		if !sampleUIDs[p.UID] {
			sampleUIDs[p.UID] = true
			uids = append(uids, p.UID)
		}
		sampleNames[p.Name] = true
	}
	candidates := append([]string(nil), PartitionLabels...)
	if overrideLabel != "" {
		overrideLabel = strings.ToLower(overrideLabel)
		if !labelNamePattern.MatchString(overrideLabel) || strings.HasPrefix(overrideLabel, "__") {
			return Suggestion{}, fmt.Errorf("openobserve: invalid cluster label %q", overrideLabel)
		}
		candidates = append([]string{overrideLabel}, removeNames(candidates, []string{overrideLabel})...)
	}

	dt := newDiscoveryTransport(t)
	ns := strconv.Quote(sample.Namespace)
	series, err := query(ctx, dt, fmt.Sprintf(`count by (uid, %s) (last_over_time(kube_pod_info{namespace=%s,uid=~"%s"}[%s]))`,
		strings.Join(candidates, ", "), ns, uidAlternation(uids), lookback))
	if err != nil {
		return Suggestion{}, err
	}

	type key struct{ label, value string }
	coverage := map[key]map[string]bool{}
	found := map[string]bool{}
	for _, s := range series {
		uid := s.Labels["uid"]
		if !sampleUIDs[uid] {
			continue
		}
		found[uid] = true
		for _, l := range candidates {
			if v := s.Labels[l]; v != "" {
				k := key{l, v}
				if coverage[k] == nil {
					coverage[k] = map[string]bool{}
				}
				coverage[k][uid] = true
			}
		}
	}
	if len(found) == 0 {
		return Suggestion{Reason: fmt.Sprintf("None of the %d sampled Pods from namespace %s were found in OpenObserve's kube_pod_info in the last 15 minutes.", len(uids), sample.Namespace)}, nil
	}
	if len(coverage) == 0 {
		return Suggestion{Reason: fmt.Sprintf("%d sampled Pods from namespace %s are in OpenObserve, but carry none of the cluster labels %s.", len(found), sample.Namespace, strings.Join(candidates, ", "))}, nil
	}
	rank := map[string]int{}
	for i, l := range candidates {
		rank[l] = i
	}
	keys := make([]key, 0, len(coverage))
	for k := range coverage {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		a, b := keys[i], keys[j]
		if len(coverage[a]) != len(coverage[b]) {
			return len(coverage[a]) > len(coverage[b])
		}
		if rank[a.label] != rank[b.label] {
			return rank[a.label] < rank[b.label]
		}
		return a.value < b.value
	})
	best := keys[0]
	sug := Suggestion{
		Label:         best.label,
		Value:         best.value,
		UnsafeDefault: best.value == UnsafeDefaultName,
		Reason:        fmt.Sprintf("%d of %d sampled Pods from namespace %s are stored under %s=%q.", len(coverage[best]), len(uids), sample.Namespace, best.label, best.value),
	}

	conflicts, err := query(ctx, dt, fmt.Sprintf(`count by (uid, pod, %s) (last_over_time(kube_pod_info{namespace=%s,%s=%s}[%s]))`,
		best.label, ns, best.label, strconv.Quote(best.value), lookback))
	if err != nil {
		return Suggestion{}, err
	}
	var clash []string
	for _, s := range conflicts {
		if s.Labels[best.label] != best.value || sampleUIDs[s.Labels["uid"]] || !sampleNames[s.Labels["pod"]] {
			continue
		}
		clash = append(clash, s.Labels["pod"])
	}
	if len(clash) > 0 {
		sort.Strings(clash)
		sug.Conflict = true
		sug.Reason += fmt.Sprintf(" Another cluster also writes Pod %s/%s under that value, so the name does not identify this cluster.", sample.Namespace, clash[0])
	}
	if sug.UnsafeDefault {
		sug.Reason += fmt.Sprintf(" %q is a default name that other clusters may share; give this cluster a unique name or a radar_cluster_uid label.", UnsafeDefaultName)
	}
	return sug, nil
}

// ConfirmedNameMatch builds a name match from a label/value the user
// confirmed, detecting the dialect and the families with no data. ownUID is
// the kube-system namespace UID of the cluster being matched: a name whose
// collector data carries another cluster's k8s_cluster_uid is refused with
// ErrNameBelongsToOtherCluster.
func ConfirmedNameMatch(ctx context.Context, t prom.Transport, label, value, ownUID string) (ClusterMatch, error) {
	label = strings.ToLower(label)
	if !labelNamePattern.MatchString(label) || strings.HasPrefix(label, "__") {
		return ClusterMatch{}, fmt.Errorf("openobserve: invalid cluster label %q", label)
	}
	if value == "" {
		return ClusterMatch{}, errors.New("openobserve: cluster name is empty")
	}
	if value == UnsafeDefaultName {
		return ClusterMatch{}, ErrUnsafeDefault
	}
	dt := newDiscoveryTransport(t)
	qv := strconv.Quote(value)

	var collectorUIDs map[string]bool
	if label == "k8s_cluster" {
		series, err := query(ctx, dt, fmt.Sprintf(`count by (%s, k8s_cluster) (last_over_time(k8s_pod_cpu_time{k8s_cluster=%s}[%s]))`, collectorUIDLabel, qv, lookback))
		if err != nil {
			return ClusterMatch{}, err
		}
		for _, s := range series {
			if s.Labels["k8s_cluster"] == value {
				if collectorUIDs == nil {
					collectorUIDs = map[string]bool{}
				}
				collectorUIDs[s.Labels[collectorUIDLabel]] = true
			}
		}
	}

	var m ClusterMatch
	switch {
	case len(collectorUIDs) > 1:
		return ClusterMatch{}, fmt.Errorf("openobserve: more than one OpenObserve collector sends the cluster name %q", value)
	case len(collectorUIDs) == 1:
		var uid string
		for u := range collectorUIDs {
			uid = u
		}
		if uid != "" && uid != ownUID {
			return ClusterMatch{}, fmt.Errorf("%w: the OpenObserve collector sending %s=%q reports cluster ID %s", ErrNameBelongsToOtherCluster, label, value, uid)
		}
		m = ClusterMatch{
			Dialect: DialectCollector,
			Options: RewriteOptions{Matchers: []Matcher{{Name: "k8s_cluster", Value: value}}, Dialect: DialectCollector, CollectorUID: uid, GatewayMetricsAllowed: true},
		}
	default:
		series, err := query(ctx, dt, fmt.Sprintf(`count by (%s) (last_over_time(kube_pod_info{%s=%s}[%s]))`, label, label, qv, lookback))
		if err != nil {
			return ClusterMatch{}, err
		}
		present := false
		for _, s := range series {
			if s.Labels[label] == value {
				present = true
			}
		}
		if !present {
			return ClusterMatch{}, fmt.Errorf("openobserve: no metrics found under %s=%q in the last 15 minutes", label, value)
		}
		m = ClusterMatch{
			Dialect: DialectStandard,
			Options: RewriteOptions{Matchers: []Matcher{{Name: label, Value: value}}, Dialect: DialectStandard},
		}
	}
	m.By, m.Label, m.Value, m.Name, m.Warning = "name", label, value, value, NameMatchWarning
	missing, err := DetectFamilies(ctx, dt, m.Options)
	if err != nil {
		return ClusterMatch{}, err
	}
	m.Missing = missing
	return m, nil
}

// DetectFamilies reports the families with no data for the scope in the last
// 15 minutes, one cheap scoped query each. A family the scope refuses
// (ErrNotCollected) is missing.
func DetectFamilies(ctx context.Context, t prom.Transport, opts RewriteOptions) ([]Family, error) {
	if _, err := prepare(opts); err != nil {
		return nil, err
	}
	scoped := &RewritingTransport{Inner: newDiscoveryTransport(t), Options: opts}
	missing := make([]bool, len(AllFamilies))
	errs := make([]error, len(AllFamilies))
	var wg sync.WaitGroup
	for i, f := range AllFamilies {
		wg.Add(1)
		go func() {
			defer wg.Done()
			series, err := query(ctx, scoped, fmt.Sprintf(`count(last_over_time(%s[%s]))`, familyMetrics[f], lookback))
			switch {
			case errors.Is(err, ErrNotCollected):
				missing[i] = true
			case err != nil:
				errs[i] = fmt.Errorf("openobserve: detect %s: %w", f, err)
			default:
				missing[i] = !hasPositive(series)
			}
		}()
	}
	wg.Wait()
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	var out []Family
	for i, f := range AllFamilies {
		if missing[i] {
			out = append(out, f)
		}
	}
	return out, nil
}

func hasPositive(series []prom.Series) bool {
	for _, s := range series {
		for _, p := range s.DataPoints {
			if p.Value > 0 {
				return true
			}
		}
	}
	return false
}

// legacyProbeMetrics are metrics likely to hold data, tried in order.
var legacyProbeMetrics = []string{"kube_pod_info", "up", "k8s_pod_cpu_time"}

// LegacyFilterBehaviour reports whether the store ignores a matcher on a
// label a metric never had (OpenObserve before v1.2), by filtering a metric
// that has data on a label nobody writes. Show VersionWarning when true.
func LegacyFilterBehaviour(ctx context.Context, t prom.Transport) (bool, error) {
	dt := newDiscoveryTransport(t)
	for _, metric := range legacyProbeMetrics {
		ignored, err := query(ctx, dt, fmt.Sprintf(`count(last_over_time(%s{radar_probe_never_written="x"}[%s]))`, metric, lookback))
		if err != nil {
			return false, err
		}
		if hasPositive(ignored) {
			return true, nil
		}
		all, err := query(ctx, dt, fmt.Sprintf(`count(last_over_time(%s[%s]))`, metric, lookback))
		if err != nil {
			return false, err
		}
		if hasPositive(all) {
			return false, nil
		}
	}
	return false, ErrNoProbeData
}

// LabelValue is one cluster name as OpenObserve stores it.
type LabelValue struct {
	Label string `json:"label"`
	Value string `json:"value"`
}

// MaxClusterNames bounds ListClusterNames.
const MaxClusterNames = 50

// ListClusterNames lists the cluster names OpenObserve holds data for: the
// values of the partition labels (and overrideLabel) on kube_pod_info and the
// collector's cluster name, over the last 15 minutes. It helps a user whose
// cluster wasn't found pick the right label; nothing here scopes a query.
func ListClusterNames(ctx context.Context, t prom.Transport, overrideLabel string) ([]LabelValue, error) {
	labels := append([]string(nil), PartitionLabels...)
	if overrideLabel != "" {
		overrideLabel = strings.ToLower(overrideLabel)
		if !labelNamePattern.MatchString(overrideLabel) || strings.HasPrefix(overrideLabel, "__") {
			return nil, fmt.Errorf("openobserve: invalid cluster label %q", overrideLabel)
		}
		labels = append([]string{overrideLabel}, removeNames(labels, []string{overrideLabel})...)
	}
	dt := newDiscoveryTransport(t)
	queries := []string{
		fmt.Sprintf(`count by (%s) (last_over_time(kube_pod_info[%s]))`, strings.Join(labels, ", "), lookback),
		fmt.Sprintf(`count by (k8s_cluster) (last_over_time(k8s_pod_cpu_time[%s]))`, lookback),
	}
	results := make([][]prom.Series, len(queries))
	errs := make([]error, len(queries))
	var wg sync.WaitGroup
	for i, q := range queries {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i], errs[i] = query(ctx, dt, q)
		}()
	}
	wg.Wait()
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	seen := map[LabelValue]bool{}
	for _, s := range results[0] {
		for _, l := range labels {
			if v := s.Labels[l]; v != "" {
				seen[LabelValue{Label: l, Value: v}] = true
			}
		}
	}
	for _, s := range results[1] {
		if v := s.Labels["k8s_cluster"]; v != "" {
			seen[LabelValue{Label: "k8s_cluster", Value: v}] = true
		}
	}
	out := make([]LabelValue, 0, len(seen))
	for lv := range seen {
		out = append(out, lv)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Value != out[j].Value {
			return out[i].Value < out[j].Value
		}
		return out[i].Label < out[j].Label
	})
	if len(out) > MaxClusterNames {
		out = out[:MaxClusterNames]
	}
	return out, nil
}
