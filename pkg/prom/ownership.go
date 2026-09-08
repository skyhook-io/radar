package prom

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// Pod membership for a workload's metrics is never inferred from a pod's
// name: a Deployment called api must not pick up api-worker pods. A caller
// establishes membership from one of two sources and passes it here as a
// PodSelection.
//
//   - Pods: the pods the workload controls right now, resolved from the
//     cluster by controller ownership. Rendered as pod=~'^(a|b)$'.
//   - Owner: the pods kube-state-metrics attributed to a direct controller
//     over time, rendered as a join on kube_pod_owner. This also covers pods
//     that were replaced during the window. The direct owner is the
//     controller that appears in the pod's ownerReference: the ReplicaSets of
//     a Deployment or Rollout, the Jobs of a CronJob, or the workload itself.
type PodSelection struct {
	Namespace string
	Pods      []string
	Owner     *WorkloadRef
}

// WorkloadRef identifies the workload whose pods a query covers.
// kube-state-metrics records ownership one edge at a time, so a Deployment
// or Rollout reaches its pods through the ReplicaSets it owns and a CronJob
// through its Jobs. Both edges are kept and joined inside the query rather
// than resolved to a list of names first: a ReplicaSet adopted by another
// Deployment mid-window then contributes only for the steps it actually
// belonged to this one.
type WorkloadRef struct {
	Kind      string
	Namespace string
	Name      string
}

// Aggregate says whether a scoped query keeps one series per pod (the
// workload page's charts, whose legends and per-pod overlays need them) or
// sums the pods into one series (the diagnose vitals).
type Aggregate int

const (
	AggregatePerPod Aggregate = iota
	AggregateTotal
)

// IsEmpty reports a selection that can name no pod. Callers should not query
// with it; a query built from it matches nothing.
func (s PodSelection) IsEmpty() bool {
	return len(s.Pods) == 0 && s.Owner == nil
}

// SelectPods is the current-pods form of a selection.
func SelectPods(namespace string, pods []string) PodSelection {
	names := append([]string(nil), pods...)
	sort.Strings(names)
	return PodSelection{Namespace: namespace, Pods: names}
}

// SelectOwner is the ownership-history form of a selection.
func SelectOwner(namespace, kind, name string) PodSelection {
	return PodSelection{
		Namespace: namespace,
		Owner:     &WorkloadRef{Kind: kind, Namespace: namespace, Name: name},
	}
}

// OwnerHop describes how a workload kind reaches the controllers that own
// its pods. Direct is the owner_kind kube_pod_owner records for the pods;
// HopMetric, when set, is the kube-state-metrics series that names those
// direct owners for the workload (kube_replicaset_owner for a Deployment,
// kube_job_owner for a CronJob) and HopLabel is the label on it carrying the
// direct owner's name.
type OwnerHop struct {
	Direct    string
	HopMetric string
	HopLabel  string
}

// OwnerHopFor returns the hop for a workload kind (singular or plural, any
// case), or false for kinds whose pods kube-state-metrics does not attribute.
func OwnerHopFor(kind string) (OwnerHop, bool) {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "deployment", "deployments":
		return OwnerHop{Direct: "ReplicaSet", HopMetric: "kube_replicaset_owner", HopLabel: "replicaset"}, true
	case "rollout", "rollouts":
		return OwnerHop{Direct: "ReplicaSet", HopMetric: "kube_replicaset_owner", HopLabel: "replicaset"}, true
	case "cronjob", "cronjobs":
		return OwnerHop{Direct: "Job", HopMetric: "kube_job_owner", HopLabel: "job_name"}, true
	case "statefulset", "statefulsets":
		return OwnerHop{Direct: "StatefulSet"}, true
	case "daemonset", "daemonsets":
		return OwnerHop{Direct: "DaemonSet"}, true
	case "replicaset", "replicasets":
		return OwnerHop{Direct: "ReplicaSet"}, true
	case "job", "jobs":
		return OwnerHop{Direct: "Job"}, true
	case "workflow", "workflows":
		return OwnerHop{Direct: "Workflow"}, true
	default:
		return OwnerHop{}, false
	}
}

// OwnerKindFor is the ownerReference Kind kube-state-metrics records for
// the workload itself (owner_kind="Deployment" on kube_replicaset_owner).
func OwnerKindFor(kind string) string {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "deployment", "deployments":
		return "Deployment"
	case "rollout", "rollouts":
		return "Rollout"
	case "cronjob", "cronjobs":
		return "CronJob"
	case "statefulset", "statefulsets":
		return "StatefulSet"
	case "daemonset", "daemonsets":
		return "DaemonSet"
	case "replicaset", "replicasets":
		return "ReplicaSet"
	case "job", "jobs":
		return "Job"
	case "workflow", "workflows":
		return "Workflow"
	default:
		return ""
	}
}

// OwnershipVector is the kube-state-metrics expression naming the pods a
// workload owned during the lookback, as a (namespace, pod) vector. For a
// Deployment or Rollout it joins kube_pod_owner's ReplicaSets to the
// ReplicaSets kube_replicaset_owner attributes to the workload; for a
// CronJob it joins through kube_job_owner's Jobs; for the rest it reads
// kube_pod_owner directly. Every selector carries its own range so a pod or
// an ownership edge that ended inside the lookback still counts, and both
// edges are evaluated at each step rather than frozen into a name list.
// Empty for kinds kube-state-metrics does not attribute.
func OwnershipVector(ref WorkloadRef, lookback time.Duration) string {
	hop, ok := OwnerHopFor(ref.Kind)
	if !ok {
		return ""
	}
	ns := SanitizeLabelValue(ref.Namespace)
	name := SanitizeLabelValue(ref.Name)
	window := promDuration(lookback)
	if hop.HopMetric == "" {
		return fmt.Sprintf(
			`max by (namespace,pod) (last_over_time(kube_pod_owner{namespace='%s',owner_kind='%s',owner_name='%s',owner_is_controller='true'}[%s]))`,
			ns, hop.Direct, name, window)
	}
	// kube_pod_owner names the direct owner in owner_name; the hop series
	// names it in its own label, so relabel one side to join them.
	pods := fmt.Sprintf(
		`label_replace(max by (namespace,pod,owner_name) (last_over_time(kube_pod_owner{namespace='%s',owner_kind='%s',owner_is_controller='true'}[%s])), '%s', '$1', 'owner_name', '(.*)')`,
		ns, hop.Direct, window, hop.HopLabel)
	owners := fmt.Sprintf(
		`max by (%s) (last_over_time(%s{namespace='%s',owner_kind='%s',owner_name='%s',owner_is_controller='true'}[%s]))`,
		hop.HopLabel, hop.HopMetric, ns, OwnerKindFor(ref.Kind), name, window)
	return fmt.Sprintf(`max by (namespace,pod) (%s * on (%s) group_left() %s)`, pods, hop.HopLabel, owners)
}

// BuildOwnedPodsProbe counts the pods kube-state-metrics attributed to the
// workload during the lookback. Zero means it has no ownership history for
// this workload in that window, so the caller charts the pods it can see now.
func BuildOwnedPodsProbe(ref WorkloadRef, lookback time.Duration) string {
	vector := OwnershipVector(ref, lookback)
	if vector == "" {
		return ""
	}
	return fmt.Sprintf(`count(%s)`, vector)
}

func exactSetPattern(names []string) string {
	escaped := make([]string, 0, len(names))
	for _, name := range names {
		escaped = append(escaped, EscapeRegexMeta(SanitizeLabelValue(name)))
	}
	return "^(" + strings.Join(escaped, "|") + ")$"
}

func promDuration(d time.Duration) string {
	if d <= 0 {
		d = 5 * time.Minute
	}
	seconds := int64(d / time.Second)
	switch {
	case seconds%86400 == 0:
		return fmt.Sprintf("%dd", seconds/86400)
	case seconds%3600 == 0:
		return fmt.Sprintf("%dh", seconds/3600)
	case seconds%60 == 0:
		return fmt.Sprintf("%dm", seconds/60)
	default:
		return fmt.Sprintf("%ds", seconds)
	}
}

// ownerLookback is how long an ownership sample keeps attributing a pod
// after kube-state-metrics stopped reporting it. It must be at least the
// range the category's function reads, or a replaced pod's trailing
// increase() or rate() would be dropped by staleness while its samples still
// feed the function; for instant gauges five minutes matches Prometheus'
// default lookback. Range vectors ignore stale markers, so this is a grace
// policy, not an exact staleness equivalence.
func ownerLookback(category MetricCategory) time.Duration {
	if category == CategoryRestarts {
		return time.Hour
	}
	return 5 * time.Minute
}

// scopeClause renders the selection into a label matcher on the left side
// of a query and, for the ownership form, the join appended after the
// function that reads the series.
func scopeClause(sel PodSelection, category MetricCategory) (podMatcher string, join string) {
	switch {
	case len(sel.Pods) > 0:
		return fmt.Sprintf(",pod=~'%s'", exactSetPattern(sel.Pods)), ""
	case sel.Owner != nil:
		return "", " * on (namespace,pod) group_left() " + OwnershipVector(*sel.Owner, ownerLookback(category))
	default:
		return ",pod=~'a^'", ""
	}
}

// BuildScopedQuery builds a workload category over one selection. The
// per-pod form keeps one series per pod for the workload page; the total
// form sums them for the diagnose vitals. filterContainer drops the
// container!=” matcher for clusters whose cAdvisor lacks the label.
func BuildScopedQuery(sel PodSelection, category MetricCategory, agg Aggregate, filterContainer bool) string {
	ns := SanitizeLabelValue(sel.Namespace)
	pods, join := scopeClause(sel, category)
	cf := ""
	if filterContainer {
		cf = "container!='',"
	}
	rate := func(metric string) string {
		return fmt.Sprintf(`rate(%s{%snamespace='%s'%s}[5m])%s`, metric, cf, ns, pods, join)
	}
	rateNoCF := func(metric string) string {
		return fmt.Sprintf(`rate(%s{namespace='%s'%s}[5m])%s`, metric, ns, pods, join)
	}
	switch category {
	case CategoryRestarts:
		if agg == AggregateTotal {
			// The total answers "how many restarts did this workload take":
			// increase() reads the counter delta, so several restarts between
			// two scrapes count as several. Its extrapolation yields
			// fractions, which round() settles per pod before summing.
			return fmt.Sprintf(`sum(round(increase(kube_pod_container_status_restarts_total{namespace='%s'%s}[1h])%s))`, ns, pods, join)
		}
		// The per-pod chart answers "when did this pod restart": changes()
		// counts the transitions the samples show, which is what puts a step
		// on the line at the moment it happened. On a fast crashloop it reads
		// lower than the total above, which is the honest difference between
		// counting events and counting restarts.
		return fmt.Sprintf(`sum by (pod,namespace) (changes(kube_pod_container_status_restarts_total{namespace='%s'%s}[1h])%s)`, ns, pods, join)
	case CategoryCPU:
		return wrap(agg, rate("container_cpu_usage_seconds_total"))
	case CategoryMemory:
		inner := fmt.Sprintf(`max by (pod,namespace,container) (container_memory_working_set_bytes{%snamespace='%s'%s})%s`, cf, ns, pods, join)
		if agg == AggregateTotal {
			return fmt.Sprintf(`sum(%s)`, inner)
		}
		return fmt.Sprintf(`sum by (pod,namespace) (%s)`, inner)
	case CategoryNetworkRX:
		return wrap(agg, rateNoCF("container_network_receive_bytes_total"))
	case CategoryNetworkTX:
		return wrap(agg, rateNoCF("container_network_transmit_bytes_total"))
	case CategoryFilesystem:
		return wrap(agg, rateNoCF("container_fs_writes_bytes_total")+" + "+rateNoCF("container_fs_reads_bytes_total"))
	default:
		return ""
	}
}

func wrap(agg Aggregate, expr string) string {
	if agg == AggregateTotal {
		return fmt.Sprintf(`sum(%s)`, expr)
	}
	return fmt.Sprintf(`sum(%s) by (pod,namespace)`, expr)
}
