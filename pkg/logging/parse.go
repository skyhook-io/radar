package logging

import (
	"sort"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/skyhook-io/radar/pkg/prom"
)

// FlowFrom reads a Flow or ClusterFlow; malformed entries are skipped.
func FlowFrom(u *unstructured.Unstructured) Flow {
	f := Flow{Namespace: u.GetNamespace(), Name: u.GetName(), Cluster: u.GetKind() == "ClusterFlow"}
	f.LoggingRef, _, _ = unstructured.NestedString(u.Object, "spec", "loggingRef")
	f.LegacySelector, _, _ = unstructured.NestedStringMap(u.Object, "spec", "selectors")
	f.GlobalOutputs, _, _ = unstructured.NestedStringSlice(u.Object, "spec", "globalOutputRefs")
	local, _, _ := unstructured.NestedStringSlice(u.Object, "spec", "localOutputRefs")
	legacy, _, _ := unstructured.NestedStringSlice(u.Object, "spec", "outputRefs")
	f.LocalOutputs = append(local, legacy...)
	filters, _, _ := unstructured.NestedSlice(u.Object, "spec", "filters")
	f.HasFilters = len(filters) > 0
	match, _, _ := unstructured.NestedSlice(u.Object, "spec", "match")
	for _, raw := range match {
		entry, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		for _, key := range []string{"select", "exclude"} {
			cond, ok := entry[key].(map[string]any)
			if !ok {
				continue
			}
			r := Rule{Exclude: key == "exclude"}
			r.Labels, _, _ = unstructured.NestedStringMap(cond, "labels")
			r.ContainerNames, _, _ = unstructured.NestedStringSlice(cond, "container_names")
			r.Hosts, _, _ = unstructured.NestedStringSlice(cond, "hosts")
			r.Namespaces, _, _ = unstructured.NestedStringSlice(cond, "namespaces")
			r.NamespaceLabels, _, _ = unstructured.NestedStringMap(cond, "namespace_labels")
			f.Match = append(f.Match, r)
		}
	}
	return f
}

type Logging struct {
	Name, LoggingRef, ControlNamespace string
	WatchNamespaces                    []string // spec.watchNamespaces; empty: all
	WatchSelector                      bool     // spec.watchNamespaceSelector is set (not evaluated)
}

func LoggingFrom(u *unstructured.Unstructured) Logging {
	l := Logging{Name: u.GetName()}
	l.LoggingRef, _, _ = unstructured.NestedString(u.Object, "spec", "loggingRef")
	l.ControlNamespace, _, _ = unstructured.NestedString(u.Object, "spec", "controlNamespace")
	l.WatchNamespaces, _, _ = unstructured.NestedStringSlice(u.Object, "spec", "watchNamespaces")
	selector, _, _ := unstructured.NestedMap(u.Object, "spec", "watchNamespaceSelector")
	l.WatchSelector = len(selector) > 0
	return l
}

// Counted reports whether the operator processes the flow: a Logging with the
// flow's loggingRef exists and, for a ClusterFlow, the flow lives in that
// Logging's control namespace.
func Counted(flow Flow, loggings []Logging) bool {
	for _, l := range loggings {
		if l.LoggingRef == flow.LoggingRef && (!flow.Cluster || flow.Namespace == l.ControlNamespace) {
			return true
		}
	}
	return false
}

// Watched reports whether the flow's Logging watches the flow's namespace
// (spec.watchNamespaces, spec.watchNamespaceSelector); a ClusterFlow is always
// watched. certain is false when only the namespace selector, which Radar
// doesn't evaluate, could admit it.
func Watched(flow Flow, loggings []Logging) (watched, certain bool) {
	if flow.Cluster {
		return true, true
	}
	for _, l := range loggings {
		if l.LoggingRef != flow.LoggingRef {
			continue
		}
		if len(l.WatchNamespaces) == 0 && !l.WatchSelector {
			return true, true
		}
		for _, ns := range l.WatchNamespaces {
			if ns == flow.Namespace {
				return true, true
			}
		}
		return false, !l.WatchSelector
	}
	return false, true
}

// OutputSummary is what may leave the server about an Output: its type and a
// redacted address, never the rest of its configuration.
type OutputSummary struct {
	Name, Namespace, Type, Address string
	Cluster, Active                bool
	ProblemsCount                  int
	Problems                       []string
}

var outputCommonKeys = map[string]bool{"loggingRef": true, "protected": true, "enabledNamespaces": true}

func OutputFrom(u *unstructured.Unstructured) OutputSummary {
	out := OutputSummary{Name: u.GetName(), Namespace: u.GetNamespace(), Cluster: u.GetKind() == "ClusterOutput"}
	spec, _, _ := unstructured.NestedMap(u.Object, "spec")
	var types []string
	for k := range spec {
		if !outputCommonKeys[k] {
			types = append(types, k)
		}
	}
	sort.Strings(types)
	if len(types) > 0 {
		out.Type = types[0]
		if block, ok := spec[out.Type].(map[string]any); ok {
			for _, key := range []string{"url", "endpoint", "host"} {
				if s, ok := block[key].(string); ok && s != "" {
					out.Address = safeAddress(s)
					break
				}
			}
		}
	}
	out.Active, _, _ = unstructured.NestedBool(u.Object, "status", "active")
	count, _, _ := unstructured.NestedInt64(u.Object, "status", "problemsCount")
	out.ProblemsCount = int(count)
	out.Problems, _, _ = unstructured.NestedStringSlice(u.Object, "status", "problems")
	return out
}

// safeAddress is prom.SafeAddress for a value that may lack a scheme: url.Parse
// reads "user:pw@host:9200" as scheme "user" with an opaque rest, so its
// userinfo would survive. Without "://", anything that could carry a
// credential is withheld whole. The UI's safeAddress applies the same rule.
func safeAddress(raw string) string {
	if !strings.Contains(raw, "://") {
		if strings.ContainsAny(raw, "@?#") {
			return "[redacted]"
		}
		return raw
	}
	return prom.SafeAddress(raw)
}
