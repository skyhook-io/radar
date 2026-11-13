package server

import (
	"log"
	"net/http"
	"slices"
	"sort"

	"github.com/go-chi/chi/v5"
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
}

// handleLoggingPod answers which Logging operator Flows forward one pod's
// logs, and where to. Gate: the caller must be able to get the pod (namespace
// access plus get pods). The Flows, Loggings and outputs are read with Radar's
// identity and projected to names, the selection decision and each output's
// kind/name/type/active state: the answer is about the caller's own pod, so the
// names that decide it are shown even to a caller who can't list Flows, and no
// output configuration ever leaves the server. On a shared listener without
// auth it reads as Radar's identity; a cross-site request is refused by
// requireSameOrigin.
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
	facts := logging.PodFacts{Namespace: pod.Namespace, Labels: pod.Labels, Node: pod.Spec.NodeName}
	for _, c := range append(pod.Spec.InitContainers, pod.Spec.Containers...) {
		facts.Containers = append(facts.Containers, c.Name)
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
	var loggings []logging.Logging
	for _, u := range read("loggings", "") {
		loggings = append(loggings, logging.LoggingFrom(u))
	}
	flows := append(read("clusterflows", ""), read("flows", ns)...)
	outputs := map[[3]string]logging.OutputSummary{} // kind, namespace, name
	for _, u := range append(read("clusteroutputs", ""), read("outputs", ns)...) {
		o := logging.OutputFrom(u)
		outputs[[3]string{u.GetKind(), o.Namespace, o.Name}] = o
	}
	outputsUnread := map[string]bool{
		"ClusterOutput": slices.Contains(resp.ReadErrors, logReadError{logging.Group, "clusteroutputs"}),
		"Output":        slices.Contains(resp.ReadErrors, logReadError{logging.Group, "outputs"}),
	}
	controlNamespace := map[string]string{}
	for _, l := range loggings {
		controlNamespace[l.LoggingRef] = l.ControlNamespace
	}

	for _, u := range flows {
		f := logging.FlowFrom(u)
		if !logging.InScope(f, ns) {
			continue
		}
		d := logging.Selects(f, facts)
		watched, watchKnown := logging.Watched(f, loggings)
		row := logRoute{
			Provider: providerLoggingOperator, Kind: u.GetKind(), Group: logging.Group, Namespace: f.Namespace, Name: f.Name,
			Ignored: !logging.Counted(f, loggings) || (!watched && watchKnown), Unmet: d.Unmet, HasFilters: f.HasFilters,
			Destinations: []logDestination{},
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
		for _, ref := range f.GlobalOutputs {
			row.Destinations = append(row.Destinations, outputRef(outputs, "ClusterOutput", controlNamespace[f.LoggingRef], ref))
		}
		for _, ref := range f.LocalOutputs {
			row.Destinations = append(row.Destinations, outputRef(outputs, "Output", f.Namespace, ref))
		}
		row.Excluded = !row.Ignored && d.Rule >= 0 && !d.Selected && !d.Uncertain
		if row.Selected {
			row.Delivers = loggingDelivers(row, outputsUnread)
		}
		if !row.Ignored {
			resp.Shipped = resp.Shipped || (row.Delivers != nil && *row.Delivers)
			resp.Uncertain = resp.Uncertain || row.Uncertain || (row.Selected && row.Delivers == nil)
		}
		resp.Routes = append(resp.Routes, row)
	}
	if resp.Shipped {
		resp.Uncertain = false
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

func outputRef(outputs map[[3]string]logging.OutputSummary, kind, namespace, name string) logDestination {
	ref := logDestination{Kind: kind, Namespace: namespace, Name: name}
	if o, ok := outputs[[3]string{kind, namespace, name}]; ok {
		ref.Type, ref.Found, ref.Active = o.Type, true, o.Active
	}
	return ref
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
