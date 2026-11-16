package logging

import (
	"slices"
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
	f.LocalOutputs, _, _ = unstructured.NestedStringSlice(u.Object, "spec", "localOutputRefs")
	f.DeprecatedOutputs, _, _ = unstructured.NestedStringSlice(u.Object, "spec", "outputRefs")
	filters, _, _ := unstructured.NestedSlice(u.Object, "spec", "filters")
	f.HasFilters = len(filters) > 0
	match, _, _ := unstructured.NestedSlice(u.Object, "spec", "match")
	spec, _, _ := unstructured.NestedMap(u.Object, "spec")
	f.MatchSet = spec["match"] != nil
	if f.MatchSet && spec["selectors"] != nil {
		f.Invalid = "match and selectors are both set"
	}
	for _, raw := range match {
		entry, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if entry["select"] != nil && entry["exclude"] != nil {
			f.Invalid = "a match entry sets both select and exclude"
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
			r.NamespacesRegex, _, _ = unstructured.NestedStringSlice(cond, "namespaces_regex")
			r.NamespaceLabels, _, _ = unstructured.NestedStringMap(cond, "namespace_labels")
			f.Match = append(f.Match, r)
		}
	}
	return f
}

type Logging struct {
	Name, LoggingRef, ControlNamespace string
	WatchNamespaces                    []string // spec.watchNamespaces; empty: all
	// WatchSelector: spec.watchNamespaceSelector is set and not empty (not
	// evaluated). An empty selector matches every namespace.
	WatchSelector bool
	// ResolvedWatchNamespaces is status.watchNamespaces, the namespaces the
	// operator resolved ("*" for all); nil when it reported none.
	ResolvedWatchNamespaces []string
	// SkipInvalidResources: spec.skipInvalidResources. Off (the default), one
	// invalid Flow or ClusterFlow stops the operator rendering the Logging's
	// configuration at all.
	SkipInvalidResources bool
	// ClusterResourcesFromAllNamespaces: spec.allowClusterResourcesFromAllNamespaces,
	// ClusterFlows and ClusterOutputs count from any namespace.
	ClusterResourcesFromAllNamespaces bool
	DefaultFlow                       *DefaultFlow // spec.defaultFlow
	// CollectorNamespace: spec.fluentBitAgentNamespace, where the fluent-bit
	// DaemonSets run; the control namespace when unset.
	CollectorNamespace string
	// Detached aggregator configs the operator bound (status.fluentdConfigName,
	// status.syslogNGConfigName); they own the aggregator StatefulSet.
	FluentdConfigName, SyslogNGConfigName string
}

// DefaultFlow receives what no Flow of its Logging selects.
type DefaultFlow struct {
	GlobalOutputs     []string // spec.defaultFlow.globalOutputRefs
	DeprecatedOutputs []string // spec.defaultFlow.outputRefs, which the operator doesn't render
	HasFilters        bool
}

func LoggingFrom(u *unstructured.Unstructured) Logging {
	l := Logging{Name: u.GetName()}
	l.LoggingRef, _, _ = unstructured.NestedString(u.Object, "spec", "loggingRef")
	l.ControlNamespace, _, _ = unstructured.NestedString(u.Object, "spec", "controlNamespace")
	l.WatchNamespaces, _, _ = unstructured.NestedStringSlice(u.Object, "spec", "watchNamespaces")
	selector, found, _ := unstructured.NestedMap(u.Object, "spec", "watchNamespaceSelector")
	if found && len(selector) == 0 {
		l.WatchNamespaces = nil
	}
	l.WatchSelector = len(selector) > 0
	l.ResolvedWatchNamespaces, _, _ = unstructured.NestedStringSlice(u.Object, "status", "watchNamespaces")
	l.SkipInvalidResources, _, _ = unstructured.NestedBool(u.Object, "spec", "skipInvalidResources")
	l.ClusterResourcesFromAllNamespaces, _, _ = unstructured.NestedBool(u.Object, "spec", "allowClusterResourcesFromAllNamespaces")
	l.CollectorNamespace, _, _ = unstructured.NestedString(u.Object, "spec", "fluentBitAgentNamespace")
	if l.CollectorNamespace == "" {
		l.CollectorNamespace = l.ControlNamespace
	}
	l.FluentdConfigName, _, _ = unstructured.NestedString(u.Object, "status", "fluentdConfigName")
	l.SyslogNGConfigName, _, _ = unstructured.NestedString(u.Object, "status", "syslogNGConfigName")
	if df, found, _ := unstructured.NestedMap(u.Object, "spec", "defaultFlow"); found && df != nil {
		d := &DefaultFlow{}
		d.GlobalOutputs, _, _ = unstructured.NestedStringSlice(df, "globalOutputRefs")
		d.DeprecatedOutputs, _, _ = unstructured.NestedStringSlice(df, "outputRefs")
		filters, _, _ := unstructured.NestedSlice(df, "filters")
		d.HasFilters = len(filters) > 0
		l.DefaultFlow = d
	}
	return l
}

// Counted reports whether the operator processes the flow: a Logging with the
// flow's loggingRef exists and, for a ClusterFlow, the flow lives in that
// Logging's control namespace or the Logging takes cluster resources from
// every namespace.
func Counted(flow Flow, loggings []Logging) bool {
	for _, l := range loggings {
		if l.LoggingRef == flow.LoggingRef && (!flow.Cluster || l.ClusterResourcesFromAllNamespaces || flow.Namespace == l.ControlNamespace) {
			return true
		}
	}
	return false
}

// Watched reports whether the flow's Logging watches the flow's namespace
// (status.watchNamespaces when the operator reported it, otherwise
// spec.watchNamespaces and spec.watchNamespaceSelector); a ClusterFlow is
// always watched. certain is false when only the namespace selector, which Radar
// doesn't evaluate, could admit it.
func Watched(flow Flow, loggings []Logging) (watched, certain bool) {
	if flow.Cluster {
		return true, true
	}
	for _, l := range loggings {
		if l.LoggingRef != flow.LoggingRef {
			continue
		}
		if len(l.ResolvedWatchNamespaces) > 0 {
			return slices.Contains(l.ResolvedWatchNamespaces, "*") || slices.Contains(l.ResolvedWatchNamespaces, flow.Namespace), true
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
	LoggingRef                     string // the operator only resolves a ref to an output with the Flow's loggingRef
	Cluster                        bool
	// Protected: a ClusterOutput only ClusterFlows may use; a Flow naming it is invalid.
	Protected     bool
	Active        *bool // nil: the operator reported no state
	ProblemsCount int
	Problems      []string
}

var outputCommonKeys = map[string]bool{"loggingRef": true, "protected": true, "enabledNamespaces": true}

func OutputFrom(u *unstructured.Unstructured) OutputSummary {
	out := OutputSummary{Name: u.GetName(), Namespace: u.GetNamespace(), Cluster: u.GetKind() == "ClusterOutput"}
	spec, _, _ := unstructured.NestedMap(u.Object, "spec")
	out.LoggingRef, _ = spec["loggingRef"].(string)
	out.Protected, _ = spec["protected"].(bool)
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
	if active, found, _ := unstructured.NestedBool(u.Object, "status", "active"); found {
		out.Active = &active
	}
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
