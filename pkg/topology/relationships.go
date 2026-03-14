package topology

import (
	"strings"

	corev1 "k8s.io/api/core/v1"
)

// resolveAPIGroup returns the API group for a resource kind using resource discovery.
// Returns empty string for core K8s types (pods, services, etc.).
func resolveAPIGroup(kind string, dp DynamicProvider) string {
	if dp == nil {
		return ""
	}
	gvr, ok := dp.GetGVR(strings.ToLower(kind))
	if !ok {
		return ""
	}
	return gvr.Group
}

// enrichRef sets the API group on a ResourceRef for CRD types.
func enrichRef(ref *ResourceRef, dp DynamicProvider) {
	if ref == nil {
		return
	}
	ref.Group = resolveAPIGroup(ref.Kind, dp)
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
func GetRelationships(kind, namespace, name string, topo *Topology, provider ResourceProvider, dp DynamicProvider) *Relationships {
	if topo == nil {
		return nil
	}

	// Build the node ID for this resource (matches format used in builder.go)
	nodeID := buildNodeID(kind, namespace, name, dp)

	rel := &Relationships{}

	for _, edge := range topo.Edges {
		if edge.Source == nodeID {
			// This resource points TO something (outgoing edge)
			ref := parseNodeID(edge.Target, dp)
			if ref == nil {
				continue
			}
			enrichRef(ref, dp)

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
				// HPA/ScaledObject/ScaledJob scales a workload
				rel.ScaleTarget = ref
			case EdgeProtects:
				// PDB protects a workload (outgoing from PDB)
				rel.ScaleTarget = ref
			case EdgeConfigures:
				// ConfigMap/Secret is used by a workload (outgoing from config)
				rel.Consumers = append(rel.Consumers, *ref)
			}
		}

		if edge.Target == nodeID {
			// Something points TO this resource (incoming edge)
			ref := parseNodeID(edge.Source, dp)
			if ref == nil {
				continue
			}
			enrichRef(ref, dp)

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
				// An HPA/ScaledObject/ScaledJob scales this resource
				rel.Scalers = append(rel.Scalers, *ref)
			case EdgeProtects:
				// A PDB protects this workload
				rel.Policies = append(rel.Policies, *ref)
			case EdgeConfigures:
				// A ConfigMap/Secret is used by this resource
				rel.ConfigRefs = append(rel.ConfigRefs, *ref)
			}
		}
	}

	// Convenience shortcuts: bridge the Deployment↔ReplicaSet↔Pod gap
	// so users see Pods directly under Deployments and vice versa.
	kindLower := strings.ToLower(kind)

	// Deployment → show grandchild Pods (Deployment→ReplicaSet→Pod)
	if kindLower == "deployments" || kindLower == "deployment" {
		for _, child := range rel.Children {
			if strings.EqualFold(child.Kind, "ReplicaSet") {
				childID := buildNodeID(child.Kind, child.Namespace, child.Name, dp)
				for _, edge := range topo.Edges {
					if edge.Source == childID && edge.Type == EdgeManages {
						podRef := parseNodeID(edge.Target, dp)
						if podRef != nil && strings.EqualFold(podRef.Kind, "Pod") {
							enrichRef(podRef, dp)
							rel.Pods = append(rel.Pods, *podRef)
						}
					}
				}
			}
		}
	}

	// Pod → if owner is a ReplicaSet, also show the grandparent Deployment
	if kindLower == "pods" || kindLower == "pod" {
		if rel.Owner != nil && strings.EqualFold(rel.Owner.Kind, "ReplicaSet") {
			ownerID := buildNodeID(rel.Owner.Kind, rel.Owner.Namespace, rel.Owner.Name, dp)
			for _, edge := range topo.Edges {
				if edge.Target == ownerID && edge.Type == EdgeManages {
					deployRef := parseNodeID(edge.Source, dp)
					if deployRef != nil && strings.EqualFold(deployRef.Kind, "Deployment") {
						enrichRef(deployRef, dp)
						rel.Deployment = deployRef
						break
					}
				}
			}
		}
	}

	// Storage chain: PVC→PV→StorageClass (direct provider lookups, not topology edges)
	if provider != nil {
		switch kindLower {
		case "persistentvolumeclaim", "persistentvolumeclaims", "pvc", "pvcs":
			pvcs, _ := provider.PersistentVolumeClaims()
			for _, pvc := range pvcs {
				if pvc.Namespace == namespace && pvc.Name == name && pvc.Spec.VolumeName != "" {
					pvRef := ResourceRef{Kind: "PersistentVolume", Name: pvc.Spec.VolumeName}
					enrichRef(&pvRef, dp)
					rel.Children = append(rel.Children, pvRef)
					break
				}
			}
		case "persistentvolume", "persistentvolumes", "pv", "pvs":
			pvs, _ := provider.PersistentVolumes()
			for _, pv := range pvs {
				if pv.Name == name {
					if pv.Spec.ClaimRef != nil {
						claimRef := ResourceRef{Kind: "PersistentVolumeClaim", Namespace: pv.Spec.ClaimRef.Namespace, Name: pv.Spec.ClaimRef.Name}
						enrichRef(&claimRef, dp)
						rel.Consumers = append(rel.Consumers, claimRef)
					}
					if pv.Spec.StorageClassName != "" {
						scRef := ResourceRef{Kind: "StorageClass", Name: pv.Spec.StorageClassName}
						enrichRef(&scRef, dp)
						rel.ConfigRefs = append(rel.ConfigRefs, scRef)
					}
					break
				}
			}
		case "storageclass", "storageclasses", "sc":
			pvs, _ := provider.PersistentVolumes()
			for _, pv := range pvs {
				if pv.Spec.StorageClassName == name {
					pvRef := ResourceRef{Kind: "PersistentVolume", Name: pv.Name}
					enrichRef(&pvRef, dp)
					rel.Children = append(rel.Children, pvRef)
				}
			}
		case "node", "nodes":
			allPods, _ := provider.Pods()
			for _, pod := range allPods {
				if pod.Spec.NodeName == name && pod.Status.Phase != corev1.PodSucceeded && pod.Status.Phase != corev1.PodFailed {
					podRef := ResourceRef{Kind: "Pod", Namespace: pod.Namespace, Name: pod.Name}
					enrichRef(&podRef, dp)
					rel.Pods = append(rel.Pods, podRef)
				}
			}
		}
	}

	// Return nil if no relationships found
	if rel.Owner == nil && rel.Deployment == nil && len(rel.Children) == 0 && len(rel.Services) == 0 &&
		len(rel.Ingresses) == 0 && len(rel.Gateways) == 0 && len(rel.Routes) == 0 &&
		len(rel.ConfigRefs) == 0 && len(rel.Consumers) == 0 && len(rel.Scalers) == 0 &&
		len(rel.Policies) == 0 && rel.ScaleTarget == nil && len(rel.Pods) == 0 {
		return nil
	}

	return rel
}

// buildNodeID constructs a node ID from kind, namespace, and name
// This must match the format used in builder.go
// Format: kind/namespace/name (using / since it's not allowed in K8s names)
func buildNodeID(kind, namespace, name string, dp DynamicProvider) string {
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
		"applications":    "application",
		"kustomizations":  "kustomization",
		"helmreleases":    "helmrelease",
		"gitrepositories": "gitrepository",
		"certificates":    "certificate",
		"issuers":         "issuer",
		"clusterissuers":  "clusterissuer",
		"nodepools":       "nodepool",
		"nodeclaims":      "nodeclaim",
		"nodeclasses":     "nodeclass",
		"ec2nodeclasses":  "nodeclass",
		"aksnodeclasses":  "nodeclass",
		"gcpnodeclasses":  "nodeclass",
		"scaledobjects":            "scaledobject",
		"scaledjobs":               "scaledjob",
		"gatewayclasses":           "gatewayclass",
		"virtualservices":          "virtualservice",
		"destinationrules":         "destinationrule",
		"istiogateways":            "istiogateway",
		"serviceentries":           "serviceentry",
		"peerauthentications":      "peerauthentication",
		"authorizationpolicies":    "authorizationpolicy",
		"knativeservices":          "knativeservice",
		"configurations":           "knativeconfiguration",
		"revisions":                "knativerevision",
		"routes":                   "knativeroute",
		"brokers":                  "broker",
		"triggers":                 "trigger",
		"pingsources":              "pingsource",
		"apiserversources":         "apiserversource",
		"containersources":         "containersource",
		"sinkbindings":             "sinkbinding",
		"channels":                 "channel",
		"ingressroutes":            "ingressroute",       // Traefik
		"ingressroutetcps":         "ingressroutetcp",
		"ingressrouteudps":         "ingressrouteudp",
		"middlewares":              "middleware",
		"middlewaretcps":           "middlewaretcp",
		"traefikservices":          "traefikservice",
		"serverstransports":        "serverstransport",
		"serverstransporttcps":     "serverstransporttcp",
		"tlsoptions":               "tlsoption",
		"tlsstores":                "tlsstore",
		"httpproxies":              "httpproxy",           // Contour
		"persistentvolumes":        "persistentvolume",
		"pvs":                      "persistentvolume",
		"storageclasses":           "storageclass",
		"poddisruptionbudgets":     "poddisruptionbudget",
		"pdbs":                     "poddisruptionbudget",
		"verticalpodautoscalers":   "verticalpodautoscaler",
		"vpas":                     "verticalpodautoscaler",
		"nodes":                    "node",
	}

	if singular, ok := kindMap[k]; ok {
		k = singular
	} else if dp != nil {
		// Fall back to resource discovery for CRDs (e.g., "certificaterequests" → "certificaterequest")
		if res, found := getResourceByName(dp, k); found {
			k = strings.ToLower(res)
		}
	}

	return k + "/" + namespace + "/" + name
}

// getResourceByName looks up a resource kind by its plural name via the DynamicProvider.
// Returns the Kind string and true if found.
func getResourceByName(dp DynamicProvider, pluralName string) (string, bool) {
	// Try GetGVR which accepts kind or resource name
	gvr, ok := dp.GetGVR(pluralName)
	if !ok {
		return "", false
	}
	kind := dp.GetKindForGVR(gvr)
	if kind == "" {
		return "", false
	}
	return kind, true
}

// parseNodeID extracts kind, namespace, and name from a node ID
// Returns nil for PodGroup since it's a UI-only concept, not a real K8s resource
// Format: kind/namespace/name (using / since it's not allowed in K8s names)
func parseNodeID(nodeID string, dp DynamicProvider) *ResourceRef {
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
		Kind:      normalizeKind(kind, dp),
		Namespace: namespace,
		Name:      name,
	}
}

// normalizeKind converts internal kind format to display format
func normalizeKind(kind string, dp DynamicProvider) string {
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
		"application":    "Application",
		"kustomization":  "Kustomization",
		"helmrelease":    "HelmRelease",
		"gitrepository":  "GitRepository",
		"certificate":    "Certificate",
		"issuer":         "Issuer",
		"clusterissuer":  "ClusterIssuer",
		"node":         "Node",
		"nodepool":     "NodePool",
		"nodeclaim":    "NodeClaim",
		"nodeclass":    "NodeClass",
		"scaledobject":            "ScaledObject",
		"scaledjob":               "ScaledJob",
		"gatewayclass":            "GatewayClass",
		"istiogateway":            "Gateway",
		"knativeservice":          "KnativeService",
		"knativeconfiguration":    "Configuration",
		"knativerevision":         "Revision",
		"knativeroute":            "Route",
		"broker":                  "Broker",
		"trigger":                 "Trigger",
		"pingsource":              "PingSource",
		"apiserversource":         "ApiServerSource",
		"containersource":         "ContainerSource",
		"sinkbinding":             "SinkBinding",
		"channel":                 "Channel",
		"ingressroute":            "IngressRoute",        // Traefik
		"ingressroutetcp":         "IngressRouteTCP",
		"ingressrouteudp":         "IngressRouteUDP",
		"middleware":              "Middleware",
		"middlewaretcp":           "MiddlewareTCP",
		"traefikservice":          "TraefikService",
		"serverstransport":        "ServersTransport",
		"serverstransporttcp":     "ServersTransportTCP",
		"tlsoption":               "TLSOption",
		"tlsstore":                "TLSStore",
		"httpproxy":               "HTTPProxy",            // Contour
		"internet":                "Internet",
		"persistentvolume":        "PersistentVolume",
		"storageclass":            "StorageClass",
		"poddisruptionbudget":     "PodDisruptionBudget",
		"verticalpodautoscaler":   "VerticalPodAutoscaler",
	}

	if normalized, ok := kindMap[strings.ToLower(kind)]; ok {
		return normalized
	}
	// Fall back to resource discovery for CRDs (e.g., "certificaterequest" → "CertificateRequest")
	if dp != nil {
		if k, found := getResourceByName(dp, kind); found {
			return k
		}
	}
	return kind
}
