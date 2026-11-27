package trace

import (
	"fmt"
	"slices"
	"strings"
)

// PlannedInClusterRoutes lists the requests an in-cluster test would send for
// a trace built without probes: one route per declared port of the Service
// behind each of the entry's rules, each with its rule's host and path, or
// one per port of a Service subject. It reads only the specs the static walk
// collected, never a probe result, so a caller can show the requests before
// anything is sent. A route to a Service scaled to zero on purpose is marked
// Benign and carries no request, as in a probed trace.
func PlannedInClusterRoutes(t *Trace) []RouteResult {
	if t == nil || len(t.Downstream) == 0 {
		return nil
	}
	entry := t.Downstream[0]
	branches := downstreamBranches(t.Downstream)
	if len(branches) == 0 {
		if entry.Resource.Kind != "Service" {
			return nil
		}
		routes := declaredPortRoutes(subjectRouteLabel(t.Subject, entry), entry.Resource.Name, hopPorts(entry), nil)
		setTargetNamespace(routes, entry.Resource.Namespace)
		markBenignScaleZero(routes, entry)
		attachInClusterRequest(routes, "", "", entry.Config)
		return routes
	}
	var out []RouteResult
	for _, b := range branches {
		backend := t.Downstream[b.start]
		// A Gateway's attached route is its own entry, and a drained backend
		// serves no traffic by design: neither is a request of this entry.
		if strings.HasPrefix(backend.Edge, "Gateway->") || isDrainedBackendHop(backend) || backend.Config == nil {
			continue
		}
		rules := ruleRoutesForBackend(entry, backend.Resource.Name, backend.Resource.Namespace, backend.Config)
		if len(rules) == 0 {
			rules = []ruleRoute{{label: backend.Resource.Name, ports: backendDeclaredPorts(entry, backend.Resource.Name, backend.Resource.Namespace, backend.Config)}}
		}
		for _, rr := range rules {
			routes := declaredPortRoutes(rr.label, backend.Resource.Name, hopPorts(backend), rr.ports)
			setTargetNamespace(routes, backend.Resource.Namespace)
			markBenignScaleZero(routes, backend)
			attachInClusterRequest(routes, rr.host, rr.path, backend.Config)
			out = append(out, routes...)
		}
	}
	return out
}

// declaredPortRoutes is one not-yet-tested route per declared port of a
// Service, within scope when a rule names ports, labelled as routesByPort
// labels a probed one.
func declaredPortRoutes(routeID, backendName string, ports []PortMap, scope []int32) []RouteResult {
	var nums []int32
	for _, p := range ports {
		if (len(scope) == 0 || slices.Contains(scope, p.Port)) && !slices.Contains(nums, p.Port) {
			nums = append(nums, p.Port)
		}
	}
	slices.Sort(nums)
	out := make([]RouteResult, 0, len(nums))
	for _, port := range nums {
		target := fmt.Sprintf("%s:%d", backendName, port)
		rid := routeID
		if len(nums) > 1 {
			rid = target
			if routeID != "" && routeID != backendName {
				rid = routeID + " · " + target
			}
		}
		out = append(out, RouteResult{Route: rid, Target: target, Outcome: OutcomeNotTested})
	}
	return out
}
