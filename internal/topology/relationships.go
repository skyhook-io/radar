package topology

import (
	"strings"
)

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

			switch edge.Type {
			case EdgeManages:
				// This resource manages/owns the target
				rel.Children = append(rel.Children, *ref)
			case EdgeExposes:
				// This is a Service exposing something
				rel.Pods = append(rel.Pods, *ref)
			case EdgeRoutesTo:
				// This is an Ingress or Service routing to something
				if strings.ToLower(kind) == "ingress" || strings.ToLower(kind) == "ingresses" {
					// Ingress routes to Service
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

			switch edge.Type {
			case EdgeManages:
				// Something manages/owns this resource
				rel.Owner = ref
			case EdgeExposes:
				// A Service exposes this resource
				rel.Services = append(rel.Services, *ref)
			case EdgeRoutesTo:
				// An Ingress or Service routes to this resource
				sourceKind := strings.ToLower(ref.Kind)
				if sourceKind == "ingress" {
					rel.Ingresses = append(rel.Ingresses, *ref)
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
		len(rel.Ingresses) == 0 && len(rel.ConfigRefs) == 0 && rel.HPA == nil &&
		rel.ScaleTarget == nil && len(rel.Pods) == 0 {
		return nil
	}

	return rel
}

// buildNodeID constructs a node ID from kind, namespace, and name
// This must match the format used in builder.go
func buildNodeID(kind, namespace, name string) string {
	// Normalize kind to match topology builder format
	k := strings.ToLower(kind)

	// Handle plural to singular conversion for common types
	kindMap := map[string]string{
		"pods":         "pod",
		"services":     "service",
		"deployments":  "deployment",
		"daemonsets":   "daemonset",
		"statefulsets": "statefulset",
		"replicasets":  "replicaset",
		"ingresses":    "ingress",
		"configmaps":   "configmap",
		"secrets":      "secret",
		"hpas":         "hpa",
		"jobs":         "job",
		"cronjobs":     "cronjob",
	}

	if singular, ok := kindMap[k]; ok {
		k = singular
	}

	return k + "-" + namespace + "-" + name
}

// parseNodeID extracts kind, namespace, and name from a node ID
func parseNodeID(nodeID string) *ResourceRef {
	// Node IDs are formatted as: kind-namespace-name
	// e.g., "deployment-default-my-app" or "pod-kube-system-coredns-abc123"

	parts := strings.SplitN(nodeID, "-", 3)
	if len(parts) < 3 {
		return nil
	}

	kind := parts[0]
	namespace := parts[1]
	name := parts[2]

	// Handle special case where namespace or name contains dashes
	// We need to be smarter about this - look for known namespace patterns
	// For now, assume namespace doesn't contain dashes (common case)

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
		"daemonset":   "DaemonSet",
		"statefulset": "StatefulSet",
		"replicaset":  "ReplicaSet",
		"ingress":     "Ingress",
		"configmap":   "ConfigMap",
		"secret":      "Secret",
		"hpa":         "HPA",
		"job":         "Job",
		"cronjob":     "CronJob",
		"podgroup":    "PodGroup",
		"internet":    "Internet",
	}

	if normalized, ok := kindMap[strings.ToLower(kind)]; ok {
		return normalized
	}
	return kind
}

// GetRelationshipsForAll computes relationships for multiple resources efficiently
// by iterating through edges only once
func GetRelationshipsForAll(resources []ResourceRef, topo *Topology) map[string]*Relationships {
	if topo == nil {
		return nil
	}

	// Build a map of node ID -> Relationships
	result := make(map[string]*Relationships)
	nodeIDs := make(map[string]bool)

	// Build set of node IDs we care about
	for _, r := range resources {
		nodeID := buildNodeID(r.Kind, r.Namespace, r.Name)
		nodeIDs[nodeID] = true
		result[nodeID] = &Relationships{}
	}

	// Single pass through all edges
	for _, edge := range topo.Edges {
		// Check if source is one of our resources
		if _, ok := nodeIDs[edge.Source]; ok {
			ref := parseNodeID(edge.Target)
			if ref != nil {
				rel := result[edge.Source]
				switch edge.Type {
				case EdgeManages:
					rel.Children = append(rel.Children, *ref)
				case EdgeExposes:
					rel.Pods = append(rel.Pods, *ref)
				case EdgeRoutesTo:
					// Could be Service or Pod depending on source type
					rel.Pods = append(rel.Pods, *ref)
				case EdgeUses:
					rel.ScaleTarget = ref
				}
			}
		}

		// Check if target is one of our resources
		if _, ok := nodeIDs[edge.Target]; ok {
			ref := parseNodeID(edge.Source)
			if ref != nil {
				rel := result[edge.Target]
				switch edge.Type {
				case EdgeManages:
					rel.Owner = ref
				case EdgeExposes:
					rel.Services = append(rel.Services, *ref)
				case EdgeRoutesTo:
					sourceKind := strings.ToLower(ref.Kind)
					if sourceKind == "ingress" {
						rel.Ingresses = append(rel.Ingresses, *ref)
					} else {
						rel.Services = append(rel.Services, *ref)
					}
				case EdgeUses:
					rel.HPA = ref
				case EdgeConfigures:
					rel.ConfigRefs = append(rel.ConfigRefs, *ref)
				}
			}
		}
	}

	return result
}
