package server

import (
	"log"
	"net/http"
	"slices"
	"sort"
	"strings"

	"github.com/go-chi/chi/v5"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/readscope"
	"github.com/skyhook-io/radar/pkg/logging"
)

// The log shipping wire is provider-neutral: a provider's own words (Flow,
// ClusterOutput, …) appear only as a row's kind, so another shipper adds rows,
// not fields.
const providerLoggingOperator = "logging-operator"

type logDestination struct {
	Kind      string `json:"kind"` // Output | ClusterOutput
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name"`
	Type      string `json:"type,omitempty"`
	Found     bool   `json:"found"`
	Active    *bool  `json:"active,omitempty"` // nil: not found, or no state reported
	protected bool
}

type logRoute struct {
	Provider  string `json:"provider"`
	Kind      string `json:"kind"`
	Group     string `json:"group"`
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	Selected  bool   `json:"selected"`
	Uncertain bool   `json:"uncertain"`
	Ignored   bool   `json:"ignored"`
	// Invalid: why the operator can't render the route; empty when valid.
	Invalid string `json:"invalid,omitempty"`
	// RenderBlocked: the route's shipper has an invalid route (or default flow)
	// and doesn't skip invalid ones, so it renders none of its configuration
	// and Radar can't tell what the configuration still running ships.
	RenderBlocked bool `json:"renderBlocked,omitempty"`
	// Fallback: the route receives what no other route of its shipper selects
	// (the Logging's defaultFlow).
	Fallback bool `json:"fallback,omitempty"`
	// Excluded: an exclude rule decided the pod; such a route is not a near miss.
	Excluded bool `json:"excluded"`
	// Delivers says whether a selecting route gets logs anywhere: it isn't
	// reported inactive and a destination it names exists and is active. nil: a
	// destination's state is unknown or its kind couldn't be read.
	Delivers     *bool            `json:"delivers,omitempty"`
	Unmet        []string         `json:"unmet"`
	HasFilters   bool             `json:"hasFilters"`
	Active       *bool            `json:"active,omitempty"`
	Destinations []logDestination `json:"destinations"`
}

type logReadError struct {
	Group    string `json:"group"`
	Resource string `json:"resource"`
}

type logShippingResponse struct {
	// Providers lists the installed shippers Radar understands; empty hides the section.
	Providers []string   `json:"providers"`
	Routes    []logRoute `json:"routes"`
	// Shipped: a counted route selects the pod for certain and delivers.
	Shipped bool `json:"shipped"`
	// Uncertain: nothing selects it for certain, but a route may.
	Uncertain bool `json:"uncertain"`
	// ReadErrors names the kinds Radar could not read; the UI never says
	// "not shipped" while it is non-empty.
	ReadErrors []logReadError `json:"readErrors"`
	// ExcludeAnnotation names the pod's annotation asking the collector to
	// drop its logs; while set, Radar never says the pod is shipped.
	ExcludeAnnotation string `json:"excludeAnnotation,omitempty"`
}

// fluent-bit drops the logs of a pod with this annotation set to "true" while
// its K8S-Logging.Exclude is on, the Logging operator's default.
const fluentbitExcludeAnnotation = "fluentbit.io/exclude"

// handleLoggingPod answers which Logging operator Flows forward one pod's
// logs, and where to. Gate: the caller must be able to get the pod (namespace
// access plus get pods). The Flows, Loggings and outputs are read with Radar's
// identity and projected to names, the selection decision and each output's
// kind/name/type/active state. Only the routes that decide the caller's pod
// leave the server (those that select, may select or exclude it, invalid ones
// that would select it, and the closest miss), so a caller who can't list
// Flows learns their names but not the rest of the cluster's routing; the
// closest miss's namespace and node conditions are withheld unless the caller
// may list its kind. No output configuration ever leaves the server. On a
// shared listener without auth it reads as Radar's identity; a cross-site
// request is refused by requireSameOrigin.
func (s *Server) handleLoggingPod(w http.ResponseWriter, r *http.Request) {
	ns, name := chi.URLParam(r, "namespace"), chi.URLParam(r, "name")
	if !s.requireConnected(w) {
		return
	}
	if readscope.NoNamespaceAccess(s.getUserNamespaces(r, []string{ns})) || !s.canRead(r, "", "pods", ns, "get") {
		s.writeError(w, http.StatusForbidden, "no access to pod "+ns+"/"+name)
		return
	}
	resp := logShippingResponse{Providers: []string{}, Routes: []logRoute{}, ReadErrors: []logReadError{}}
	discovery := k8s.GetResourceDiscovery()
	if discovery == nil {
		s.writeError(w, http.StatusServiceUnavailable, "API discovery not ready")
		return
	}
	if _, ok := discovery.GetGVRWithGroup("loggings", logging.Group); !ok {
		s.writeJSON(w, resp)
		return
	}
	resp.Providers = append(resp.Providers, providerLoggingOperator)
	cache := k8s.GetResourceCache()
	if cache == nil || cache.Pods() == nil {
		s.writeError(w, http.StatusServiceUnavailable, "Resource cache not available")
		return
	}
	pod, err := cache.Pods().Pods(ns).Get(name)
	if apierrors.IsNotFound(err) {
		s.writeError(w, http.StatusNotFound, "pod "+ns+"/"+name+" not found")
		return
	}
	if err != nil {
		log.Printf("[logging] Failed to get pod %s/%s: %v", ns, name, err)
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	facts := loggingPodFacts(pod)
	if pod.Annotations[fluentbitExcludeAnnotation] == "true" {
		resp.ExcludeAnnotation = fluentbitExcludeAnnotation
	}

	budget := newSyncBudget(r.Context())
	// listDynamicSyncedWithin turns an informer that hasn't synced (a slow
	// apiserver, or a kind Radar may not list) into an error rather than an
	// empty list, which would read as "not shipped".
	read := func(plural, namespace string) []*unstructured.Unstructured {
		items, err := listDynamicSyncedWithin(r.Context(), cache, plural, logging.Group, namespace, budget)
		if err != nil {
			resp.ReadErrors = append(resp.ReadErrors, logReadError{logging.Group, plural})
			return nil
		}
		return items
	}
	// An invalid Flow in any watched namespace stops its Logging rendering, so
	// Flows and Outputs are read everywhere when Radar can; otherwise the pod's
	// namespace is all Radar judges by.
	readWide := func(plural string) ([]*unstructured.Unstructured, bool) {
		if items, err := listDynamicSyncedWithin(r.Context(), cache, plural, logging.Group, "", budget); err == nil {
			return items, true
		}
		return read(plural, ns), false
	}
	var loggings []logging.Logging
	for _, u := range read("loggings", "") {
		loggings = append(loggings, logging.LoggingFrom(u))
	}
	namespacedFlows, _ := readWide("flows")
	flows := append(read("clusterflows", ""), namespacedFlows...)
	namespacedOutputs, outputsEverywhere := readWide("outputs")
	outputs := map[[3]string]logging.OutputSummary{} // kind, namespace, name
	for _, u := range append(read("clusteroutputs", ""), namespacedOutputs...) {
		o := logging.OutputFrom(u)
		outputs[[3]string{u.GetKind(), o.Namespace, o.Name}] = o
	}
	outputsUnread := map[string]bool{
		"ClusterOutput": slices.Contains(resp.ReadErrors, logReadError{logging.Group, "clusteroutputs"}),
		"Output":        slices.Contains(resp.ReadErrors, logReadError{logging.Group, "outputs"}),
	}
	// Unread Loggings: whether the operator counts or watches a Flow is unknown.
	loggingsUnread := slices.Contains(resp.ReadErrors, logReadError{logging.Group, "loggings"})
	// Where a Logging's ClusterOutputs are looked up; "" is every namespace.
	clusterOutputNamespace := map[string]string{}
	for _, l := range loggings {
		clusterOutputNamespace[l.LoggingRef] = l.ControlNamespace
		if l.ClusterResourcesFromAllNamespaces {
			clusterOutputNamespace[l.LoggingRef] = ""
		}
	}
	// Per loggingRef: whether a counted Flow selects the pod for certain, or
	// may, and whether an invalid route stops the Logging rendering.
	selectedBy, maybeBy, blocked := map[string]bool{}, map[string]bool{}, map[string]bool{}
	skipInvalid := map[string]bool{}
	for _, l := range loggings {
		skipInvalid[l.LoggingRef] = l.SkipInvalidResources
		if l.DefaultFlow != nil {
			for _, ref := range l.DefaultFlow.GlobalOutputs {
				if d := outputRef(outputs, "ClusterOutput", clusterOutputNamespace[l.LoggingRef], l.LoggingRef, ref); !d.Found && !outputsUnread["ClusterOutput"] {
					blocked[l.LoggingRef] = true
				}
			}
		}
	}

	type candidate struct {
		row        logRoute
		loggingRef string
		relevant   bool // selects, may select or excludes the pod, or would if valid
	}
	var candidates []candidate
	for _, u := range flows {
		f := logging.FlowFrom(u)
		d := logging.Selects(f, facts)
		watched, watchKnown := logging.Watched(f, loggings)
		if loggingsUnread {
			watched, watchKnown = false, false
		}
		counted := !loggingsUnread && logging.Counted(f, loggings) && (watched || !watchKnown)
		row := logRoute{
			Provider: providerLoggingOperator, Kind: u.GetKind(), Group: logging.Group, Namespace: f.Namespace, Name: f.Name,
			Ignored: f.Invalid != "" || (!loggingsUnread && (!logging.Counted(f, loggings) || (!watched && watchKnown))), Invalid: f.Invalid,
			Unmet: d.Unmet, HasFilters: f.HasFilters, Destinations: []logDestination{},
		}
		for _, ref := range f.GlobalOutputs {
			row.Destinations = append(row.Destinations, outputRef(outputs, "ClusterOutput", clusterOutputNamespace[f.LoggingRef], f.LoggingRef, ref))
		}
		for _, ref := range f.LocalOutputs {
			row.Destinations = append(row.Destinations, outputRef(outputs, "Output", f.Namespace, f.LoggingRef, ref))
		}
		unread := outputsUnread
		if !outputsEverywhere && f.Namespace != ns {
			unread = map[string]bool{"ClusterOutput": outputsUnread["ClusterOutput"], "Output": true}
		}
		if reason := missingOutput(row.Destinations, unread, f.Cluster); row.Invalid == "" && reason != "" {
			row.Invalid, row.Ignored = reason, true
		}
		if row.Invalid != "" && counted && !skipInvalid[f.LoggingRef] {
			blocked[f.LoggingRef] = true
		}
		if !logging.InScope(f, ns) {
			continue
		}
		if row.Unmet == nil {
			row.Unmet = []string{}
		}
		// An ignored Flow forwards nothing, whatever its rules say. A Flow only a
		// namespace selector (not evaluated) could admit may at most be selecting.
		row.Selected, row.Uncertain = d.Selected && !row.Ignored, d.Uncertain && !row.Ignored
		if !watched && !watchKnown && row.Selected {
			row.Selected, row.Uncertain = false, true
		}
		if active, found, _ := unstructured.NestedBool(u.Object, "status", "active"); found {
			row.Active = &active
		}
		row.Excluded = !row.Ignored && d.Rule >= 0 && !d.Selected && !d.Uncertain
		candidates = append(candidates, candidate{row, f.LoggingRef, d.Selected || d.Uncertain || row.Excluded})
	}
	for i := range candidates {
		row, ref := &candidates[i].row, candidates[i].loggingRef
		if !row.Ignored || row.Invalid != "" {
			row.RenderBlocked = blocked[ref]
		}
		if row.RenderBlocked && row.Selected {
			row.Selected, row.Uncertain = false, true
		}
		if row.Selected {
			row.Delivers = loggingDelivers(*row, outputsUnread)
		}
		if !row.Ignored {
			selectedBy[ref] = selectedBy[ref] || row.Selected
			maybeBy[ref] = maybeBy[ref] || row.Uncertain
		}
	}
	// The defaultFlow receives what no Flow of its Logging selects. Unread Flows
	// leave open whether one does.
	flowsUnread := slices.Contains(resp.ReadErrors, logReadError{logging.Group, "flows"}) || slices.Contains(resp.ReadErrors, logReadError{logging.Group, "clusterflows"})
	for _, l := range loggings {
		if l.DefaultFlow == nil || selectedBy[l.LoggingRef] {
			continue
		}
		row := logRoute{
			Provider: providerLoggingOperator, Kind: "Logging", Group: logging.Group, Name: l.Name, Fallback: true,
			Unmet: []string{}, HasFilters: l.DefaultFlow.HasFilters, Destinations: []logDestination{},
		}
		row.RenderBlocked = blocked[l.LoggingRef]
		row.Uncertain = maybeBy[l.LoggingRef] || flowsUnread || row.RenderBlocked
		row.Selected = !row.Uncertain
		for _, ref := range l.DefaultFlow.GlobalOutputs {
			row.Destinations = append(row.Destinations, outputRef(outputs, "ClusterOutput", clusterOutputNamespace[l.LoggingRef], l.LoggingRef, ref))
		}
		if row.Selected {
			row.Delivers = loggingDelivers(row, outputsUnread)
		}
		candidates = append(candidates, candidate{row, l.LoggingRef, true})
	}
	for _, c := range candidates {
		if row := c.row; !row.Ignored {
			resp.Shipped = resp.Shipped || (row.Delivers != nil && *row.Delivers)
			resp.Uncertain = resp.Uncertain || row.Uncertain || (row.Selected && row.Delivers == nil)
		}
	}
	if resp.Shipped {
		resp.Uncertain = false
	}
	if resp.ExcludeAnnotation != "" && resp.Shipped {
		resp.Shipped, resp.Uncertain = false, true
	}
	// The closest miss: what the UI explains when nothing selects the pod.
	closest := -1
	for i, c := range candidates {
		row := c.row
		if row.Ignored || row.Selected || row.Uncertain || row.Excluded || row.Fallback {
			continue
		}
		if closest < 0 || len(row.Unmet) < len(candidates[closest].row.Unmet) ||
			(len(row.Unmet) == len(candidates[closest].row.Unmet) && row.Name < candidates[closest].row.Name) {
			closest = i
		}
	}
	for i, c := range candidates {
		row := c.row
		switch {
		case i == closest:
			if !s.canRead(r, logging.Group, strings.ToLower(row.Kind)+"s", row.Namespace, "list") {
				row.Unmet = withholdPlacement(row.Unmet)
			}
		case row.Invalid != "" && (row.Kind == "Flow" || c.relevant):
		case row.Ignored || !c.relevant && !row.Fallback:
			continue
		}
		resp.Routes = append(resp.Routes, row)
	}
	sort.SliceStable(resp.Routes, func(i, j int) bool {
		a, b := loggingFlowRank(resp.Routes[i]), loggingFlowRank(resp.Routes[j])
		if a != b {
			return a < b
		}
		return resp.Routes[i].Name < resp.Routes[j].Name
	})
	s.writeJSON(w, resp)
}

// withholdPlacement replaces the namespace and node conditions a route lacks,
// which name other namespaces and nodes, with one neutral term.
func withholdPlacement(unmet []string) []string {
	out := []string{}
	withheld := false
	for _, u := range unmet {
		if strings.HasPrefix(u, "namespace in ") || strings.HasPrefix(u, "namespace matching ") || strings.HasPrefix(u, "node in ") {
			withheld = true
			continue
		}
		out = append(out, u)
	}
	if withheld {
		out = append(out, "a namespace or node condition")
	}
	return out
}

func loggingPodFacts(pod *corev1.Pod) logging.PodFacts {
	facts := logging.PodFacts{Namespace: pod.Namespace, Labels: pod.Labels, Node: pod.Spec.NodeName}
	for _, c := range append(pod.Spec.InitContainers, pod.Spec.Containers...) {
		facts.Containers = append(facts.Containers, c.Name)
	}
	for _, c := range pod.Spec.EphemeralContainers {
		facts.Containers = append(facts.Containers, c.Name)
	}
	return facts
}

// outputRef finds a named output carrying the Flow's loggingRef, as the
// operator resolves refs only among its Logging's outputs. A ClusterOutput with
// namespace "" may be in any namespace; like the operator, the first by
// namespace wins.
func outputRef(outputs map[[3]string]logging.OutputSummary, kind, namespace, loggingRef, name string) logDestination {
	ref := logDestination{Kind: kind, Namespace: namespace, Name: name}
	if kind == "ClusterOutput" && namespace == "" {
		for key, o := range outputs {
			if key[0] == kind && key[2] == name && o.LoggingRef == loggingRef && (!ref.Found || o.Namespace < ref.Namespace) {
				ref.Namespace, ref.Type, ref.Found, ref.Active, ref.protected = o.Namespace, o.Type, true, o.Active, o.Protected
			}
		}
		return ref
	}
	if o, ok := outputs[[3]string{kind, namespace, name}]; ok && o.LoggingRef == loggingRef {
		ref.Type, ref.Found, ref.Active, ref.protected = o.Type, true, o.Active, o.Protected
	}
	return ref
}

// missingOutput is why the operator can't render a Flow naming an output it
// can't resolve, or (for a namespaced Flow) a protected ClusterOutput; empty
// when every named output was found and usable, or one wasn't and Radar didn't
// read its kind.
func missingOutput(dests []logDestination, unread map[string]bool, cluster bool) string {
	for _, d := range dests {
		if d.Found && d.protected && !cluster {
			return "it names ClusterOutput " + d.Name + ", which is protected: only ClusterFlows may use it"
		}
		if !d.Found && !unread[d.Kind] {
			return "it names " + d.Kind + " " + d.Name + ", which Radar didn't find for its Logging"
		}
	}
	return ""
}

// loggingDelivers decides whether a selecting Flow's logs reach an output:
// false when the operator reports the Flow inactive or no named output is
// found and active; nil when an output's state is unknown or its kind wasn't
// read, so "missing" can't be told from "unread".
func loggingDelivers(row logRoute, outputsUnread map[string]bool) *bool {
	yes, no := true, false
	if row.Active != nil && !*row.Active {
		return &no
	}
	unknown := false
	for _, o := range row.Destinations {
		switch {
		case o.Found && o.Active != nil && *o.Active:
			return &yes
		case o.Found && o.Active == nil, !o.Found && outputsUnread[o.Kind]:
			unknown = true
		}
	}
	if unknown {
		return nil
	}
	return &no
}

func loggingFlowRank(f logRoute) int {
	switch {
	case f.Ignored:
		return 3
	case f.Selected:
		return 0
	case f.Uncertain:
		return 1
	}
	return 2
}
