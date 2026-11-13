package server

import (
	"net/http"
	"slices"
	"sort"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"

	"github.com/skyhook-io/radar/internal/integration"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/readscope"
	"github.com/skyhook-io/radar/pkg/logging"
)

const maxUnroutedPods = 500

type logPodCount struct {
	Count      int  `json:"count"`
	LowerBound bool `json:"lowerBound"`
}

type logWorkload struct {
	Kind      string `json:"kind"`
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	Ready     int    `json:"ready"`
	Desired   int    `json:"desired"`
}

type logInstance struct {
	Name             string        `json:"name"`
	ControlNamespace string        `json:"controlNamespace"`
	Collectors       []logWorkload `json:"collectors"`
	Aggregators      []logWorkload `json:"aggregators"`
}

type logPipelineRoute struct {
	Provider  string `json:"provider"`
	Kind      string `json:"kind"`
	Group     string `json:"group"`
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	// Instance names the Logging whose loggingRef the route carries; empty when
	// no Logging has it.
	Instance     string           `json:"instance"`
	Ignored      bool             `json:"ignored"`
	HasFilters   bool             `json:"hasFilters"`
	Active       *bool            `json:"active,omitempty"`
	Destinations []logDestination `json:"destinations"`
	SelectedPods logPodCount      `json:"selectedPods"`
}

type logPipelineDestination struct {
	Kind      string   `json:"kind"`
	Namespace string   `json:"namespace"`
	Name      string   `json:"name"`
	Type      string   `json:"type,omitempty"`
	Address   string   `json:"address,omitempty"`
	Active    *bool    `json:"active,omitempty"`
	Problems  []string `json:"problems"`
}

type logPipelineProvider struct {
	Provider     string                   `json:"provider"`
	Instances    []logInstance            `json:"instances"`
	Routes       []logPipelineRoute       `json:"routes"`
	Destinations []logPipelineDestination `json:"destinations"`
}

type logPodRef struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
}

type logUnrouted struct {
	Count      int         `json:"count"`
	LowerBound bool        `json:"lowerBound"`
	Pods       []logPodRef `json:"pods"`
	Truncated  bool        `json:"truncated"`
}

type logPipelineCoverage struct {
	// Gaps names the kinds Radar couldn't read, or the caller may not list.
	Gaps []logReadError `json:"gaps"`
	// Namespaces the pod counts cover; null is every namespace.
	Namespaces []string `json:"namespaces"`
}

type logPipelineResponse struct {
	Providers []logPipelineProvider `json:"providers"`
	Unrouted  logUnrouted           `json:"unrouted"`
	Coverage  logPipelineCoverage   `json:"coverage"`
}

// handleLoggingPipeline answers how logs flow through each installed shipper:
// collectors → aggregators → routes (with how many running pods each selects)
// → destinations, and which running pods nothing routes. Gate: the caller can
// list Loggings (cluster-scoped), as Capacity gates on NodePools. Every other
// kind is shown only where the caller may list it; what is withheld is a
// coverage gap, never an empty list. Pods are counted only in namespaces the
// caller can see, and a count over partial data is a lower bound. Outputs are
// projected to kind, type, redacted address, state and problems.
func (s *Server) handleLoggingPipeline(w http.ResponseWriter, r *http.Request) {
	if !s.requireConnected(w) {
		return
	}
	if !s.canRead(r, logging.Group, "loggings", "", "list") {
		s.writeError(w, http.StatusForbidden, "no access to list Loggings")
		return
	}
	namespaces, _ := s.parseNamespacesForUserWithStatus(r)
	resp := logPipelineResponse{
		Providers: []logPipelineProvider{},
		Unrouted:  logUnrouted{Pods: []logPodRef{}},
		Coverage:  logPipelineCoverage{Gaps: []logReadError{}, Namespaces: namespaces},
	}
	discovery := k8s.GetResourceDiscovery()
	if discovery == nil {
		s.writeError(w, http.StatusServiceUnavailable, "API discovery not ready")
		return
	}
	if _, ok := discovery.GetGVRWithGroup("loggings", logging.Group); !ok {
		s.writeJSON(w, resp)
		return
	}
	cache := k8s.GetResourceCache()
	if cache == nil || cache.Pods() == nil {
		s.writeError(w, http.StatusServiceUnavailable, "Resource cache not available")
		return
	}

	gap := func(group, resource string) {
		for _, g := range resp.Coverage.Gaps {
			if g.Group == group && g.Resource == resource {
				return
			}
		}
		resp.Coverage.Gaps = append(resp.Coverage.Gaps, logReadError{group, resource})
	}
	// allowed memoizes the caller's list access per (resource, namespace).
	seen := map[[2]string]bool{}
	allowed := func(group, resource, ns string) bool {
		key := [2]string{group + "/" + resource, ns}
		ok, done := seen[key]
		if !done {
			ok = s.canRead(r, group, resource, ns, "list")
			seen[key] = ok
		}
		if !ok {
			gap(group, resource)
		}
		return ok
	}
	inView := func(ns string) bool {
		return namespaces == nil || (!readscope.NoNamespaceAccess(namespaces) && slices.Contains(namespaces, ns))
	}
	budget := newSyncBudget(r.Context())
	// read lists a Logging kind as far as the caller may list it (per
	// namespace for namespaced kinds) and Radar has it; anything short of a
	// full read is a gap. Namespaced Flows and Outputs only matter within the
	// view; ClusterFlows and ClusterOutputs live in control namespaces.
	read := func(kind, plural string, cluster bool, within []string) []*unstructured.Unstructured {
		k := integration.WorkspaceKind{Key: plural, Group: logging.Group, Kind: kind, Resource: plural, ClusterScoped: cluster}
		acc, items := s.readWorkspaceKind(r, cache, k, within, []string{logging.Group}, budget)
		if acc.State != integration.KindCoverageFull {
			gap(logging.Group, plural)
		}
		return items
	}

	var loggings []logging.Logging
	for _, u := range read("Logging", "loggings", true, nil) {
		loggings = append(loggings, logging.LoggingFrom(u))
	}
	sort.Slice(loggings, func(i, j int) bool { return loggings[i].Name < loggings[j].Name })
	controlNamespace := map[string]string{}
	instanceOf := map[string]string{}
	for _, l := range loggings {
		controlNamespace[l.LoggingRef] = l.ControlNamespace
		instanceOf[l.LoggingRef] = l.Name
	}
	var agents []logging.Agent
	if _, ok := discovery.GetGVRWithGroup("fluentbitagents", logging.Group); ok {
		for _, u := range read("FluentbitAgent", "fluentbitagents", true, nil) {
			ref, _, _ := unstructured.NestedString(u.Object, "spec", "loggingRef")
			agents = append(agents, logging.Agent{Name: u.GetName(), LoggingRef: ref})
		}
	}

	outputs := map[[3]string]logging.OutputSummary{}
	provider := logPipelineProvider{Provider: providerLoggingOperator, Instances: []logInstance{}, Routes: []logPipelineRoute{}, Destinations: []logPipelineDestination{}}
	for _, src := range []struct {
		kind, plural string
		within       []string
	}{{"ClusterOutput", "clusteroutputs", nil}, {"Output", "outputs", namespaces}} {
		for _, u := range read(src.kind, src.plural, false, src.within) {
			o := logging.OutputFrom(u)
			outputs[[3]string{u.GetKind(), o.Namespace, o.Name}] = o
			problems := o.Problems
			if problems == nil {
				problems = []string{}
			}
			provider.Destinations = append(provider.Destinations, logPipelineDestination{
				Kind: u.GetKind(), Namespace: o.Namespace, Name: o.Name, Type: o.Type, Address: o.Address, Active: o.Active, Problems: problems,
			})
		}
	}

	var cands []logging.Candidate
	for _, src := range []struct {
		kind, plural string
		within       []string
	}{{"ClusterFlow", "clusterflows", nil}, {"Flow", "flows", namespaces}} {
		for _, u := range read(src.kind, src.plural, false, src.within) {
			f := logging.FlowFrom(u)
			c := loggingCandidate(f, loggings)
			cands = append(cands, c)
			provider.Routes = append(provider.Routes, logPipelineRoute{
				Provider: providerLoggingOperator, Kind: u.GetKind(), Group: logging.Group, Namespace: f.Namespace, Name: f.Name,
				Instance: instanceOf[f.LoggingRef], Ignored: c.Ignored, HasFilters: f.HasFilters, Active: flowActive(u), Destinations: flowDestinations(f, outputs, controlNamespace),
			})
		}
	}
	routesPartial := false
	for _, g := range resp.Coverage.Gaps {
		if g.Group == logging.Group && (g.Resource == "loggings" || g.Resource == "clusterflows" || g.Resource == "flows") {
			routesPartial = true
		}
	}

	for _, l := range loggings {
		var ws []logging.Workload
		ns := l.ControlNamespace
		if allowed("apps", "daemonsets", ns) && cache.DaemonSets() != nil {
			list, _ := cache.DaemonSets().DaemonSets(ns).List(labels.Everything())
			for _, d := range list {
				ws = append(ws, ownedWorkload("DaemonSet", d.Namespace, d.Name, d.OwnerReferences, int(d.Status.NumberReady), int(d.Status.DesiredNumberScheduled)))
			}
		}
		if allowed("apps", "statefulsets", ns) && cache.StatefulSets() != nil {
			list, _ := cache.StatefulSets().StatefulSets(ns).List(labels.Everything())
			for _, st := range list {
				desired := 1
				if st.Spec.Replicas != nil {
					desired = int(*st.Spec.Replicas)
				}
				ws = append(ws, ownedWorkload("StatefulSet", st.Namespace, st.Name, st.OwnerReferences, int(st.Status.ReadyReplicas), desired))
			}
		}
		collectors, aggregators := logging.InstanceWorkloads(l, agents, ws)
		provider.Instances = append(provider.Instances, logInstance{
			Name: l.Name, ControlNamespace: ns, Collectors: wireWorkloads(collectors), Aggregators: wireWorkloads(aggregators),
		})
	}

	// shortcut: one pass over every running pod in view per request; fine for
	// thousands of pods, cache the counts if the page is polled on huge fleets.
	var pods []logging.PodFacts
	if !readscope.NoNamespaceAccess(namespaces) {
		all, _ := cache.Pods().List(labels.Everything())
		for _, p := range all {
			if p.Status.Phase == corev1.PodRunning && inView(p.Namespace) {
				pods = append(pods, loggingPodFacts(p))
			}
		}
	}
	// Namespaces narrower than "all" leave pods out of every count.
	podsPartial := namespaces != nil
	counts, unrouted, uncertain := logging.CountPods(cands, pods)
	for i := range provider.Routes {
		provider.Routes[i].SelectedPods = logPodCount{Count: counts[i].Count, LowerBound: counts[i].LowerBound || podsPartial}
	}
	sort.Slice(provider.Routes, func(i, j int) bool {
		a, b := provider.Routes[i], provider.Routes[j]
		if a.Ignored != b.Ignored {
			return !a.Ignored
		}
		return a.Namespace+"/"+a.Name < b.Namespace+"/"+b.Name
	})
	sort.Slice(unrouted, func(i, j int) bool {
		return unrouted[i].Namespace+"/"+unrouted[i].Name < unrouted[j].Namespace+"/"+unrouted[j].Name
	})
	resp.Unrouted.Count = len(unrouted)
	resp.Unrouted.LowerBound = podsPartial || routesPartial || uncertain
	for i, p := range unrouted {
		if i == maxUnroutedPods {
			resp.Unrouted.Truncated = true
			break
		}
		resp.Unrouted.Pods = append(resp.Unrouted.Pods, logPodRef{p.Namespace, p.Name})
	}
	resp.Providers = append(resp.Providers, provider)
	s.writeJSON(w, resp)
}

func ownedWorkload(kind, ns, name string, refs []metav1.OwnerReference, ready, desired int) logging.Workload {
	w := logging.Workload{Kind: kind, Namespace: ns, Name: name, Ready: ready, Desired: desired}
	for _, ref := range refs {
		if ref.Controller != nil && *ref.Controller {
			w.OwnerKind, w.OwnerName = ref.Kind, ref.Name
		}
	}
	return w
}

func wireWorkloads(ws []logging.Workload) []logWorkload {
	out := []logWorkload{}
	for _, w := range ws {
		out = append(out, logWorkload{Kind: w.Kind, Namespace: w.Namespace, Name: w.Name, Ready: w.Ready, Desired: w.Desired})
	}
	return out
}
