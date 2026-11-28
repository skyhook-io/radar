package logging

import (
	"fmt"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func obj(kind, ns, name string, spec, status map[string]any) *unstructured.Unstructured {
	o := map[string]any{"apiVersion": Group + "/v1beta1", "kind": kind, "metadata": map[string]any{"name": name}, "spec": spec}
	if ns != "" {
		o["metadata"].(map[string]any)["namespace"] = ns
	}
	if status != nil {
		o["status"] = status
	}
	return &unstructured.Unstructured{Object: o}
}

func TestFlowFromAndCounted(t *testing.T) {
	optin := FlowFrom(obj("ClusterFlow", "logging-system", "optin", map[string]any{
		"match":            []any{map[string]any{"select": map[string]any{"labels": map[string]any{"example.com/loki-logs": "true"}, "container_names": []any{"app"}, "hosts": []any{"n1"}, "namespaces": []any{"x"}, "namespace_labels": map[string]any{"team": "t"}}}, map[string]any{"exclude": map[string]any{"labels": map[string]any{"tier": "debug"}}}},
		"filters":          []any{map[string]any{"dedot": map[string]any{}}},
		"globalOutputRefs": []any{"loki"},
	}, nil))
	want := []Rule{
		{Labels: map[string]string{"example.com/loki-logs": "true"}, ContainerNames: []string{"app"}, Hosts: []string{"n1"}, Namespaces: []string{"x"}, NamespaceLabels: map[string]string{"team": "t"}},
		{Exclude: true, Labels: map[string]string{"tier": "debug"}},
	}
	if !optin.Cluster || optin.Namespace != "logging-system" || optin.Name != "optin" || !optin.HasFilters || !reflect.DeepEqual(optin.GlobalOutputs, []string{"loki"}) || !reflect.DeepEqual(optin.Match, want) {
		t.Fatalf("cluster flow = %+v", optin)
	}
	legacy := FlowFrom(obj("Flow", "team", "legacy", map[string]any{"selectors": map[string]any{"app": "x"}, "localOutputRefs": []any{"out"}, "outputRefs": []any{"old"}, "loggingRef": "second"}, nil))
	// The operator renders only globalOutputRefs and localOutputRefs.
	if legacy.Cluster || !reflect.DeepEqual(legacy.LegacySelector, map[string]string{"app": "x"}) || !reflect.DeepEqual(legacy.LocalOutputs, []string{"out"}) || !reflect.DeepEqual(legacy.DeprecatedOutputs, []string{"old"}) || legacy.LoggingRef != "second" {
		t.Fatalf("legacy flow = %+v", legacy)
	}

	loggings := []Logging{
		LoggingFrom(obj("Logging", "", "default", map[string]any{"controlNamespace": "logging-system"}, nil)),
		LoggingFrom(obj("Logging", "", "second", map[string]any{"loggingRef": "second", "controlNamespace": "other"}, nil)),
	}
	stray := FlowFrom(obj("ClusterFlow", "team", "stray", map[string]any{}, nil))
	orphan := FlowFrom(obj("Flow", "team", "orphan", map[string]any{"loggingRef": "nope"}, nil))
	for _, c := range []struct {
		name string
		flow Flow
		want bool
	}{{"cluster flow in control namespace", optin, true}, {"cluster flow elsewhere", stray, false}, {"flow of a second Logging", legacy, true}, {"flow of a missing Logging", orphan, false}} {
		if got := Counted(c.flow, loggings); got != c.want {
			t.Errorf("Counted(%s) = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestFlowFromNamespacesRegexAndInvalid(t *testing.T) {
	f := FlowFrom(obj("ClusterFlow", "logging-system", "re", map[string]any{"match": []any{map[string]any{"select": map[string]any{"namespaces_regex": []any{"^team-"}}}}}, nil))
	if f.Invalid != "" || len(f.Match) != 1 || !reflect.DeepEqual(f.Match[0].NamespacesRegex, []string{"^team-"}) {
		t.Fatalf("flow = %+v", f)
	}
	for name, spec := range map[string]map[string]any{
		"match and selectors":             {"match": []any{}, "selectors": map[string]any{"app": "x"}},
		"select and exclude in one entry": {"match": []any{map[string]any{"select": map[string]any{}, "exclude": map[string]any{"labels": map[string]any{"a": "b"}}}}},
	} {
		if f := FlowFrom(obj("Flow", "team", "bad", spec, nil)); f.Invalid == "" {
			t.Errorf("%s: Invalid is empty", name)
		}
	}
}

// The operator tells "match: []" (a route with no match rules, which the
// label router never selects) from no match key (select everything).
func TestFlowFromMatchSet(t *testing.T) {
	if f := FlowFrom(obj("Flow", "team", "empty", map[string]any{"match": []any{}}, nil)); !f.MatchSet {
		t.Errorf("match: [] → MatchSet false")
	}
	if f := FlowFrom(obj("Flow", "team", "none", map[string]any{}, nil)); f.MatchSet {
		t.Errorf("no match key → MatchSet true")
	}
}

func TestOutputFromLoggingRef(t *testing.T) {
	if o := OutputFrom(obj("Output", "team", "out", map[string]any{"loggingRef": "second", "loki": map[string]any{}}, nil)); o.LoggingRef != "second" {
		t.Errorf("LoggingRef = %q", o.LoggingRef)
	}
}

func TestLoggingFromDefaultFlowAndClusterResources(t *testing.T) {
	l := LoggingFrom(obj("Logging", "", "default", map[string]any{
		"controlNamespace":                       "logging-system",
		"allowClusterResourcesFromAllNamespaces": true,
		"defaultFlow":                            map[string]any{"globalOutputRefs": []any{"loki"}, "outputRefs": []any{"old"}},
	}, nil))
	if !l.ClusterResourcesFromAllNamespaces || l.DefaultFlow == nil || !reflect.DeepEqual(l.DefaultFlow.GlobalOutputs, []string{"loki"}) || !reflect.DeepEqual(l.DefaultFlow.DeprecatedOutputs, []string{"old"}) {
		t.Fatalf("logging = %+v", l)
	}
	stray := FlowFrom(obj("ClusterFlow", "team", "stray", map[string]any{}, nil))
	if !Counted(stray, []Logging{l}) {
		t.Error("a ClusterFlow outside the control namespace counts when the Logging allows cluster resources from all namespaces")
	}
	if l := LoggingFrom(obj("Logging", "", "x", map[string]any{"controlNamespace": "a"}, nil)); l.DefaultFlow != nil {
		t.Errorf("no defaultFlow: %+v", l.DefaultFlow)
	}
}

func TestOutputFrom(t *testing.T) {
	out := OutputFrom(obj("ClusterOutput", "logging-system", "loki", map[string]any{
		"loggingRef": "x",
		"loki":       map[string]any{"url": "https://user:pw@loki.example/loki/api/v1/push?x=1", "password": map[string]any{"value": "s3cret"}},
	}, map[string]any{"active": true, "problemsCount": int64(1), "problems": []any{"secret not found"}}))
	if out.Type != "loki" || out.Address != "https://loki.example/loki/api/v1/push" || out.Active == nil || !*out.Active || !out.Cluster || out.ProblemsCount != 1 || !reflect.DeepEqual(out.Problems, []string{"secret not found"}) {
		t.Fatalf("output = %+v", out)
	}
	if s := fmt.Sprintf("%+v", out); strings.Contains(s, "s3cret") || strings.Contains(s, "user:pw") || strings.Contains(s, "x=1") {
		t.Fatalf("output summary leaks configuration: %s", s)
	}
	s3 := OutputFrom(obj("Output", "team", "archive", map[string]any{"protected": true, "s3": map[string]any{"s3_bucket": "b"}}, nil))
	if s3.Type != "s3" || s3.Address != "" || s3.Cluster || s3.Active != nil {
		t.Fatalf("s3 output = %+v", s3)
	}
}

func TestWatched(t *testing.T) {
	loggings := []Logging{
		LoggingFrom(obj("Logging", "", "listed", map[string]any{"controlNamespace": "logging", "watchNamespaces": []any{"team"}}, nil)),
		LoggingFrom(obj("Logging", "", "selected", map[string]any{"loggingRef": "sel", "controlNamespace": "logging2", "watchNamespaceSelector": map[string]any{"matchLabels": map[string]any{"logs": "on"}}}, nil)),
	}
	for _, c := range []struct {
		name             string
		flow             Flow
		watched, certain bool
	}{
		{"flow in a watched namespace", Flow{Namespace: "team"}, true, true},
		{"flow outside watchNamespaces", Flow{Namespace: "other"}, false, true},
		{"cluster flow is always watched", Flow{Namespace: "logging", Cluster: true}, true, true},
		{"selector can't be evaluated", Flow{Namespace: "other", LoggingRef: "sel"}, false, false},
	} {
		if w, sure := Watched(c.flow, loggings); w != c.watched || sure != c.certain {
			t.Errorf("%s: Watched = %v,%v want %v,%v", c.name, w, sure, c.watched, c.certain)
		}
	}
}

// A host without a scheme reads to url.Parse as "scheme:opaque", so userinfo
// in it survives prom.SafeAddress; it must not survive here.
func TestOutputFromRedactsSchemelessCredentials(t *testing.T) {
	for raw, want := range map[string]string{
		"elastic:changeme@es.local:9200": "[redacted]",
		"es.local:9200?token=abc":        "[redacted]",
		"es.local:9200":                  "es.local:9200",
	} {
		out := OutputFrom(obj("Output", "team", "es", map[string]any{"elasticsearch": map[string]any{"host": raw}}, nil))
		if out.Address != want {
			t.Errorf("host %q → %q, want %q", raw, out.Address, want)
		}
	}
}

// The operator watches every namespace when watchNamespaceSelector is present
// but empty, and the namespaces it resolved are in status.watchNamespaces
// ("*" for all), which wins over Radar's reading of the spec.
func TestWatchedSelectorAndStatus(t *testing.T) {
	for _, c := range []struct {
		name             string
		spec, status     map[string]any
		watched, certain bool
	}{
		{"empty selector watches all", map[string]any{"watchNamespaces": []any{"a"}, "watchNamespaceSelector": map[string]any{}}, nil, true, true},
		{"status lists the namespace", map[string]any{"watchNamespaceSelector": map[string]any{"matchLabels": map[string]any{"logs": "on"}}}, map[string]any{"watchNamespaces": []any{"a", "team"}}, true, true},
		{"status omits the namespace", map[string]any{"watchNamespaceSelector": map[string]any{"matchLabels": map[string]any{"logs": "on"}}}, map[string]any{"watchNamespaces": []any{"a"}}, false, true},
		{"status says all", map[string]any{"watchNamespaces": []any{"a"}}, map[string]any{"watchNamespaces": []any{"*"}}, true, true},
	} {
		l := LoggingFrom(obj("Logging", "", "l", c.spec, c.status))
		if w, sure := Watched(Flow{Namespace: "team"}, []Logging{l}); w != c.watched || sure != c.certain {
			t.Errorf("%s: Watched = %v,%v want %v,%v", c.name, w, sure, c.watched, c.certain)
		}
	}
}

func TestLoggingAndOutputFlags(t *testing.T) {
	if l := LoggingFrom(obj("Logging", "", "l", map[string]any{"skipInvalidResources": true}, nil)); !l.SkipInvalidResources {
		t.Error("skipInvalidResources not read")
	}
	if o := OutputFrom(obj("ClusterOutput", "logging", "audit", map[string]any{"protected": true, "loki": map[string]any{}}, nil)); !o.Protected || o.Type != "loki" {
		t.Errorf("protected output = %+v", o)
	}
}

// FlowFrom compiles a rule's namespaces_regex once, so matching many pods
// against it compiles nothing, and decides as an uncompiled rule would.
func TestFlowFromCompilesNamespaceRegexOnce(t *testing.T) {
	compiles := 0
	orig := compileRegex
	compileRegex = func(p string) (*regexp.Regexp, error) { compiles++; return orig(p) }
	defer func() { compileRegex = orig }()
	f := FlowFrom(obj("ClusterFlow", "logging", "re", map[string]any{"match": []any{
		map[string]any{"exclude": map[string]any{"namespaces_regex": []any{"(?<=x)kube"}}},
		map[string]any{"select": map[string]any{"namespaces_regex": []any{"^team-", "^prod"}}},
	}}, nil))
	parsed := compiles
	if parsed != 3 {
		t.Fatalf("FlowFrom compiled %d patterns, want 3", parsed)
	}
	for _, ns := range []string{"team-a", "prod", "other", "kube"} {
		pod := PodFacts{Namespace: ns, Containers: []string{"app"}}
		got := Selects(f, pod)
		uncompiled := f
		uncompiled.Match = []Rule{{Exclude: true, NamespacesRegex: f.Match[0].NamespacesRegex}, {NamespacesRegex: f.Match[1].NamespacesRegex}}
		if want := Selects(uncompiled, pod); !reflect.DeepEqual(got, want) {
			t.Errorf("%s: compiled %+v, uncompiled %+v", ns, got, want)
		}
	}
	compiles = parsed
	for range 100 {
		Selects(f, PodFacts{Namespace: "team-a", Containers: []string{"app"}})
	}
	if compiles != parsed {
		t.Fatalf("matching compiled %d more patterns", compiles-parsed)
	}
}
