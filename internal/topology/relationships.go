package topology

import (
	"strings"

	"github.com/skyhook-io/radar/internal/k8s"
)

// resolveAPIGroup returns the API group for a resource kind using resource discovery.
// Returns empty string for core K8s types (pods, services, etc.).
func resolveAPIGroup(kind string) string {
	discovery := k8s.GetResourceDiscovery()
	if discovery == nil {
		return ""
	}
	gvr, ok := discovery.GetGVR(strings.ToLower(kind))
	if !ok {
		return ""
	}
	return gvr.Group
}

// enrichRef sets the API group on a ResourceRef for CRD types.
func enrichRef(ref *ResourceRef) {
	if ref == nil {
		return
	}
	ref.Group = resolveAPIGroup(ref.Kind)
}

// isRouteKind returns true if the kind is a Gateway API route type.
func isRouteKind(kindLower string) bool {
	switch kindLower {
	case "httproute", "httproutes", "grpcroute", "grpcroutes",
		"tcproute", "tcproutes", "tlsroute", "tlsroutes":
		return true
	}
	return false
}

// GetRelationships computes relationships for a specific resource
// by finding all edges in the topology that involve this resource.
// The topology should be pre-built and cached for performance.
func GetRelationships(kind, namespace, name string, topo *Topology) *Relationships {
	if topo == nil {
		return nil
	}

	// Build the node ID for this resource (matches format used in builder.go)
	nodeID := buildNodeID(kind, namespace, name)

	rel := &Relationships{}

	for _, edge := range topo.Edges {
		if edge.Source == nodeID {
			// This resource points TO something (outgoing edge)
			ref := parseNodeID(edge.Target)
			if ref == nil {
				continue
			}
			enrichRef(ref)

			switch edge.Type {
			case EdgeManages:
				// This resource manages/owns the target
				rel.Children = append(rel.Children, *ref)
			case EdgeExposes:
				// This is a Service exposing something
				rel.Pods = append(rel.Pods, *ref)
			case EdgeRoutesTo:
				// This is an Ingress, Gateway, route, or Service routing to something
				kindLower := strings.ToLower(kind)
				targetKindLower := strings.ToLower(ref.Kind)
				if kindLower == "gateway" || kindLower == "gateways" {
					// Gateway routes to routes or services
					if isRouteKind(targetKindLower) {
						rel.Routes = append(rel.Routes, *ref)
					} else {
						rel.Services = append(rel.Services, *ref)
					}
				} else if kindLower == "ingress" || kindLower == "ingresses" ||
					isRouteKind(kindLower) {
					// Ingress/Route routes to Service
					rel.Services = append(rel.Services, *ref)
				} else {
					// Service routes to Pod
					rel.Pods = append(rel.Pods, *ref)
				}
			case EdgeUses:
				// HPA uses/scales a workload
				rel.ScaleTarget = ref
			case EdgeConfigures:
				// ConfigMap/Secret configures a workload - this is outgoing from config
				// Skip - we handle this on the target side
			}
		}

		if edge.Target == nodeID {
			// Something points TO this resource (incoming edge)
			ref := parseNodeID(edge.Source)
			if ref == nil {
				continue
			}
			enrichRef(ref)

			switch edge.Type {
			case EdgeManages:
				// Something manages/owns this resource
				rel.Owner = ref
			case EdgeExposes:
				// A Service exposes this resource
				rel.Services = append(rel.Services, *ref)
			case EdgeRoutesTo:
				// An Ingress, Gateway, route, or Service routes to this resource
				sourceKind := strings.ToLower(ref.Kind)
				if sourceKind == "ingress" {
					rel.Ingresses = append(rel.Ingresses, *ref)
				} else if sourceKind == "gateway" || sourceKind == "httproute" ||
					sourceKind == "grpcroute" || sourceKind == "tcproute" || sourceKind == "tlsroute" {
					rel.Gateways = append(rel.Gateways, *ref)
				} else if sourceKind == "service" {
					rel.Services = append(rel.Services, *ref)
				}
			case EdgeUses:
				// An HPA scales this resource
				rel.HPA = ref
			case EdgeConfigures:
				// A ConfigMap/Secret is used by this resource
				rel.ConfigRefs = append(rel.ConfigRefs, *ref)
			}
		}
	}

	// Return nil if no relationships found
	if rel.Owner == nil && len(rel.Children) == 0 && len(rel.Services) == 0 &&
		len(rel.Ingresses) == 0 && len(rel.Gateways) == 0 && len(rel.Routes) == 0 &&
		len(rel.ConfigRefs) == 0 && rel.HPA == nil &&
		rel.ScaleTarget == nil && len(rel.Pods) == 0 {
		return nil
	}

	return rel
}

// buildNodeID constructs a node ID from kind, namespace, and name
// This must match the format used in builder.go
// Format: kind/namespace/name (using / since it's not allowed in K8s names)
func buildNodeID(kind, namespace, name string) string {
	// Normalize kind to match topology builder format
	k := strings.ToLower(kind)

	// Handle plural to singular conversion for common types
	kindMap := map[string]string{
		"pods":         "pod",
		"services":     "service",
		"deployments":  "deployment",
		"rollouts":     "rollout",
		"daemonsets":   "daemonset",
		"statefulsets": "statefulset",
		"replicasets":  "replicaset",
		"ingresses":    "ingress",
		"gateways":     "gateway",
		"httproutes":   "httproute",
		"grpcroutes":   "grpcroute",
		"tcproutes":    "tcproute",
		"tlsroutes":    "tlsroute",
		"configmaps":   "configmap",
		"secrets":      "secret",
		"horizontalpodautoscalers": "horizontalpodautoscaler",
		"jobs":                    "job",
		"cronjobs":                "cronjob",
		"persistentvolumeclaims":  "persistentvolumeclaim",
	}

	if singular, ok := kindMap[k]; ok {
		k = singular
	}

	return k + "/" + namespace + "/" + name
}

// parseNodeID extracts kind, namespace, and name from a node ID
// Returns nil for PodGroup since it's a UI-only concept, not a real K8s resource
// Format: kind/namespace/name (using / since it's not allowed in K8s names)
func parseNodeID(nodeID string) *ResourceRef {
	// Node IDs are formatted as: kind/namespace/name
	// e.g., "deployment/default/my-app" or "pod/kube-system/coredns-abc123"

	parts := strings.SplitN(nodeID, "/", 3)
	if len(parts) < 3 {
		return nil
	}

	kind := parts[0]
	namespace := parts[1]
	name := parts[2]

	// Skip PodGroup - it's a UI grouping concept, not a real K8s resource
	if strings.ToLower(kind) == "podgroup" {
		return nil
	}

	return &ResourceRef{
		Kind:      normalizeKind(kind),
		Namespace: namespace,
		Name:      name,
	}
}

// normalizeKind converts internal kind format to display format
func normalizeKind(kind string) string {
	kindMap := map[string]string{
		"pod":         "Pod",
		"service":     "Service",
		"deployment":  "Deployment",
		"rollout":     "Rollout",
		"daemonset":   "DaemonSet",
		"statefulset": "StatefulSet",
		"replicaset":  "ReplicaSet",
		"ingress":     "Ingress",
		"gateway":     "Gateway",
		"httproute":   "HTTPRoute",
		"grpcroute":   "GRPCRoute",
		"tcproute":    "TCPRoute",
		"tlsroute":    "TLSRoute",
		"configmap":                "ConfigMap",
		"secret":                   "Secret",
		"horizontalpodautoscaler":  "HorizontalPodAutoscaler",
		"job":                      "Job",
		"cronjob":                  "CronJob",
		"persistentvolumeclaim":    "PersistentVolumeClaim",
		"podgroup":                 "PodGroup",
		"internet":    "Internet",
	}

	if normalized, ok := kindMap[strings.ToLower(kind)]; ok {
		return normalized
	}
	return kind
}
