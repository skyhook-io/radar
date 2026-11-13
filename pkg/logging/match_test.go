package logging

import (
	"reflect"
	"testing"
)

type labels map[string]string
type containers []string
type hosts []string
type namespaces []string
type nsLabels map[string]string

func rule(exclude bool, conds ...any) Rule {
	r := Rule{Exclude: exclude}
	for _, c := range conds {
		switch v := c.(type) {
		case labels:
			r.Labels = v
		case containers:
			r.ContainerNames = v
		case hosts:
			r.Hosts = v
		case namespaces:
			r.Namespaces = v
		case nsLabels:
			r.NamespaceLabels = v
		}
	}
	return r
}

func sel(conds ...any) Rule  { return rule(false, conds...) }
func excl(conds ...any) Rule { return rule(true, conds...) }
func rules(rs ...Rule) Flow  { return Flow{Namespace: "a", Match: rs} }
func clusterRules(rs ...Rule) Flow {
	return Flow{Namespace: "logging", Cluster: true, Match: rs}
}

type pod struct {
	ns         string
	labels     labels
	containers []string
	node       string
}

func (p pod) facts() PodFacts {
	ns := p.ns
	if ns == "" {
		ns = "a"
	}
	cs := p.containers
	if cs == nil {
		cs = []string{"app"}
	}
	return PodFacts{Namespace: ns, Labels: p.labels, Containers: cs, Node: p.node}
}

func TestSelects(t *testing.T) {
	for _, tc := range []struct {
		name string
		flow Flow
		pod  pod
		want Decision
	}{
		{"empty match selects all in scope", Flow{Namespace: "a"}, pod{ns: "a"}, Decision{Selected: true, Rule: -1}},
		{"namespaced flow never selects another namespace", Flow{Namespace: "a"}, pod{ns: "b"}, Decision{Rule: -1}},
		{"cluster flow selects any namespace", Flow{Namespace: "logging", Cluster: true}, pod{ns: "b"}, Decision{Selected: true, Rule: -1}},
		{"label select", rules(sel(labels{"example.com/loki-logs": "true"})), pod{labels: labels{"example.com/loki-logs": "true"}}, Decision{Selected: true, Rule: 0}},
		{"label missing names the unmet condition", rules(sel(labels{"example.com/loki-logs": "true"})), pod{}, Decision{Rule: -1, Unmet: []string{"label example.com/loki-logs=true"}}},
		{"container name select needs one container", rules(sel(containers{"postgres"})), pod{containers: []string{"bootstrap", "postgres"}}, Decision{Selected: true, Rule: 0}},
		{"first matching rule wins: exclude before select", rules(excl(labels{"tier": "debug"}), sel()), pod{labels: labels{"tier": "debug"}}, Decision{Rule: 0}},
		{"exclude not matching falls through to select", rules(excl(labels{"tier": "debug"}), sel()), pod{}, Decision{Selected: true, Rule: 1}},
		{"hosts", rules(sel(hosts{"node-1"})), pod{node: "node-2"}, Decision{Rule: -1, Unmet: []string{"node in [node-1]"}}},
		{"cluster flow namespaces", clusterRules(sel(namespaces{"x"})), pod{ns: "y"}, Decision{Rule: -1, Unmet: []string{"namespace in [x]"}}},
		{"namespace_labels deciding is uncertain", clusterRules(sel(labels{"app": "a"}, nsLabels{"team": "t"})), pod{labels: labels{"app": "a"}}, Decision{Uncertain: true, Rule: 0}},
		{"namespace_labels with an unmet condition does not decide", clusterRules(sel(labels{"app": "a"}, nsLabels{"team": "t"}), sel()), pod{}, Decision{Selected: true, Rule: 1}},
		{"legacy selectors when match empty", Flow{Namespace: "a", LegacySelector: map[string]string{"app": "x"}}, pod{ns: "a", labels: labels{"app": "x"}}, Decision{Selected: true, Rule: -1}},
		{"legacy selector unmet", Flow{Namespace: "a", LegacySelector: map[string]string{"app": "x"}}, pod{ns: "a"}, Decision{Rule: -1, Unmet: []string{"label app=x"}}},
		{"legacy ignored when match present", Flow{Namespace: "a", LegacySelector: map[string]string{"app": "x"}, Match: []Rule{sel(labels{"app": "y"})}}, pod{ns: "a", labels: labels{"app": "x"}}, Decision{Rule: -1, Unmet: []string{"label app=y"}}},
		{"nearest select rule is the one with fewest unmet", rules(sel(labels{"a": "1", "b": "2"}), sel(labels{"c": "3", "d": "4"})), pod{labels: labels{"a": "1"}}, Decision{Rule: -1, Unmet: []string{"label b=2"}}},
		{"container-scoped exclude leaves other containers selected", rules(excl(containers{"istio-proxy"}), sel()), pod{containers: []string{"app", "istio-proxy"}}, Decision{Selected: true, Rule: 1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := Selects(tc.flow, tc.pod.facts())
			if len(got.Unmet) == 0 {
				got.Unmet = nil
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("Selects = %+v, want %+v", got, tc.want)
			}
		})
	}
}
