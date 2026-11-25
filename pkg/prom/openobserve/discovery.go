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

// FamilyLabel is how Radar names a family to people, the same names the UI
// shows (familyLabel in web/src/components/settings/openobserveStatus.ts).
func FamilyLabel(f Family) string {
	if l, ok := familyLabels[f]; ok {
		return l
	}
	return string(f)
}

var familyLabels = map[Family]string{
	FamilyCPU: "CPU", FamilyMemory: "Memory", FamilyThrottling: "CPU throttling", FamilyNetwork: "Network",
	FamilyFilesystem: "Disk I/O", FamilyRestarts: "Restarts", FamilyVolumes: "Volumes", FamilyNodes: "Nodes", FamilyHPA: "HPA",
}

// MissingFamiliesNote is the one phrase for families OpenObserve holds no
// data for.
func MissingFamiliesNote(families []Family) string {
	names := make([]string, len(families))
	for i, f := range families {
		names[i] = FamilyLabel(f)
	}
	return "No data in OpenObserve for: " + strings.Join(names, ", ") + "."
}

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
const VersionWarning = "This OpenObserve version ignores filters on labels a metric doesn't have, so Radar only shows metrics whose data carries the cluster label; others show as having no data in OpenObserve. Upgrade to v1.2 or later."

// NameMatchWarning is the standing warning on a cluster matched by name.
const NameMatchWarning = "Matched by cluster name. If another cluster writes metrics under the same name, Radar cannot tell them apart. Add a radar_cluster_uid label with this cluster's kube-system namespace UID to match by ID."

// CollectorNoIDWarning is added when the collector sending the confirmed name
// reports no cluster ID: collectors without one can't be told apart, so
// Radar can't check that only one of them sends the name.
const CollectorNoIDWarning = "The OpenObserve collector sending this name reports no cluster ID, so Radar couldn't check whether another collector sends the same name. Set k8s_cluster_uid on the collector to match by ID."

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
	lookbackDuration     = 15 * time.Minute
	// activeWindow is how recently a Pod must have reported to count as
	// another cluster writing the same name. A Pod replaced under the same
	// name (a StatefulSet's db-0) stops reporting; a cluster that really
	// shares the name keeps writing. Five minutes is Prometheus' staleness
	// window, longer than any scrape interval a collector uses, so a live
	// clash is never missed.
	activeWindow = "5m"
	// nameHistory is how far back a collector cluster name must have been
	// sent by this cluster's collector alone before Radar reads the metrics
	// that carry only that name (kube-state and scraped disk metrics). It
	// covers the longest span Radar reads (7-day charts and rightsizing):
	// a cluster rebuilt under the same name leaves the old one's series in
	// that span, and they would chart as this cluster's.
	nameHistory = "7d"
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
					if v := s.Labels[l]; v != "" && !IsReservedValue(v) {
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
	usage, err := collectorNameUsage(ctx, dt, "")
	if err != nil {
		return nil, err
	}
	for _, uid := range pending {
		name := collectorFound[uid]
		elsewhere := ""
		if collectorNameUnique(uid, name, usage) {
			if elsewhere, err = nameUsedElsewhere(ctx, dt, name, uid); err != nil {
				return nil, err
			}
		}
		out[uid] = collectorMatch(uid, name, usage, elsewhere)
	}
	return out, nil
}

func collectorNameUnique(uid, name string, usage map[string]map[string]bool) bool {
	return name != UnsafeDefaultName && !IsReservedValue(name) && len(usage[name]) == 1 && usage[name][uid]
}

// nameEvidenceMetrics are metrics nearly every cluster's Prometheus or
// collector gateway sends, read to find another cluster writing a collector's
// cluster name under any label.
var nameEvidenceMetrics = []string{"kube_pod_info", "up"}

// maxNameEvidenceNodes bounds the node lists nameUsedElsewhere compares.
const maxNameEvidenceNodes = 5000

// nameUsedElsewhere looks, over nameHistory, for another cluster writing
// metrics under a collector's cluster name, which the collector's
// k8s_pod_cpu_time alone can't show: a Prometheus remote-writing the name in
// k8s_cluster (or any partition label) without a collector. uid is the
// collector's k8s_cluster_uid ("" for an agent that sends none). It returns
// what it found, or "" when nothing else uses the name.
//
// Series carrying another cluster ID, or the name in a partition label the
// collector doesn't write, belong to someone else. Series with only
// k8s_cluster=name look the same from the collector's own gateway and from
// another cluster, so their Pods' nodes are compared with the nodes the
// collector's agents run on: another cluster's Pods run on nodes this
// collector has never reported.
func nameUsedElsewhere(ctx context.Context, t prom.Transport, name, uid string) (string, error) {
	qv := strconv.Quote(name)
	group := strings.Join(append([]string{IDLabel, collectorUIDLabel}, PartitionLabels...), ", ")
	queries := make([]string, 0, len(nameEvidenceMetrics)+2)
	for _, metric := range nameEvidenceMetrics {
		parts := make([]string, 0, len(PartitionLabels))
		for _, l := range PartitionLabels {
			parts = append(parts, fmt.Sprintf(`count by (%s) (last_over_time(%s{%s=%s}[%s]))`, group, metric, l, qv, nameHistory))
		}
		queries = append(queries, strings.Join(parts, " or "))
	}
	agentSelector := fmt.Sprintf(`k8s_pod_cpu_time{%s=%s}`, collectorUIDLabel, strconv.Quote(uid))
	if uid == "" {
		agentSelector = fmt.Sprintf(`k8s_pod_cpu_time{k8s_cluster=%s}`, qv)
	}
	queries = append(queries,
		fmt.Sprintf(`count by (node) (last_over_time(kube_pod_info{k8s_cluster=%s,node!=""}[%s]))`, qv, nameHistory),
		fmt.Sprintf(`count by (k8s_node_name) (last_over_time(%s[%s]))`, agentSelector, nameHistory),
	)
	results := make([][]prom.Series, len(queries))
	errs := make([]error, len(queries))
	var wg sync.WaitGroup
	for i, q := range queries {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i], errs[i] = query(ctx, t, q)
		}()
	}
	wg.Wait()
	if err := errors.Join(errs...); err != nil {
		return "", err
	}

	for i, metric := range nameEvidenceMetrics {
		for _, s := range results[i] {
			if id := s.Labels[IDLabel]; id != "" && id != uid {
				return fmt.Sprintf("%s carries %s=%q", metric, IDLabel, id), nil
			}
			if id := s.Labels[collectorUIDLabel]; id != "" && id != uid {
				return fmt.Sprintf("%s carries %s=%q", metric, collectorUIDLabel, id), nil
			}
			for _, l := range PartitionLabels {
				if l != collectorClusterLabel && s.Labels[l] == name {
					return fmt.Sprintf("%s has %s=%q", metric, l, name), nil
				}
			}
		}
	}

	podNodes, agentNodes := results[len(results)-2], results[len(results)-1]
	if len(podNodes) > maxNameEvidenceNodes || len(agentNodes) > maxNameEvidenceNodes {
		return fmt.Sprintf("more than %d nodes report under the name, too many to compare", maxNameEvidenceNodes), nil
	}
	own := make(map[string]bool, len(agentNodes))
	for _, s := range agentNodes {
		own[s.Labels["k8s_node_name"]] = true
	}
	var foreign []string
	for _, s := range podNodes {
		if n := s.Labels["node"]; n != "" && !own[n] {
			foreign = append(foreign, n)
		}
	}
	if len(foreign) > 0 {
		sort.Strings(foreign)
		return fmt.Sprintf("kube_pod_info lists Pods on node %s, which this cluster's collector has never reported", foreign[0]), nil
	}
	return "", nil
}

// collectorNameUsage maps each collector cluster name to the k8s_cluster_uid
// values (including "" for agents that send none) seen with it over
// nameHistory. name, when set, limits the lookup to that cluster name.
func collectorNameUsage(ctx context.Context, t prom.Transport, name string) (map[string]map[string]bool, error) {
	selector := "k8s_pod_cpu_time"
	if name != "" {
		selector = fmt.Sprintf("k8s_pod_cpu_time{k8s_cluster=%s}", strconv.Quote(name))
	}
	series, err := query(ctx, t, fmt.Sprintf(`count by (%s, k8s_cluster) (last_over_time(%s[%s]))`, collectorUIDLabel, selector, nameHistory))
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

// nameElsewhereWarning explains refused name-only metrics when another
// cluster writes the collector's name without a collector of its own.
func nameElsewhereWarning(name, evidence string) string {
	return fmt.Sprintf("Another cluster also wrote metrics under the cluster name %q in the last 7 days (%s). Radar refuses this cluster's kube-state metrics (restarts, HPA, workload ownership) and scraped disk metrics, which carry only the name, until the collector is given a unique cluster name or a radar_cluster_uid label, or the other cluster's data is more than 7 days old.", name, evidence)
}

// sharedNameWarning explains refused name-only metrics when another collector
// sent the same cluster name within nameHistory, now or before (a cluster
// rebuilt under the same name).
func sharedNameWarning(name string) string {
	return fmt.Sprintf("More than one OpenObserve collector sent the cluster name %q in the last 7 days. Radar refuses this cluster's kube-state metrics (restarts, HPA, workload ownership) and scraped disk metrics, which carry only the name, until the collector is given a unique cluster name or a radar_cluster_uid label, or the other collector's data is more than 7 days old.", name)
}

// reservedCollectorNameWarning explains a collector match scoped by ID alone.
func reservedCollectorNameWarning(name string) string {
	return fmt.Sprintf("This cluster's OpenObserve collector sends the cluster name %q, which OpenObserve treats as its dashboard placeholder and never filters on. Radar matches it by cluster ID only and refuses its kube-state metrics (restarts, HPA, workload ownership) and scraped disk metrics, which carry only the name, until the collector is given another cluster name.", name)
}

// collectorMatch builds the ID match of a collector sending name. elsewhere
// is what nameUsedElsewhere found, for a name unique among collectors.
func collectorMatch(uid, name string, usage map[string]map[string]bool, elsewhere string) ClusterMatch {
	if IsReservedValue(name) {
		// The name can't filter, so the cluster ID is the only matcher and
		// nothing that carries only the name is read.
		m := ClusterMatch{
			By:      "id",
			Dialect: DialectCollector,
			Options: RewriteOptions{Matchers: []Matcher{{Name: collectorUIDLabel, Value: uid}}, Dialect: DialectCollector},
			Label:   collectorUIDLabel,
			Value:   uid,
			Warning: reservedCollectorNameWarning(name),
		}
		m.Missing = staticMissing(m.Options)
		return m
	}
	unique := collectorNameUnique(uid, name, usage) && elsewhere == ""
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
		} else if elsewhere != "" {
			m.Warning = nameElsewhereWarning(name, elsewhere)
		} else {
			m.Warning = sharedNameWarning(name)
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
	var reserved *key
	for _, s := range series {
		uid := s.Labels["uid"]
		if !sampleUIDs[uid] {
			continue
		}
		found[uid] = true
		for _, l := range candidates {
			if v := s.Labels[l]; v != "" && IsReservedValue(v) {
				if reserved == nil {
					reserved = &key{l, v}
				}
			} else if v != "" {
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
	if len(coverage) == 0 && reserved != nil {
		return Suggestion{Reason: fmt.Sprintf("%d sampled Pods from namespace %s are stored under %s=%q, which OpenObserve treats as its dashboard placeholder and never filters on, so it can't identify this cluster. Give the cluster another name, or use the cluster ID label below.", len(found), sample.Namespace, reserved.label, reserved.value)}, nil
	}
	if len(coverage) == 0 {
		// OpenObserve strips its HA dedup label (cluster, by default) on
		// remote write, so a setup that set it finds it missing here.
		return Suggestion{Reason: fmt.Sprintf("%d sampled Pods from namespace %s are in OpenObserve, but carry none of the cluster labels %s. If you set a cluster label, OpenObserve removes it on remote write by default. Use the cluster ID label below, or rename yours to cluster_name.", len(found), sample.Namespace, strings.Join(candidates, ", "))}, nil
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
		best.label, ns, best.label, strconv.Quote(best.value), activeWindow))
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
	if IsReservedValue(value) {
		return ClusterMatch{}, ErrReservedName
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
	elsewhere := ""
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
		// The name-only metrics also need the name to be this collector's
		// alone over the span charts read, not only the last 15 minutes.
		usage, err := collectorNameUsage(ctx, dt, value)
		if err != nil {
			return ClusterMatch{}, err
		}
		unique := len(usage[value]) == 1 && usage[value][uid]
		if unique {
			if elsewhere, err = nameUsedElsewhere(ctx, dt, value, uid); err != nil {
				return ClusterMatch{}, err
			}
			unique = elsewhere == ""
		}
		m = ClusterMatch{
			Dialect: DialectCollector,
			Options: RewriteOptions{Matchers: []Matcher{{Name: "k8s_cluster", Value: value}}, Dialect: DialectCollector, CollectorUID: uid, GatewayMetricsAllowed: unique},
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
	if m.Dialect == DialectCollector && m.Options.CollectorUID == "" {
		m.Warning += " " + CollectorNoIDWarning
	}
	if m.Dialect == DialectCollector && !m.Options.GatewayMetricsAllowed {
		if elsewhere != "" {
			m.Warning += " " + nameElsewhereWarning(value, elsewhere)
		} else {
			m.Warning += " " + sharedNameWarning(value)
		}
	}
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
			present, err := hasRecentData(ctx, scoped, familyMetrics[f])
			switch {
			case errors.Is(err, ErrNotCollected):
				missing[i] = true
			case err != nil:
				errs[i] = fmt.Errorf("openobserve: detect %s: %w", f, err)
			default:
				missing[i] = !present
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
// label a metric never had (OpenObserve before v1.2). With a
// *prom.HTTPTransport it reads the store's version; otherwise, or when the
// version can't be read, it filters a metric that has data on a label nobody
// writes. Show VersionWarning when true. DetectStoreBehaviour also says
// whether label lookups can be scoped.
func LegacyFilterBehaviour(ctx context.Context, t prom.Transport) (bool, error) {
	if ht, ok := t.(*prom.HTTPTransport); ok {
		if v, err := FetchVersion(ctx, ht); err == nil {
			return v.Before(1, 2), nil
		}
	}
	return probeLegacyFilter(ctx, t)
}

func probeLegacyFilter(ctx context.Context, t prom.Transport) (bool, error) {
	dt := newDiscoveryTransport(t)
	for _, metric := range legacyProbeMetrics {
		ignored, err := hasRecentData(ctx, dt, metric+`{radar_probe_never_written="x"}`)
		if err != nil {
			return false, err
		}
		if ignored {
			return true, nil
		}
		all, err := hasRecentData(ctx, dt, metric)
		if err != nil {
			return false, err
		}
		if all {
			return false, nil
		}
	}
	return false, ErrNoProbeData
}

// hasRecentData reports whether selector has a sample in the last 15
// minutes. Older OpenObserve versions can answer the instant query empty
// while range queries over the same data, which charts use, answer; a range
// query confirms an empty instant answer.
func hasRecentData(ctx context.Context, t prom.Transport, selector string) (bool, error) {
	series, err := query(ctx, t, fmt.Sprintf(`count(last_over_time(%s[%s]))`, selector, lookback))
	if err != nil {
		return false, err
	}
	if hasPositive(series) {
		return true, nil
	}
	end := time.Now()
	res, err := prom.NewClient(t).QueryRange(ctx, fmt.Sprintf(`count(%s)`, selector), end.Add(-lookbackDuration), end, time.Minute)
	if err != nil {
		return false, err
	}
	return hasPositive(res.Series), nil
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
			if v := s.Labels[l]; v != "" && !IsReservedValue(v) {
				seen[LabelValue{Label: l, Value: v}] = true
			}
		}
	}
	for _, s := range results[1] {
		if v := s.Labels["k8s_cluster"]; v != "" && !IsReservedValue(v) {
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
