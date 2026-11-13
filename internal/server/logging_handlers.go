package server

import (
	"net/http"
	"sort"

	"github.com/go-chi/chi/v5"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/readscope"
	"github.com/skyhook-io/radar/pkg/logging"
)

type loggingOutputRef struct {
	Kind   string `json:"kind"` // Output | ClusterOutput
	Name   string `json:"name"`
	Type   string `json:"type,omitempty"`
	Active *bool  `json:"active,omitempty"` // nil: output not found
}

type loggingPodFlow struct {
	Kind       string             `json:"kind"`
	Namespace  string             `json:"namespace"`
	Name       string             `json:"name"`
	Selected   bool               `json:"selected"`
	Uncertain  bool               `json:"uncertain"`
	Ignored    bool               `json:"ignored"`
	Unmet      []string           `json:"unmet"`
	HasFilters bool               `json:"hasFilters"`
	Active     *bool              `json:"active,omitempty"`
	Outputs    []loggingOutputRef `json:"outputs"`
}

type loggingPodResponse struct {
	Installed bool             `json:"installed"`
	Flows     []loggingPodFlow `json:"flows"`
	// Shipped: a counted Flow selects the pod for certain.
	Shipped bool `json:"shipped"`
	// Uncertain: nothing selects it for certain, but a Flow may.
	Uncertain bool `json:"uncertain"`
	// ReadErrors names the kinds Radar could not read; the UI never says
	// "not shipped" while it is non-empty.
	ReadErrors []string `json:"readErrors"`
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
	resp := loggingPodResponse{Flows: []loggingPodFlow{}, ReadErrors: []string{}}
	discovery := k8s.GetResourceDiscovery()
	if _, ok := discovery.GetGVRWithGroup("loggings", logging.Group); !ok {
		s.writeJSON(w, resp)
		return
	}
	resp.Installed = true
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
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	facts := logging.PodFacts{Namespace: pod.Namespace, Labels: pod.Labels, Node: pod.Spec.NodeName}
	for _, c := range append(pod.Spec.InitContainers, pod.Spec.Containers...) {
		facts.Containers = append(facts.Containers, c.Name)
	}

	budget := newSyncBudget(r.Context())
	dc := k8s.GetDynamicResourceCache()
	read := func(plural, namespace string) []*unstructured.Unstructured {
		gvr, ok := discovery.GetGVRWithGroup(plural, logging.Group)
		if !ok || dc == nil {
			resp.ReadErrors = append(resp.ReadErrors, plural)
			return nil
		}
		items, err := budget.listBlocking(dc, gvr, namespace)
		if err != nil {
			resp.ReadErrors = append(resp.ReadErrors, plural)
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
		row := loggingPodFlow{
			Kind: u.GetKind(), Namespace: f.Namespace, Name: f.Name,
			Ignored: !logging.Counted(f, loggings), Unmet: d.Unmet, HasFilters: f.HasFilters,
			Outputs: []loggingOutputRef{},
		}
		if row.Unmet == nil {
			row.Unmet = []string{}
		}
		// An ignored Flow forwards nothing, whatever its rules say.
		row.Selected, row.Uncertain = d.Selected && !row.Ignored, d.Uncertain && !row.Ignored
		if active, found, _ := unstructured.NestedBool(u.Object, "status", "active"); found {
			row.Active = &active
		}
		for _, ref := range f.GlobalOutputs {
			row.Outputs = append(row.Outputs, outputRef(outputs, "ClusterOutput", controlNamespace[f.LoggingRef], ref))
		}
		for _, ref := range f.LocalOutputs {
			row.Outputs = append(row.Outputs, outputRef(outputs, "Output", f.Namespace, ref))
		}
		if !row.Ignored {
			resp.Shipped = resp.Shipped || row.Selected
			resp.Uncertain = resp.Uncertain || row.Uncertain
		}
		resp.Flows = append(resp.Flows, row)
	}
	if resp.Shipped {
		resp.Uncertain = false
	}
	sort.SliceStable(resp.Flows, func(i, j int) bool {
		a, b := loggingFlowRank(resp.Flows[i]), loggingFlowRank(resp.Flows[j])
		if a != b {
			return a < b
		}
		return resp.Flows[i].Name < resp.Flows[j].Name
	})
	s.writeJSON(w, resp)
}

func outputRef(outputs map[[3]string]logging.OutputSummary, kind, namespace, name string) loggingOutputRef {
	ref := loggingOutputRef{Kind: kind, Name: name}
	if o, ok := outputs[[3]string{kind, namespace, name}]; ok {
		ref.Type = o.Type
		active := o.Active
		ref.Active = &active
	}
	return ref
}

// loggingFlowRank orders selected, uncertain, not selected, then ignored.
func loggingFlowRank(f loggingPodFlow) int {
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
