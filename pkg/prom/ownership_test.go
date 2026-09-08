package prom

import (
	"strings"
	"testing"
	"time"
)

func TestOwnerHopsFollowKubeStateMetricsLabels(t *testing.T) {
	for kind, want := range map[string]OwnerHop{
		"Deployment":  {Direct: "ReplicaSet", HopMetric: "kube_replicaset_owner", HopLabel: "replicaset"},
		"rollouts":    {Direct: "ReplicaSet", HopMetric: "kube_replicaset_owner", HopLabel: "replicaset"},
		"CronJob":     {Direct: "Job", HopMetric: "kube_job_owner", HopLabel: "job_name"},
		"statefulset": {Direct: "StatefulSet"},
		"DaemonSet":   {Direct: "DaemonSet"},
		"replicaset":  {Direct: "ReplicaSet"},
		"job":         {Direct: "Job"},
		"Workflow":    {Direct: "Workflow"},
	} {
		got, ok := OwnerHopFor(kind)
		if !ok || got != want {
			t.Errorf("%s: hop = %+v (%v), want %+v", kind, got, ok, want)
		}
	}
	if _, ok := OwnerHopFor("Service"); ok {
		t.Error("Service has no pod owner hop")
	}
}

func TestOwnershipVectorKeepsBothEdgesInTheQuery(t *testing.T) {
	// A Deployment's pods are named through the ReplicaSets kube-state-metrics
	// attributes to it, so a ReplicaSet adopted by another Deployment stops
	// contributing at the step the adoption happened rather than for the whole
	// window.
	got := OwnershipVector(WorkloadRef{Kind: "Deployment", Namespace: "shop", Name: "api"}, time.Hour)
	for _, want := range []string{
		`kube_pod_owner{namespace='shop',owner_kind='ReplicaSet',owner_is_controller='true'}[1h]`,
		`kube_replicaset_owner{namespace='shop',owner_kind='Deployment',owner_name='api',owner_is_controller='true'}[1h]`,
		`label_replace(`,
		`* on (replicaset) group_left()`,
		`max by (namespace,pod)`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("deployment ownership vector missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "owner_name=~") {
		t.Errorf("the hop must stay in the query, not be flattened to owner names:\n%s", got)
	}

	cron := OwnershipVector(WorkloadRef{Kind: "cronjob", Namespace: "shop", Name: "nightly"}, 6*time.Hour)
	if !strings.Contains(cron, "kube_job_owner{namespace='shop',owner_kind='CronJob',owner_name='nightly'") ||
		!strings.Contains(cron, "* on (job_name) group_left()") {
		t.Errorf("cronjob ownership vector:\n%s", cron)
	}

	direct := OwnershipVector(WorkloadRef{Kind: "StatefulSet", Namespace: "shop", Name: "db"}, time.Hour)
	want := `max by (namespace,pod) (last_over_time(kube_pod_owner{namespace='shop',owner_kind='StatefulSet',owner_name='db',owner_is_controller='true'}[1h]))`
	if direct != want {
		t.Errorf("statefulset ownership vector:\n got %s\nwant %s", direct, want)
	}
	if OwnershipVector(WorkloadRef{Kind: "Service", Namespace: "shop", Name: "api"}, time.Hour) != "" {
		t.Error("a Service owns no pods")
	}
}

func TestBuildOwnedPodsProbeCountsTheOwnershipVector(t *testing.T) {
	ref := WorkloadRef{Kind: "StatefulSet", Namespace: "shop", Name: "db"}
	got := BuildOwnedPodsProbe(ref, 2*time.Hour)
	if !strings.HasPrefix(got, "count(") || !strings.Contains(got, "[2h]") {
		t.Fatalf("probe: %s", got)
	}
	if BuildOwnedPodsProbe(WorkloadRef{Kind: "Service", Namespace: "shop", Name: "api"}, time.Hour) != "" {
		t.Fatal("no ownership vector means no probe")
	}
}

func TestBuildScopedQueryJoinsOwnershipAfterTheRangeFunction(t *testing.T) {
	sel := SelectOwner("shop", "StatefulSet", "db")
	owner := func(window string) string {
		return `max by (namespace,pod) (last_over_time(kube_pod_owner{namespace='shop',owner_kind='StatefulSet',owner_name='db',owner_is_controller='true'}[` + window + `]))`
	}
	cases := map[string]struct{ got, want string }{
		"restarts per pod": {
			BuildScopedQuery(sel, CategoryRestarts, AggregatePerPod, true),
			`sum by (pod,namespace) (changes(kube_pod_container_status_restarts_total{namespace='shop'}[1h]) * on (namespace,pod) group_left() ` + owner("1h") + `)`,
		},
		"restarts total": {
			BuildScopedQuery(sel, CategoryRestarts, AggregateTotal, true),
			`sum(round(increase(kube_pod_container_status_restarts_total{namespace='shop'}[1h]) * on (namespace,pod) group_left() ` + owner("1h") + `))`,
		},
		"cpu per pod": {
			BuildScopedQuery(sel, CategoryCPU, AggregatePerPod, true),
			`sum(rate(container_cpu_usage_seconds_total{container!='',namespace='shop'}[5m]) * on (namespace,pod) group_left() ` + owner("5m") + `) by (pod,namespace)`,
		},
		"memory total": {
			BuildScopedQuery(sel, CategoryMemory, AggregateTotal, false),
			`sum(max by (pod,namespace,container) (container_memory_working_set_bytes{namespace='shop'}) * on (namespace,pod) group_left() ` + owner("5m") + `)`,
		},
		"network rx per pod": {
			BuildScopedQuery(sel, CategoryNetworkRX, AggregatePerPod, true),
			`sum(rate(container_network_receive_bytes_total{namespace='shop'}[5m]) * on (namespace,pod) group_left() ` + owner("5m") + `) by (pod,namespace)`,
		},
	}
	for name, tc := range cases {
		if tc.got != tc.want {
			t.Errorf("%s:\n got %s\nwant %s", name, tc.got, tc.want)
		}
	}
}

// The ownership lookback must cover what the category's function reads, or a
// pod replaced inside the window loses the tail of its own rate.
func TestBuildScopedQueryOwnershipLookbackCoversTheFunctionRange(t *testing.T) {
	sel := SelectOwner("shop", "StatefulSet", "db")
	if q := BuildScopedQuery(sel, CategoryRestarts, AggregateTotal, true); !strings.Contains(q, "[1h])") || !strings.Contains(q, "[1h]))") {
		t.Errorf("restarts reads a 1h counter increase, so ownership must look back 1h: %s", q)
	}
	for _, cat := range []MetricCategory{CategoryCPU, CategoryNetworkRX, CategoryNetworkTX, CategoryFilesystem} {
		q := BuildScopedQuery(sel, cat, AggregateTotal, true)
		if !strings.Contains(q, "[5m]") || strings.Contains(q, "[1h]") {
			t.Errorf("%s reads a 5m rate, so ownership must look back 5m: %s", cat, q)
		}
	}
}

func TestBuildScopedQueryCurrentPodsMatchExactly(t *testing.T) {
	sel := SelectPods("shop", []string{"api-7f6-b", "api-7f6-a"})
	got := BuildScopedQuery(sel, CategoryCPU, AggregatePerPod, true)
	want := `sum(rate(container_cpu_usage_seconds_total{container!='',namespace='shop',pod=~'^(api-7f6-a|api-7f6-b)$'}[5m])) by (pod,namespace)`
	if got != want {
		t.Fatalf("cpu per pod:\n got %s\nwant %s", got, want)
	}
	if strings.Contains(got, "api-.*") || strings.Contains(got, "kube_pod_owner") {
		t.Fatalf("current-pods form must neither prefix-match nor join: %s", got)
	}
	escaped := BuildScopedQuery(SelectPods("te'st", []string{"a.b", "c{d}"}), CategoryCPU, AggregatePerPod, true)
	if strings.Contains(escaped, "a.b|") || strings.Contains(escaped, "c{d}") {
		t.Fatalf("names must be sanitized and regex-escaped: %s", escaped)
	}
	empty := BuildScopedQuery(PodSelection{Namespace: "shop"}, CategoryMemory, AggregateTotal, true)
	if !strings.Contains(empty, `pod=~'a^'`) {
		t.Fatalf("an empty selection must match nothing, got %s", empty)
	}
	if !(PodSelection{Namespace: "shop"}).IsEmpty() || sel.IsEmpty() || SelectOwner("shop", "StatefulSet", "db").IsEmpty() {
		t.Fatal("IsEmpty disagrees with the selection")
	}
}

func TestBuildQueryNoLongerInfersWorkloadPodsFromNames(t *testing.T) {
	for _, kind := range []string{"Deployment", "StatefulSet", "DaemonSet", "ReplicaSet", "Job", "CronJob"} {
		if q := BuildQuery(kind, "shop", "api", CategoryCPU); q != "" {
			t.Errorf("%s: BuildQuery still builds from the name: %s", kind, q)
		}
	}
	if q := BuildQuery("Pod", "shop", "api-7f6-a", CategoryCPU); !strings.Contains(q, `pod='api-7f6-a'`) {
		t.Errorf("pod query lost its exact match: %s", q)
	}
}
