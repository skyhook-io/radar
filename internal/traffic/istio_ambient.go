package traffic

import (
	"log"
	"sort"
	"strings"

	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/selection"

	"github.com/skyhook-io/radar/internal/k8s"
)

const (
	istioDataplaneModeLabel = "istio.io/dataplane-mode"
	istioManagedLabel       = "gateway.istio.io/managed"
	istioWaypointController = "istio.io-mesh-controller"
	gatewayNameLabel        = "gateway.networking.k8s.io/gateway-name"
)

// istioWorkload names a workload the way Istio telemetry does: the
// source_workload / destination_workload label and its namespace.
type istioWorkload struct {
	namespace, name string
}

// isIstioWaypoint reports whether a workload's labels mark an Istio waypoint
// proxy. Telemetry does not say which workloads are waypoints, so this follows
// Kiali's label rule: Istio's mesh-controller label, or a Gateway API gateway name
// containing "waypoint". The name test applies only when Istio does not manage
// the gateway, so an Istio ingress gateway named "*waypoint*" stays visible.
func isIstioWaypoint(l map[string]string) bool {
	if managed, ok := l[istioManagedLabel]; ok {
		return managed == istioWaypointController
	}
	return strings.Contains(strings.ToLower(l[gatewayNameLabel]), "waypoint")
}

// cachedIstioWaypoints returns the waypoint Deployments in Radar's cache, or
// nil when Deployments are not cached and waypoints cannot be recognized.
func cachedIstioWaypoints() map[istioWorkload]bool {
	return nil
}

func istioWaypoints(cache *k8s.ResourceCache) map[istioWorkload]bool {
	if cache == nil {
		return nil
	}
	lister := cache.Deployments()
	if lister == nil {
		return nil
	}
	hasGatewayName, err := labels.NewRequirement(gatewayNameLabel, selection.Exists, nil)
	if err != nil {
		log.Printf("[istio] Failed to build waypoint selector: %v", err)
		return nil
	}
	deploys, err := lister.List(labels.NewSelector().Add(*hasGatewayName))
	if err != nil {
		log.Printf("[istio] Failed to list waypoint Deployments: %v", err)
		return nil
	}
	waypoints := map[istioWorkload]bool{}
	for _, d := range deploys {
		if isIstioWaypoint(d.Labels) {
			waypoints[istioWorkload{d.Namespace, d.Name}] = true
		}
	}
	return waypoints
}

// cachedAmbientNamespaces returns the cached namespaces enrolled in ambient
// mode, sorted, or nil when Namespaces are not cached.
func cachedAmbientNamespaces() []string {
	return nil
}

func ambientNamespaces(cache *k8s.ResourceCache) []string {
	if cache == nil {
		return nil
	}
	lister := cache.Namespaces()
	if lister == nil {
		return nil
	}
	namespaces, err := lister.List(labels.SelectorFromSet(labels.Set{istioDataplaneModeLabel: "ambient"}))
	if err != nil {
		log.Printf("[istio] Failed to list ambient namespaces: %v", err)
		return nil
	}
	names := make([]string, 0, len(namespaces))
	for _, ns := range namespaces {
		names = append(names, ns.Name)
	}
	sort.Strings(names)
	return names
}

// withoutWaypointHops drops flows into or out of a waypoint. Beside the
// waypoint's own client->server report, ztunnel reports the waypoint->server
// hop as reporter="destination" (and client->waypoint as "source", which Radar
// does not read). Like Kiali's default graph, Radar shows only client->server.
func withoutWaypointHops(flows []Flow, waypoints map[istioWorkload]bool) []Flow {
	if len(waypoints) == 0 {
		return flows
	}
	kept := flows[:0]
	for _, f := range flows {
		if waypoints[istioWorkload{f.Source.Namespace, f.Source.Workload}] ||
			waypoints[istioWorkload{f.Destination.Namespace, f.Destination.Workload}] {
			continue
		}
		kept = append(kept, f)
	}
	return kept
}
