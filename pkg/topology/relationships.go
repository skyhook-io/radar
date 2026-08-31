package topology

import (
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/skyhook-io/radar/pkg/resourceid"
)

// RelationshipsIndex is a precomputed map from node ID to the edges touching
// that node, derived from a single pass over Topology.Edges. Repeated
// per-resource lookups via GetRelationshipsWithIndex skip the O(E) edge scan
// in exchange for one O(E) build per topology refresh.
//
// Build once via IndexByResource(topo). Pass to GetRelationshipsWithIndex on
// each call. Lookups are read-only and goroutine-safe; mutation is not.
type RelationshipsIndex struct {
	byNodeID           map[string]*nodeEdgeSlots
	nodesByID          map[string]*Node
	nodesByResourceKey map[string]*Node
}

type nodeEdgeSlots struct {
	incoming []Edge
	outgoing []Edge
}

// IndexByResource builds a RelationshipsIndex over topo. Safe to call with a
// nil topology — returns an empty index whose lookups all miss.
func IndexByResource(topo *Topology) *RelationshipsIndex {
	if topo == nil {
		return &RelationshipsIndex{
			byNodeID:           map[string]*nodeEdgeSlots{},
			nodesByID:          map[string]*Node{},
			nodesByResourceKey: map[string]*Node{},
		}
	}
	idx := &RelationshipsIndex{
		byNodeID:           make(map[string]*nodeEdgeSlots, len(topo.Nodes)),
		nodesByID:          make(map[string]*Node, len(topo.Nodes)),
		nodesByResourceKey: make(map[string]*Node, len(topo.Nodes)),
	}
	for i := range topo.Nodes {
		node := &topo.Nodes[i]
		idx.nodesByID[node.ID] = node
		for _, key := range nodeResourceKeys(node) {
			idx.nodesByResourceKey[key] = node
		}
	}
	for _, e := range topo.Edges {
		if e.Source != "" {
			slot := idx.byNodeID[e.Source]
			if slot == nil {
				slot = &nodeEdgeSlots{}
				idx.byNodeID[e.Source] = slot
			}
			slot.outgoing = append(slot.outgoing, e)
		}
		if e.Target != "" {
			slot := idx.byNodeID[e.Target]
			if slot == nil {
				slot = &nodeEdgeSlots{}
				idx.byNodeID[e.Target] = slot
			}
			slot.incoming = append(slot.incoming, e)
		}
	}
	return idx
}

// EdgesFor returns the incoming and outgoing edges touching nodeID. Both
// slices alias the index's internal storage — callers MUST NOT mutate them.
// Returns (nil, nil) when the node has no edges or the index is nil.
func (r *RelationshipsIndex) EdgesFor(nodeID string) (incoming, outgoing []Edge) {
	if r == nil {
		return nil, nil
	}
	slot := r.byNodeID[nodeID]
	if slot == nil {
		return nil, nil
	}
	return slot.incoming, slot.outgoing
}

// edgesForNode returns incoming/outgoing edges for nodeID, preferring the
// index when supplied and falling back to a linear scan over topo.Edges.
func edgesForNode(topo *Topology, idx *RelationshipsIndex, nodeID string) (incoming, outgoing []Edge) {
	if idx != nil {
		return idx.EdgesFor(nodeID)
	}
	if topo == nil {
		return nil, nil
	}
	for _, e := range topo.Edges {
		if e.Source == nodeID {
			outgoing = append(outgoing, e)
		}
		if e.Target == nodeID {
			incoming = append(incoming, e)
		}
	}
	return incoming, outgoing
}

// GetCascadeDeletePreview walks topology management edges to approximate the
// resources Kubernetes may garbage-collect with root.
func GetCascadeDeletePreview(root ResourceRef, topo *Topology, dp DynamicProvider) *CascadeDeletePreview {
	preview := &CascadeDeletePreview{
		Root:       root,
		Dependents: []ResourceRef{},
	}
	if topo == nil {
		return preview
	}

	rootID := buildNodeID(root.Kind, root.Namespace, root.Name, dp)
	if root.Group != "" {
		if dp == nil {
			return preview
		}
		gvr, ok := dp.GetGVRWithGroup(root.Kind, root.Group)
		if !ok {
			return preview
		}
		resolvedKind := dp.GetKindForGVR(gvr)
		if resolvedKind == "" {
			return preview
		}
		rootID = strings.ToLower(KindForGVK(resolvedKind, root.Group)) + "/" + root.Namespace + "/" + root.Name
	}
	nodeByID := make(map[string]*Node, len(topo.Nodes))
	for i := range topo.Nodes {
		nodeByID[topo.Nodes[i].ID] = &topo.Nodes[i]
	}

	rootNode, ok := nodeByID[rootID]
	if root.Group != "" {
		if ok && !nodeMatchesAPIGroup(rootNode, root.Group) {
			ok = false
		}
		if !ok {
			// Two CRDs can share a lowercase plural, so the deterministic
			// kind/ns/name ID may resolve to the wrong API group. Resolve
			// against each node's recorded API group instead.
			if matched, _ := findNodeByRef(topo.Nodes, root); matched != nil {
				rootNode = matched
				rootID = matched.ID
				ok = true
			}
		}
	} else {
		if !ok {
			return preview
		}
		rootKind := string(rootNode.Kind)
		if externalKind, found := collisionKindToK8sKind[rootNode.Kind]; found {
			rootKind = externalKind
		}
		matches := 0
		for i := range topo.Nodes {
			node := &topo.Nodes[i]
			nodeKind := string(node.Kind)
			if externalKind, found := collisionKindToK8sKind[node.Kind]; found {
				nodeKind = externalKind
			}
			if strings.EqualFold(nodeKind, rootKind) && node.Name == root.Name && nodeNamespaceFromData(node) == root.Namespace {
				matches++
			}
		}
		if matches > 1 {
			return preview
		}
	}
	if !ok {
		return preview
	}
	preview.RootResolved = true

	// Build adjacency list for EdgeManages edges (source → targets)
	manages := make(map[string][]string)
	for _, edge := range topo.Edges {
		if edge.Type == EdgeManages {
			manages[edge.Source] = append(manages[edge.Source], edge.Target)
		}
	}

	// BFS from root node
	visited := map[string]bool{rootID: true}
	queue := []string{rootID}
	var dependents []ResourceRef

	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]

		for _, targetID := range manages[current] {
			if visited[targetID] {
				continue
			}
			visited[targetID] = true

			ref := resourceRefForNode(nodeByID[targetID], dp)
			if ref == nil {
				continue
			}
			dependents = append(dependents, *ref)
			queue = append(queue, targetID)
		}
	}

	if dependents != nil {
		preview.Dependents = dependents
	}

	return preview
}

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
	if ref.Group != "" {
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

func isServiceEntrypointRouteKind(kindLower string) bool {
	if isRouteKind(kindLower) {
		return true
	}
	switch kindLower {
	case "route", "ingressroute", "ingressroutetcp", "ingressrouteudp", "virtualservice", "httpproxy":
		return true
	}
	return false
}

// GetRelationships computes relationships for a specific resource by finding
// all edges in the topology that involve this resource. The topology should be
// pre-built and cached for performance. Builds a per-call inline index for
// edge lookups; callers with many GetRelationships calls against the same
// topology should use GetRelationshipsWithIndex with a shared
// RelationshipsIndex instead.
//
// Prefer GetRelationshipsWithObject when the resource has already been fetched
// by the caller — the kind/name lookup used inside this entry point is
// group-blind and can return the wrong typed object for CRDs whose plural
// collides with a core resource (e.g. Knative Service vs core Service).
func GetRelationships(kind, namespace, name string, topo *Topology, provider ResourceProvider, dp DynamicProvider) *Relationships {
	return GetRelationshipsWithObject(kind, namespace, name, nil, topo, provider, dp, nil)
}

// GetRelationshipsWithIndex is the indexed variant of GetRelationships. When
// idx is non-nil, edge lookups go through the precomputed inverted index
// instead of scanning topo.Edges; when nil, behavior matches GetRelationships
// exactly. Callers that issue many per-resource queries against the same
// topology (T6 BuildResourceContext, T12 get_neighborhood) should build idx
// once and reuse it.
//
// Prefer GetRelationshipsWithObject when the resource has already been fetched —
// see the doc on GetRelationships for the kind/group collision rationale.
func GetRelationshipsWithIndex(kind, namespace, name string, topo *Topology, provider ResourceProvider, dp DynamicProvider, idx *RelationshipsIndex) *Relationships {
	return GetRelationshipsWithObject(kind, namespace, name, nil, topo, provider, dp, idx)
}

// GetRelationshipsWithObject is the canonical entry point. When obj is non-nil
// it is used directly for Pod spec extraction (ServiceAccountName, NodeName)
// and ManagedBy synthesis, eliminating the group-blind kind/name lookup
// inside lookupObjectMetadata. Callers that have already fetched the resource
// — REST GET, MCP get_resource — MUST pass obj here, otherwise CRDs whose
// plural collides with a core resource (e.g. Knative serving.knative.dev/Service
// vs core/v1 Service) silently surface the wrong managed-by ref.
//
// obj may be any typed K8s object or *unstructured.Unstructured (both satisfy
// metav1.Object and the *corev1.Pod type assertion remains nil-safe). When
// obj is nil, behavior matches the pre-refactor path: lookupObjectMetadata
// is called, with the group-collision risk noted above.
func GetRelationshipsWithObject(kind, namespace, name string, obj any, topo *Topology, provider ResourceProvider, dp DynamicProvider, idx *RelationshipsIndex) *Relationships {
	if topo == nil {
		return nil
	}

	// Build the node ID for this resource (matches format used in builder.go)
	resolvedKind := kind
	objectKind, objectGroup := objectGVK(obj)
	if objectGroup != "" {
		if objectKind != "" {
			resolvedKind = objectKind
		}
		resolvedKind = KindForGVK(resolvedKind, objectGroup)
	}
	nodeID := buildNodeID(resolvedKind, namespace, name, dp)
	lookupIndex := idx
	if lookupIndex == nil {
		lookupIndex = IndexByResource(topo)
	}
	nodeByID := lookupIndex.nodesByID
	directNode, directExists := nodeByID[nodeID]
	if !directExists || objectGroup != "" && !nodeMatchesAPIGroup(directNode, objectGroup) {
		if matched, _ := findNodeByRef(topo.Nodes, ResourceRef{Kind: resolvedKind, Namespace: namespace, Name: name, Group: objectGroup}); matched != nil {
			nodeID = matched.ID
		} else if objectGroup != "" {
			nodeID = ""
		}
	}
	incomingEdges, outgoingEdges := edgesForNode(topo, lookupIndex, nodeID)
	refForNodeID := func(id string) *ResourceRef {
		return resourceRefForNode(nodeByID[id], dp)
	}

	rel := &Relationships{}
	kindLower := strings.ToLower(kind)

	for _, edge := range outgoingEdges {
		// This resource points TO something (outgoing edge)
		ref := refForNodeID(edge.Target)
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
			// This is an Ingress, Gateway, route, or Service routing to something
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
			if isStorageRefKind(kindLower) {
				rel.Consumers = append(rel.Consumers, *ref)
			} else {
				// HPA/ScaledObject/ScaledJob scales a workload
				rel.ScaleTarget = ref
			}
		case EdgeProtects:
			// Outgoing EdgeProtects fires when the queried resource IS a
			// PDB, NetworkPolicy, CiliumNetworkPolicy, or MachineHealthCheck —
			// each of these emits a "protects/selects target workload" edge.
			//
			// Intentionally NOT surfaced today. The existing per-resource
			// relationship fields (PDBs, NetworkPolicies, Scalers, etc.)
			// describe "things that act on me," not "things I act on" —
			// so there's no semantically correct field to land outgoing
			// protects refs in.
			//
			// TODO: when we introduce a target-side "Protects []ResourceRef"
			// field on Relationships, surface these refs there with their
			// source kind preserved. Until then, leave the outgoing direction
			// of EdgeProtects unsurfaced. The topology graph itself still
			// carries these edges; only the per-resource projection skips them.
		case EdgeConfigures:
			// ConfigMap/Secret is used by a workload (outgoing from config)
			rel.Consumers = append(rel.Consumers, *ref)
		}
	}

	for _, edge := range incomingEdges {
		// Something points TO this resource (incoming edge)
		ref := refForNodeID(edge.Source)
		if ref == nil {
			continue
		}

		switch edge.Type {
		case EdgeManages:
			// Something manages/owns this resource
			rel.Owner = ref
		case EdgeExposes:
			if isServiceEntrypointRouteKind(strings.ToLower(ref.Kind)) {
				rel.Routes = appendResourceRef(rel.Routes, *ref)
			} else {
				rel.Services = appendResourceRef(rel.Services, *ref)
			}
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
			if isStorageRefKind(ref.Kind) {
				rel.StorageRefs = append(rel.StorageRefs, *ref)
			} else {
				// An HPA/ScaledObject/ScaledJob scales this resource
				rel.Scalers = append(rel.Scalers, *ref)
			}
		case EdgeProtects:
			// Incoming EdgeProtects: dispatch on source kind so PDBs and
			// NetworkPolicies land in distinct fields.
			switch ref.Kind {
			case "PodDisruptionBudget":
				rel.PDBs = append(rel.PDBs, *ref)
			case "NetworkPolicy", "GlobalNetworkPolicy", "StagedNetworkPolicy", "StagedGlobalNetworkPolicy", "StagedKubernetesNetworkPolicy",
				"CiliumNetworkPolicy", "ClusterNetworkPolicy", "CiliumClusterwideNetworkPolicy":
				rel.NetworkPolicies = append(rel.NetworkPolicies, *ref)
			}
		case EdgeConfigures:
			switch ref.Kind {
			case "ServiceAccount":
				rel.ServiceAccount = ref
			case "ServiceMonitor", "PodMonitor":
				// Monitor resources observe their targets; topology carries the edge,
				// but Relationships has no observability group to project it into yet.
			default:
				rel.ConfigRefs = append(rel.ConfigRefs, *ref)
			}
		}
	}

	addServiceEntrypoints(rel, topo, lookupIndex)

	// Convenience shortcuts: bridge the Deployment↔ReplicaSet↔Pod gap
	// so users see Pods directly under Deployments and vice versa.

	// Deployment → show grandchild Pods (Deployment→ReplicaSet→Pod)
	if kindLower == "deployments" || kindLower == "deployment" {
		for _, child := range rel.Children {
			if strings.EqualFold(child.Kind, "ReplicaSet") {
				childID := buildNodeID(child.Kind, child.Namespace, child.Name, dp)
				_, childOutgoing := edgesForNode(topo, lookupIndex, childID)
				for _, edge := range childOutgoing {
					if edge.Type != EdgeManages {
						continue
					}
					podRef := refForNodeID(edge.Target)
					if podRef != nil && strings.EqualFold(podRef.Kind, "Pod") {
						rel.Pods = append(rel.Pods, *podRef)
					}
				}
			}
		}
	}

	// Pod → if owner is a ReplicaSet, also show the grandparent Deployment
	if kindLower == "pods" || kindLower == "pod" {
		if rel.Owner != nil && strings.EqualFold(rel.Owner.Kind, "ReplicaSet") {
			ownerID := buildNodeID(rel.Owner.Kind, rel.Owner.Namespace, rel.Owner.Name, dp)
			ownerIncoming, _ := edgesForNode(topo, lookupIndex, ownerID)
			for _, edge := range ownerIncoming {
				if edge.Type != EdgeManages {
					continue
				}
				deployRef := refForNodeID(edge.Source)
				if deployRef != nil && strings.EqualFold(deployRef.Kind, "Deployment") {
					rel.Deployment = deployRef
					break
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

	// Hygiene fields (T2): ServiceAccount + Node from Pod.Spec, ManagedBy from
	// labels/annotations on the queried object (or topology owner chain fallback).
	//
	// Use the caller-provided obj when available — it is the authoritative
	// resource (already disambiguated by group at fetch time) and avoids the
	// group-blind kind/name lookup. Fall back to lookupObjectMetadata only
	// when obj is nil (back-compat path).
	queriedObj := obj
	if queriedObj == nil {
		queriedObj = lookupObjectMetadata(kindLower, namespace, name, provider, dp)
	}
	if pod, ok := queriedObj.(*corev1.Pod); ok {
		if sa := pod.Spec.ServiceAccountName; sa != "" {
			saRef := ResourceRef{Kind: "ServiceAccount", Namespace: namespace, Name: sa}
			enrichRef(&saRef, dp)
			rel.ServiceAccount = &saRef
		}
		if nodeName := pod.Spec.NodeName; nodeName != "" {
			nodeRef := ResourceRef{Kind: "Node", Name: nodeName}
			enrichRef(&nodeRef, dp)
			rel.Node = &nodeRef
		}
		for _, pc := range pod.Spec.ResourceClaims {
			claimName := ""
			if pc.ResourceClaimName != nil {
				claimName = *pc.ResourceClaimName
			} else {
				// Template-generated claims: the actual claim name only exists in status
				for _, st := range pod.Status.ResourceClaimStatuses {
					if st.Name == pc.Name && st.ResourceClaimName != nil {
						claimName = *st.ResourceClaimName
						break
					}
				}
			}
			if claimName != "" {
				rel.ResourceClaims = append(rel.ResourceClaims, ResourceRef{
					Kind: "ResourceClaim", Namespace: namespace, Name: claimName, Group: "resource.k8s.io",
				})
			}
		}
	}
	if endpoint, ok := queriedObj.(*unstructured.Unstructured); ok &&
		(kindLower == "hostendpoint" || kindLower == "hostendpoints") &&
		(endpoint.GroupVersionKind().Group == "crd.projectcalico.org" || endpoint.GroupVersionKind().Group == "projectcalico.org") {
		if nodeName, _, _ := unstructured.NestedString(endpoint.Object, "spec", "node"); nodeName != "" {
			nodeRef := ResourceRef{Kind: "Node", Name: nodeName}
			enrichRef(&nodeRef, dp)
			rel.Node = &nodeRef
		}
	}
	// ResourceClaim → DeviceClass (what it requests) + reservedFor Pods (who holds it).
	if kindLower == "resourceclaim" || kindLower == "resourceclaims" {
		if u, ok := queriedObj.(*unstructured.Unstructured); ok && u.GroupVersionKind().Group == "resource.k8s.io" {
			seenClasses := map[string]bool{}
			requests, _, _ := unstructured.NestedSlice(u.Object, "spec", "devices", "requests")
			for _, r := range requests {
				req, ok := r.(map[string]any)
				if !ok {
					continue
				}
				// v1/v1beta2 nest the class under "exactly" (or firstAvailable
				// subrequests); v1beta1 had deviceClassName at the request level.
				var classNames []string
				if dc, _, _ := unstructured.NestedString(req, "exactly", "deviceClassName"); dc != "" {
					classNames = append(classNames, dc)
				} else if dc, _, _ := unstructured.NestedString(req, "deviceClassName"); dc != "" {
					classNames = append(classNames, dc)
				}
				if subs, _, _ := unstructured.NestedSlice(req, "firstAvailable"); len(subs) > 0 {
					for _, s := range subs {
						if sub, ok := s.(map[string]any); ok {
							if dc, _, _ := unstructured.NestedString(sub, "deviceClassName"); dc != "" {
								classNames = append(classNames, dc)
							}
						}
					}
				}
				for _, dc := range classNames {
					if !seenClasses[dc] {
						seenClasses[dc] = true
						rel.ConfigRefs = append(rel.ConfigRefs, ResourceRef{Kind: "DeviceClass", Name: dc, Group: "resource.k8s.io"})
					}
				}
			}
			reserved, _, _ := unstructured.NestedSlice(u.Object, "status", "reservedFor")
			for _, r := range reserved {
				ref, ok := r.(map[string]any)
				if !ok {
					continue
				}
				resource, _, _ := unstructured.NestedString(ref, "resource")
				holderName, _, _ := unstructured.NestedString(ref, "name")
				if resource == "pods" && holderName != "" {
					podRef := ResourceRef{Kind: "Pod", Namespace: namespace, Name: holderName}
					enrichRef(&podRef, dp)
					rel.Consumers = append(rel.Consumers, podRef)
				}
			}
		}
	}
	var managedByMeta metav1.Object
	if m, ok := queriedObj.(metav1.Object); ok {
		managedByMeta = m
	}
	if mb := synthesizeManagedByFromNode(managedByMeta, nodeID, topo, dp, idx); len(mb) > 0 {
		rel.ManagedBy = mb
	}

	// Return nil if no relationships found
	if rel.Owner == nil && rel.Deployment == nil && len(rel.Children) == 0 && len(rel.Services) == 0 &&
		len(rel.Ingresses) == 0 && len(rel.Gateways) == 0 && len(rel.Routes) == 0 &&
		len(rel.ConfigRefs) == 0 && len(rel.Consumers) == 0 && len(rel.Scalers) == 0 &&
		len(rel.StorageRefs) == 0 &&
		len(rel.PDBs) == 0 && len(rel.NetworkPolicies) == 0 &&
		rel.ScaleTarget == nil && len(rel.Pods) == 0 &&
		rel.ServiceAccount == nil && rel.Node == nil && len(rel.ResourceClaims) == 0 && len(rel.ManagedBy) == 0 {
		return nil
	}

	return rel
}

func addServiceEntrypoints(rel *Relationships, topo *Topology, idx *RelationshipsIndex) {
	for _, service := range rel.Services {
		if service.Group != "" {
			continue
		}
		serviceNode := idx.nodesByResourceKey[resourceid.ResourceKey(service.Group, service.Kind, service.Namespace, service.Name)]
		if serviceNode == nil || !strings.EqualFold(KubernetesKindForNode(serviceNode), "Service") {
			continue
		}
		incoming, _ := edgesForNode(topo, idx, serviceNode.ID)
		for _, edge := range incoming {
			if edge.Type != EdgeRoutesTo && edge.Type != EdgeExposes {
				continue
			}
			ref := resourceRefForNode(idx.nodesByID[edge.Source], nil)
			if ref == nil {
				continue
			}
			kind := strings.ToLower(ref.Kind)
			switch kind {
			case "ingress":
				rel.Ingresses = appendResourceRef(rel.Ingresses, *ref)
			case "gateway":
				rel.Gateways = appendResourceRef(rel.Gateways, *ref)
			default:
				if isServiceEntrypointRouteKind(kind) {
					rel.Routes = appendResourceRef(rel.Routes, *ref)
				}
			}
		}
	}
}

func resourceRefForNodeID(nodeID string, topo *Topology, dp DynamicProvider) *ResourceRef {
	if topo != nil {
		for i := range topo.Nodes {
			if topo.Nodes[i].ID == nodeID {
				return resourceRefForNode(&topo.Nodes[i], dp)
			}
		}
	}
	return nil
}

func resourceRefForNode(node *Node, dp DynamicProvider) *ResourceRef {
	if node == nil {
		return nil
	}
	ref := parseNodeID(node.ID, dp)
	if ref == nil {
		return nil
	}
	ref.Kind = KubernetesKindForNode(node)
	ref.Group = nodeAPIGroupFromData(node)
	if ref.Group == "" {
		ref.Group = resourceid.GroupForBuiltinKind(ref.Kind)
	}
	return ref
}

type objectGVKReader interface {
	GetAPIVersion() string
	GetKind() string
}

func objectGVK(obj any) (kind, group string) {
	if reader, ok := obj.(objectGVKReader); ok {
		return reader.GetKind(), APIVersionGroup(reader.GetAPIVersion())
	}
	if object, ok := obj.(runtime.Object); ok {
		gvk := object.GetObjectKind().GroupVersionKind()
		return gvk.Kind, gvk.Group
	}
	return "", ""
}

func appendResourceRef(refs []ResourceRef, candidate ResourceRef) []ResourceRef {
	for _, ref := range refs {
		if strings.EqualFold(ref.Kind, candidate.Kind) && ref.Namespace == candidate.Namespace && ref.Name == candidate.Name && ref.Group == candidate.Group {
			return refs
		}
	}
	return append(refs, candidate)
}

func isStorageRefKind(kind string) bool {
	switch strings.ToLower(kind) {
	case "persistentvolumeclaim", "persistentvolumeclaims", "pvc", "pvcs":
		return true
	default:
		return false
	}
}

// lookupObjectMetadata returns the typed K8s object (or *unstructured.Unstructured
// for CRDs) for kind/namespace/name. Used to source labels/annotations for
// ManagedBy synthesis and Pod spec fields (ServiceAccountName, NodeName).
//
// Typed-resource lookups go through the ResourceProvider's accessor methods
// (Pods, Deployments, …). For CRDs and any kind without a provider method,
// falls back to the DynamicProvider so GitOps annotations on managed CRs
// (HelmRelease, ExternalSecret, Certificate, etc.) still drive the chip —
// without the fallback, the UI's chip would silently disappear for any
// resource kind not in the typed switch below.
func lookupObjectMetadata(kindLower, namespace, name string, provider ResourceProvider, dp DynamicProvider) any {
	if provider != nil {
		if obj := lookupTypedMetadata(kindLower, namespace, name, provider); obj != nil {
			return obj
		}
	}
	// Fallback for CRDs and any kind not in the typed switch. dp.Get
	// returns a *unstructured.Unstructured, which satisfies metav1.Object.
	if dp != nil {
		if gvr, ok := dp.GetGVR(kindLower); ok {
			if u, err := dp.Get(gvr, namespace, name); err == nil && u != nil {
				return u
			}
		}
	}
	return nil
}

// lookupTypedMetadata is the original typed-resource switch. Split out from
// lookupObjectMetadata so the CRD fallback path is clearly the second tier.
func lookupTypedMetadata(kindLower, namespace, name string, provider ResourceProvider) any {
	switch kindLower {
	case "pod", "pods":
		pods, _ := provider.Pods()
		for _, p := range pods {
			if p.Namespace == namespace && p.Name == name {
				return p
			}
		}
	case "deployment", "deployments":
		ds, _ := provider.Deployments()
		for _, d := range ds {
			if d.Namespace == namespace && d.Name == name {
				return d
			}
		}
	case "statefulset", "statefulsets":
		ss, _ := provider.StatefulSets()
		for _, s := range ss {
			if s.Namespace == namespace && s.Name == name {
				return s
			}
		}
	case "daemonset", "daemonsets":
		ds, _ := provider.DaemonSets()
		for _, d := range ds {
			if d.Namespace == namespace && d.Name == name {
				return d
			}
		}
	case "replicaset", "replicasets":
		rs, _ := provider.ReplicaSets()
		for _, r := range rs {
			if r.Namespace == namespace && r.Name == name {
				return r
			}
		}
	case "job", "jobs":
		jobs, _ := provider.Jobs()
		for _, j := range jobs {
			if j.Namespace == namespace && j.Name == name {
				return j
			}
		}
	case "cronjob", "cronjobs":
		cjs, _ := provider.CronJobs()
		for _, c := range cjs {
			if c.Namespace == namespace && c.Name == name {
				return c
			}
		}
	case "service", "services":
		svcs, _ := provider.Services()
		for _, s := range svcs {
			if s.Namespace == namespace && s.Name == name {
				return s
			}
		}
	case "configmap", "configmaps":
		cms, _ := provider.ConfigMaps()
		for _, c := range cms {
			if c.Namespace == namespace && c.Name == name {
				return c
			}
		}
	case "secret", "secrets":
		ss, _ := provider.Secrets()
		for _, s := range ss {
			if s.Namespace == namespace && s.Name == name {
				return s
			}
		}
	case "serviceaccount", "serviceaccounts":
		if serviceAccountProvider, ok := provider.(ServiceAccountProvider); ok {
			serviceAccounts, _ := serviceAccountProvider.ServiceAccounts()
			for _, serviceAccount := range serviceAccounts {
				if serviceAccount.Namespace == namespace && serviceAccount.Name == name {
					return serviceAccount
				}
			}
		}
	case "ingress", "ingresses":
		is, _ := provider.Ingresses()
		for _, i := range is {
			if i.Namespace == namespace && i.Name == name {
				return i
			}
		}
	case "poddisruptionbudget", "poddisruptionbudgets", "pdb", "pdbs":
		pdbs, _ := provider.PodDisruptionBudgets()
		for _, p := range pdbs {
			if p.Namespace == namespace && p.Name == name {
				return p
			}
		}
	case "networkpolicy", "networkpolicies", "netpol":
		nps, _ := provider.NetworkPolicies()
		for _, n := range nps {
			if n.Namespace == namespace && n.Name == name {
				return n
			}
		}
	case "horizontalpodautoscaler", "horizontalpodautoscalers", "hpa", "hpas":
		hpas, _ := provider.HorizontalPodAutoscalers()
		for _, h := range hpas {
			if h.Namespace == namespace && h.Name == name {
				return h
			}
		}
	case "persistentvolumeclaim", "persistentvolumeclaims", "pvc", "pvcs":
		pvcs, _ := provider.PersistentVolumeClaims()
		for _, p := range pvcs {
			if p.Namespace == namespace && p.Name == name {
				return p
			}
		}
	case "persistentvolume", "persistentvolumes", "pv", "pvs":
		// Cluster-scoped: ignore namespace.
		pvs, _ := provider.PersistentVolumes()
		for _, p := range pvs {
			if p.Name == name {
				return p
			}
		}
	case "node", "nodes":
		// Cluster-scoped: ignore namespace.
		nodes, _ := provider.Nodes()
		for _, n := range nodes {
			if n.Name == name {
				return n
			}
		}
	}
	return nil
}

// buildNodeID constructs a node ID from kind, namespace, and name
// This must match the format used in builder.go
// Format: kind/namespace/name (using / since it's not allowed in K8s names)
func buildNodeID(kind, namespace, name string, dp DynamicProvider) string {
	// Normalize kind to match topology builder format
	k := strings.ToLower(kind)
	switch k {
	case "globalnetworkpolicy":
		k = "calicoglobalnetworkpolicy"
	case "stagednetworkpolicy":
		k = "calicostagednetworkpolicy"
	case "stagedglobalnetworkpolicy":
		k = "calicostagedglobalnetworkpolicy"
	case "stagedkubernetesnetworkpolicy":
		k = "calicostagedkubernetesnetworkpolicy"
	case "caliconetworkpolicies":
		k = "caliconetworkpolicy"
	case "globalnetworkpolicies":
		k = "calicoglobalnetworkpolicy"
	case "stagednetworkpolicies":
		k = "calicostagednetworkpolicy"
	case "stagedglobalnetworkpolicies":
		k = "calicostagedglobalnetworkpolicy"
	case "stagedkubernetesnetworkpolicies":
		k = "calicostagedkubernetesnetworkpolicy"
	}

	// Handle plural to singular conversion for common types
	kindMap := map[string]string{
		"pods":                             "pod",
		"services":                         "service",
		"deployments":                      "deployment",
		"rollouts":                         "rollout",
		"daemonsets":                       "daemonset",
		"statefulsets":                     "statefulset",
		"replicasets":                      "replicaset",
		"ingresses":                        "ingress",
		"gateways":                         "gateway",
		"httproutes":                       "httproute",
		"grpcroutes":                       "grpcroute",
		"tcproutes":                        "tcproute",
		"tlsroutes":                        "tlsroute",
		"configmaps":                       "configmap",
		"secrets":                          "secret",
		"serviceaccounts":                  "serviceaccount",
		"sealedsecrets":                    "sealedsecret",
		"horizontalpodautoscalers":         "horizontalpodautoscaler",
		"jobs":                             "job",
		"cronjobs":                         "cronjob",
		"persistentvolumeclaims":           "persistentvolumeclaim",
		"applications":                     "application",
		"kustomizations":                   "kustomization",
		"helmreleases":                     "helmrelease",
		"gitrepositories":                  "gitrepository",
		"certificates":                     "certificate",
		"issuers":                          "issuer",
		"clusterissuers":                   "clusterissuer",
		"nodepools":                        "nodepool",
		"nodeclaims":                       "nodeclaim",
		"nodeclasses":                      "nodeclass",
		"ec2nodeclasses":                   "nodeclass",
		"aksnodeclasses":                   "nodeclass",
		"gcenodeclasses":                   "nodeclass",
		"scaledobjects":                    "scaledobject",
		"scaledjobs":                       "scaledjob",
		"gatewayclasses":                   "gatewayclass",
		"virtualservices":                  "virtualservice",
		"destinationrules":                 "destinationrule",
		"istiogateways":                    "istiogateway",
		"serviceentries":                   "serviceentry",
		"peerauthentications":              "peerauthentication",
		"authorizationpolicies":            "authorizationpolicy",
		"knativeservices":                  "knativeservice",
		"configurations":                   "knativeconfiguration",
		"revisions":                        "knativerevision",
		"routes":                           "knativeroute",
		"brokers":                          "broker",
		"triggers":                         "trigger",
		"pingsources":                      "pingsource",
		"apiserversources":                 "apiserversource",
		"containersources":                 "containersource",
		"sinkbindings":                     "sinkbinding",
		"channels":                         "channel",
		"ingressroutes":                    "ingressroute", // Traefik
		"ingressroutetcps":                 "ingressroutetcp",
		"ingressrouteudps":                 "ingressrouteudp",
		"middlewares":                      "middleware",
		"middlewaretcps":                   "middlewaretcp",
		"traefikservices":                  "traefikservice",
		"serverstransports":                "serverstransport",
		"serverstransporttcps":             "serverstransporttcp",
		"tlsoptions":                       "tlsoption",
		"tlsstores":                        "tlsstore",
		"httpproxies":                      "httpproxy", // Contour
		"persistentvolumes":                "persistentvolume",
		"pvs":                              "persistentvolume",
		"storageclasses":                   "storageclass",
		"poddisruptionbudgets":             "poddisruptionbudget",
		"pdbs":                             "poddisruptionbudget",
		"networkpolicies":                  "networkpolicy",
		"netpol":                           "networkpolicy",
		"ciliumnetworkpolicies":            "ciliumnetworkpolicy",
		"ciliumclusterwidenetworkpolicies": "ciliumclusterwidenetworkpolicy",
		"clusternetworkpolicies":           "clusternetworkpolicy",
		"verticalpodautoscalers":           "verticalpodautoscaler",
		"servicemonitors":                  "servicemonitor",
		"podmonitors":                      "podmonitor",
		"vpas":                             "verticalpodautoscaler",
		"nodes":                            "node",
		"clusterclasses":                   "clusterclass",        // Cluster API
		"machines":                         "machine",             // Cluster API
		"machinesets":                      "machineset",          // Cluster API
		"machinedeployments":               "machinedeployment",   // Cluster API
		"machinepools":                     "machinepool",         // Cluster API
		"kubeadmcontrolplanes":             "kubeadmcontrolplane", // Cluster API
		"machinehealthchecks":              "machinehealthcheck",  // Cluster API
		"resourceclaims":                   "resourceclaim",       // DRA
		"resourceclaimtemplates":           "resourceclaimtemplate",
		"deviceclasses":                    "deviceclass",
		"resourceslices":                   "resourceslice",
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

	parts := strings.Split(nodeID, "/")
	if len(parts) < 3 {
		return nil
	}

	kind := parts[0]
	namespace := parts[1]
	name := parts[2]
	group := ""
	normalizedKind := ""
	if strings.EqualFold(kind, "nodeclass") && len(parts) >= 5 {
		group = parts[3]
		normalizedKind = normalizeKindWithGroup(parts[4], group, dp)
	}

	// Skip PodGroup - it's a UI grouping concept, not a real K8s resource
	if strings.ToLower(kind) == "podgroup" {
		return nil
	}
	if normalizedKind == "" {
		normalizedKind = normalizeKind(kind, dp)
	}

	return &ResourceRef{
		Kind:      normalizedKind,
		Namespace: namespace,
		Name:      name,
		Group:     group,
	}
}

func normalizeKindWithGroup(kind, group string, dp DynamicProvider) string {
	if dp != nil && group != "" {
		if gvr, ok := dp.GetGVRWithGroup(kind, group); ok {
			if resolved := dp.GetKindForGVR(gvr); resolved != "" {
				return resolved
			}
		}
	}
	return normalizeKind(kind, dp)
}

// normalizeKind converts internal kind format to display format
func normalizeKind(kind string, dp DynamicProvider) string {
	switch strings.ToLower(kind) {
	case "caliconetworkpolicy", "caliconetworkpolicies":
		return string(KindCalicoNetworkPolicy)
	case "calicoglobalnetworkpolicy", "globalnetworkpolicy", "globalnetworkpolicies":
		return string(KindCalicoGlobalNetworkPolicy)
	case "calicostagednetworkpolicy", "stagednetworkpolicy", "stagednetworkpolicies":
		return string(KindCalicoStagedNetworkPolicy)
	case "calicostagedglobalnetworkpolicy", "stagedglobalnetworkpolicy", "stagedglobalnetworkpolicies":
		return string(KindCalicoStagedGlobalNetworkPolicy)
	case "calicostagedkubernetesnetworkpolicy", "stagedkubernetesnetworkpolicy", "stagedkubernetesnetworkpolicies":
		return string(KindCalicoStagedKubernetesNetworkPolicy)
	}
	kindMap := map[string]string{
		"pod":                            "Pod",
		"service":                        "Service",
		"deployment":                     "Deployment",
		"rollout":                        "Rollout",
		"daemonset":                      "DaemonSet",
		"statefulset":                    "StatefulSet",
		"replicaset":                     "ReplicaSet",
		"ingress":                        "Ingress",
		"gateway":                        "Gateway",
		"httproute":                      "HTTPRoute",
		"grpcroute":                      "GRPCRoute",
		"tcproute":                       "TCPRoute",
		"tlsroute":                       "TLSRoute",
		"configmap":                      "ConfigMap",
		"secret":                         "Secret",
		"serviceaccount":                 "ServiceAccount",
		"sealedsecret":                   "SealedSecret",
		"servicemonitor":                 "ServiceMonitor",
		"podmonitor":                     "PodMonitor",
		"horizontalpodautoscaler":        "HorizontalPodAutoscaler",
		"job":                            "Job",
		"cronjob":                        "CronJob",
		"persistentvolumeclaim":          "PersistentVolumeClaim",
		"podgroup":                       "PodGroup",
		"application":                    "Application",
		"kustomization":                  "Kustomization",
		"helmrelease":                    "HelmRelease",
		"gitrepository":                  "GitRepository",
		"certificate":                    "Certificate",
		"issuer":                         "Issuer",
		"clusterissuer":                  "ClusterIssuer",
		"node":                           "Node",
		"nodepool":                       "NodePool",
		"nodeclaim":                      "NodeClaim",
		"nodeclass":                      "NodeClass",
		"scaledobject":                   "ScaledObject",
		"scaledjob":                      "ScaledJob",
		"gatewayclass":                   "GatewayClass",
		"istiogateway":                   "Gateway",
		"knativeservice":                 "KnativeService",
		"knativeconfiguration":           "Configuration",
		"knativerevision":                "Revision",
		"knativeroute":                   "Route",
		"broker":                         "Broker",
		"trigger":                        "Trigger",
		"pingsource":                     "PingSource",
		"apiserversource":                "ApiServerSource",
		"containersource":                "ContainerSource",
		"sinkbinding":                    "SinkBinding",
		"channel":                        "Channel",
		"ingressroute":                   "IngressRoute", // Traefik
		"ingressroutetcp":                "IngressRouteTCP",
		"ingressrouteudp":                "IngressRouteUDP",
		"middleware":                     "Middleware",
		"middlewaretcp":                  "MiddlewareTCP",
		"traefikservice":                 "TraefikService",
		"serverstransport":               "ServersTransport",
		"serverstransporttcp":            "ServersTransportTCP",
		"tlsoption":                      "TLSOption",
		"tlsstore":                       "TLSStore",
		"httpproxy":                      "HTTPProxy", // Contour
		"internet":                       "Internet",
		"persistentvolume":               "PersistentVolume",
		"storageclass":                   "StorageClass",
		"poddisruptionbudget":            "PodDisruptionBudget",
		"networkpolicy":                  "NetworkPolicy",
		"ciliumnetworkpolicy":            "CiliumNetworkPolicy",
		"ciliumclusterwidenetworkpolicy": "CiliumClusterwideNetworkPolicy",
		"clusternetworkpolicy":           "ClusterNetworkPolicy",
		"verticalpodautoscaler":          "VerticalPodAutoscaler",
		"capicluster":                    "Cluster",             // Cluster API
		"clusterclass":                   "ClusterClass",        // Cluster API
		"machine":                        "Machine",             // Cluster API
		"machineset":                     "MachineSet",          // Cluster API
		"machinedeployment":              "MachineDeployment",   // Cluster API
		"machinepool":                    "MachinePool",         // Cluster API
		"kubeadmcontrolplane":            "KubeadmControlPlane", // Cluster API
		"machinehealthcheck":             "MachineHealthCheck",  // Cluster API
		"resourceclaim":                  "ResourceClaim",       // DRA
		"resourceclaimtemplate":          "ResourceClaimTemplate",
		"deviceclass":                    "DeviceClass",
		"resourceslice":                  "ResourceSlice",
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
