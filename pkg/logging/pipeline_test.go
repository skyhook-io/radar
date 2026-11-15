package logging

import (
	"reflect"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestInstanceWorkloads(t *testing.T) {
	l := Logging{Name: "demo"}
	agents := []Agent{{Name: "demo"}, {Name: "other-agent", LoggingRef: "other"}}
	ws := []Workload{
		{Kind: "DaemonSet", Namespace: "logging", Name: "demo-fluentbit", OwnerKind: "FluentbitAgent", OwnerName: "demo", Ready: 3, Desired: 3},
		{Kind: "StatefulSet", Namespace: "logging", Name: "demo-fluentd", OwnerKind: "Logging", OwnerName: "demo", Ready: 1, Desired: 1},
		{Kind: "DaemonSet", Namespace: "logging", Name: "inline-fluentbit", OwnerKind: "Logging", OwnerName: "demo"},
		{Kind: "DaemonSet", Namespace: "logging", Name: "foreign", OwnerKind: "FluentbitAgent", OwnerName: "other-agent"},
		{Kind: "StatefulSet", Namespace: "logging", Name: "elsewhere", OwnerKind: "Logging", OwnerName: "second"},
	}
	collectors, aggregators := InstanceWorkloads(l, agents, ws)
	if names(collectors) != "demo-fluentbit,inline-fluentbit" || names(aggregators) != "demo-fluentd" {
		t.Fatalf("collectors=%s aggregators=%s", names(collectors), names(aggregators))
	}
}

// A detached FluentdConfig or SyslogNGConfig owns the aggregator instead of
// the Logging; the Logging's status names it.
func TestInstanceWorkloadsDetachedAggregator(t *testing.T) {
	l := LoggingFrom(&unstructured.Unstructured{Object: map[string]any{
		"kind": "Logging", "metadata": map[string]any{"name": "demo"},
		"spec":   map[string]any{"controlNamespace": "logging", "fluentBitAgentNamespace": "agents"},
		"status": map[string]any{"fluentdConfigName": "demo-fluentd", "syslogNGConfigName": "demo-syslog"},
	}})
	if l.CollectorNamespace != "agents" {
		t.Errorf("CollectorNamespace = %q", l.CollectorNamespace)
	}
	if def := LoggingFrom(&unstructured.Unstructured{Object: map[string]any{"spec": map[string]any{"controlNamespace": "logging"}}}); def.CollectorNamespace != "logging" {
		t.Errorf("default CollectorNamespace = %q", def.CollectorNamespace)
	}
	ws := []Workload{
		{Kind: "StatefulSet", Name: "fd", OwnerKind: "FluentdConfig", OwnerName: "demo-fluentd"},
		{Kind: "StatefulSet", Name: "sng", OwnerKind: "SyslogNGConfig", OwnerName: "demo-syslog"},
		{Kind: "StatefulSet", Name: "other", OwnerKind: "FluentdConfig", OwnerName: "someone-else"},
	}
	if _, aggregators := InstanceWorkloads(l, nil, ws); names(aggregators) != "fd,sng" {
		t.Fatalf("aggregators = %s", names(aggregators))
	}
}

func names(ws []Workload) string {
	s := ""
	for i, w := range ws {
		if i > 0 {
			s += ","
		}
		s += w.Name
	}
	return s
}

func TestCountPods(t *testing.T) {
	optin := Flow{Namespace: "logging", Cluster: true, Match: []Rule{{Labels: map[string]string{"logs": "on"}}}}
	local := Flow{Namespace: "a", Match: []Rule{{Labels: map[string]string{"app": "x"}}}}
	stray := Flow{Namespace: "x", Cluster: true}
	pods := []PodFacts{
		{Namespace: "a", Labels: map[string]string{"logs": "on", "app": "x"}, Containers: []string{"c"}},
		{Namespace: "b", Labels: map[string]string{"logs": "on"}, Containers: []string{"c"}},
		{Namespace: "b", Labels: map[string]string{"app": "x"}, Containers: []string{"c"}},
	}
	counts, unrouted, uncertain := CountPods([]Candidate{{Flow: optin}, {Flow: local}, {Flow: stray, Ignored: true}}, pods)
	want := []RouteCount{{Count: 2}, {Count: 1}, {}}
	if !reflect.DeepEqual(counts, want) || len(unrouted) != 1 || unrouted[0].Namespace != "b" || uncertain {
		t.Fatalf("counts=%+v unrouted=%+v uncertain=%v", counts, unrouted, uncertain)
	}
}

func TestPipelineUncertainIsLowerBound(t *testing.T) {
	byNsLabel := Flow{Namespace: "logging", Cluster: true, Match: []Rule{{NamespaceLabels: map[string]string{"team": "t"}}}}
	watchUnknown := Flow{Namespace: "b", Match: []Rule{{Labels: map[string]string{"app": "x"}}}}
	pods := []PodFacts{
		{Namespace: "a", Containers: []string{"c"}},
		{Namespace: "b", Labels: map[string]string{"app": "x"}, Containers: []string{"c"}},
	}
	counts, unrouted, uncertain := CountPods([]Candidate{{Flow: byNsLabel}, {Flow: watchUnknown, OnlyMaybe: true}}, pods)
	if counts[0] != (RouteCount{Count: 0, LowerBound: true}) || counts[1] != (RouteCount{Count: 0, LowerBound: true}) || len(unrouted) != 0 || !uncertain {
		t.Fatalf("counts=%+v unrouted=%+v uncertain=%v", counts, unrouted, uncertain)
	}
}
