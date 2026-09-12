package prom

import (
	"strings"
	"testing"
	"time"
)

func TestWorkloadMetricsScope(t *testing.T) {
	for _, scope := range []WorkloadMetricsScope{
		{},
		{SingleCluster: true, ClusterLabels: map[string]string{"cluster": "a"}},
		{ClusterLabels: map[string]string{"cluster": ""}},
		{ClusterLabels: map[string]string{"cluster=~": "a.*"}},
		{ClusterLabels: map[string]string{"__name__": "up"}},
	} {
		if _, err := scope.Matchers(); err == nil {
			t.Errorf("accepted unsafe scope: %+v", scope)
		}
	}
	got, err := (WorkloadMetricsScope{ClusterLabels: map[string]string{"region": "west", "cluster": `a"b`}}).Matchers()
	if err != nil || got != `cluster="a\"b",region="west"` {
		t.Fatalf("exact escaped matchers: %q, %v", got, err)
	}
	if got, err := (WorkloadMetricsScope{SingleCluster: true}).Matchers(); err != nil || got != "" {
		t.Fatalf("explicit isolated endpoint: %q, %v", got, err)
	}
}

func TestThrottleQueryScope(t *testing.T) {
	scope := WorkloadMetricsScope{ClusterLabels: map[string]string{"cluster": "east"}}
	query, err := BuildThrottleQuery(time.Minute, SelectPods("shop", []string{"api-0", "api-1"}), scope)
	if err != nil {
		t.Fatal(err)
	}
	for _, matcher := range []string{`cluster="east"`, `namespace="shop"`, `pod=~'^(api-0|api-1)$'`, `container!='POD'`} {
		if strings.Count(query, matcher) != 2 {
			t.Errorf("scope %q must constrain both counters: %s", matcher, query)
		}
	}
	if strings.Contains(query, "or vector(0)") || !strings.HasPrefix(query, "100 *") {
		t.Fatalf("must preserve missing counters and return percent: %s", query)
	}
	if _, err := BuildThrottleQuery(time.Minute, SelectPods("shop", nil), scope); err == nil {
		t.Fatal("empty population must not query the namespace")
	}
}
