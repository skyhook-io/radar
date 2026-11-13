package logging

import (
	"fmt"
	"reflect"
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
	if legacy.Cluster || !reflect.DeepEqual(legacy.LegacySelector, map[string]string{"app": "x"}) || !reflect.DeepEqual(legacy.LocalOutputs, []string{"out", "old"}) || legacy.LoggingRef != "second" {
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

func TestOutputFrom(t *testing.T) {
	out := OutputFrom(obj("ClusterOutput", "logging-system", "loki", map[string]any{
		"loggingRef": "x",
		"loki":       map[string]any{"url": "https://user:pw@loki.example/loki/api/v1/push?x=1", "password": map[string]any{"value": "s3cret"}},
	}, map[string]any{"active": true, "problemsCount": int64(1), "problems": []any{"secret not found"}}))
	if out.Type != "loki" || out.Address != "https://loki.example/loki/api/v1/push" || !out.Active || !out.Cluster || out.ProblemsCount != 1 || !reflect.DeepEqual(out.Problems, []string{"secret not found"}) {
		t.Fatalf("output = %+v", out)
	}
	if s := fmt.Sprintf("%+v", out); strings.Contains(s, "s3cret") || strings.Contains(s, "user:pw") || strings.Contains(s, "x=1") {
		t.Fatalf("output summary leaks configuration: %s", s)
	}
	s3 := OutputFrom(obj("Output", "team", "archive", map[string]any{"protected": true, "s3": map[string]any{"s3_bucket": "b"}}, nil))
	if s3.Type != "s3" || s3.Address != "" || s3.Cluster || s3.Active {
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
