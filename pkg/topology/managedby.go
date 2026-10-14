package topology

import (
	"strings"

	"github.com/skyhook-io/radar/pkg/resourceid"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// GitOps / Helm ownership signal keys. Detection precedence mirrors
// packages/k8s-ui/src/utils/gitops-owner.ts so server-side ManagedBy synthesis
// agrees with the legacy client-side derivation. T14 retires the frontend copy
// once UI consumes Relationships.ManagedBy from the server.
const (
	argoTrackingIDAnnotation = "argocd.argoproj.io/tracking-id"
	argoInstanceLabel        = "argocd.argoproj.io/instance"
	fluxKustomizeNameLabel   = "kustomize.toolkit.fluxcd.io/name"
	fluxKustomizeNSLabel     = "kustomize.toolkit.fluxcd.io/namespace"
	fluxHelmNameLabel        = "helm.toolkit.fluxcd.io/name"
	fluxHelmNSLabel          = "helm.toolkit.fluxcd.io/namespace"
	helmReleaseNameAnno      = "meta.helm.sh/release-name"
	helmReleaseNSAnno        = "meta.helm.sh/release-namespace"
)

// API groups for the synthesized manager refs.
const (
	argoApplicationGroup = "argoproj.io"
	fluxKustomizeGroup   = "kustomize.toolkit.fluxcd.io"
	fluxHelmGroup        = "helm.toolkit.fluxcd.io"
)

// SynthesizeManagedBy returns the topmost meaningful manager(s) for the given
// resource. Detection order returns on the first hit:
//  1. Flux HelmRelease via helm.toolkit.fluxcd.io/{name,namespace} labels
//  2. Flux Kustomization via kustomize.toolkit.fluxcd.io/{name,namespace} labels
//  3. ArgoCD Application via argocd.argoproj.io/tracking-id annotation
//  4. ArgoCD Application via argocd.argoproj.io/instance label
//  5. Helm release via meta.helm.sh/release-name annotation
//  6. Topmost K8s owner via topology EdgeManages chain (Pod -> ReplicaSet -> Deployment)
//
// Cases 1-5 inspect the queried resource's own labels/annotations. Case 6 walks
// owner references via the topology graph. If obj is nil, only the topology
// owner walk runs.
//
// idx is the topology inverted edges index. When non-nil, the owner walk
// uses it for O(depth) lookup; when nil, the walk falls back to scanning
// topo.Edges (O(E) per call). High-fanout callers (T6/T89/T12 list/search
// enrichment) MUST pass a shared index — without it, the per-row managed-by
// path is O(N × E).
//
// Returns nil when no meaningful manager is detectable.
func SynthesizeManagedBy(obj metav1.Object, kind, namespace, name string, topo *Topology, dp DynamicProvider, idx *RelationshipsIndex) []ResourceRef {
	if idx == nil && topo != nil {
		idx = IndexByResource(topo)
	}
	objectKind, group := objectGVK(obj)
	if objectKind != "" {
		kind = objectKind
	} else if builtin, ok := resourceid.BuiltinForName(kind); ok {
		kind = builtin.Kind
	} else {
		kind = normalizeKind(kind, dp)
	}
	identity := resourceid.ResolveCurrent(resourceid.OptionalGroupReference(group, kind, namespace, name), nil).Ref
	target := ResourceRef{Kind: identity.Kind, Group: identity.Group, Namespace: namespace, Name: name}
	if ref := detectManagedByFromMeta(obj, idx, target); ref != nil {
		// detectManagedByFromMeta hand-sets Group for GitOps/Flux managers; do
		// NOT call enrichRef here — it would overwrite the deliberate group
		// with the result of a dp lookup keyed on the manager kind, which
		// resolves wrong for cross-group kinds like Flux's HelmRelease vs
		// native Helm.
		return []ResourceRef{*ref}
	}
	if top := walkTopmostOwner(kind, namespace, name, topo, dp, idx); top != nil {
		return []ResourceRef{*top}
	}
	return nil
}

func synthesizeManagedByFromNode(obj metav1.Object, nodeID string, target ResourceRef, topo *Topology, dp DynamicProvider, idx *RelationshipsIndex) []ResourceRef {
	if ref := detectManagedByFromMeta(obj, idx, target); ref != nil {
		return []ResourceRef{*ref}
	}
	if top := walkTopmostOwnerFromNodeID(nodeID, topo, dp, idx); top != nil {
		return []ResourceRef{*top}
	}
	return nil
}

// IsGitOpsManagerKind identifies the exact in-cluster GitOps ownership
// boundaries used by resource context and Applications. A manager's source
// or parent manager does not own each of its generated descendants.
func IsGitOpsManagerKind(kind NodeKind, group string) bool {
	switch kind {
	case KindApplication:
		return group == "argoproj.io"
	case KindKustomization:
		return group == "kustomize.toolkit.fluxcd.io"
	case KindHelmRelease:
		return group == "helm.toolkit.fluxcd.io"
	}
	return false
}

// AcceptsManagerHint checks Flux origin labels against observed inventory and ancestry.
// Origin labels remain useful when no inventory is available. Once an observed
// Flux controller records inventory (or targets another cluster), membership
// must follow the graph, including an owner chain for generated descendants.
func (idx *RelationshipsIndex) AcceptsManagerHint(ref, target ResourceRef, annotations map[string]string) bool {
	if idx == nil || (ref.Group != fluxHelmGroup && ref.Group != fluxKustomizeGroup) {
		return true
	}
	manager := idx.nodesByResourceKey[resourceid.ResourceKey(ref.Group, ref.Kind, ref.Namespace, ref.Name)]
	if manager == nil || !manager.gitopsManagementAuthoritative {
		return true
	}
	// Helm deliberately excludes hooks from managed inventory. Their origin
	// still identifies the release whose lifecycle created them; this does not
	// add an inventory edge or promise pruning. Remote hints cannot match here.
	if ref.Group == fluxHelmGroup && ref.Kind == "HelmRelease" && annotations["helm.sh/hook"] != "" && !manager.gitopsRemoteDestination {
		return true
	}
	targetKey := resourceid.ResourceKey(target.Group, target.Kind, target.Namespace, target.Name)
	if manager.managedResourceKeys[targetKey] {
		return true
	}
	node := idx.nodesByResourceKey[targetKey]
	if node == nil {
		return false
	}
	nodeID := node.ID
	seen := map[string]bool{nodeID: true}
	queue := []string{nodeID}
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		// The nearest manager is the ownership boundary. Its source or
		// parent manager is not the manager of generated descendants.
		if currentNode := idx.nodesByID[current]; current != nodeID && currentNode != nil && IsGitOpsManagerKind(currentNode.Kind, nodeAPIGroupFromData(currentNode)) {
			continue
		}
		incoming, _ := idx.EdgesFor(current)
		for _, edge := range incoming {
			if edge.Type != EdgeManages {
				continue
			}
			if edge.Source == manager.ID {
				return true
			}
			if !seen[edge.Source] {
				seen[edge.Source] = true
				queue = append(queue, edge.Source)
			}
		}
	}
	return false
}

// detectManagedByFromMeta inspects labels/annotations on obj for GitOps / Helm
// ownership signals and returns the strongest accepted manager ref. Inventory
// can reject a stale Flux signal without discarding weaker metadata evidence.
// Returns nil if no signal is accepted.
func detectManagedByFromMeta(obj metav1.Object, idx *RelationshipsIndex, target ResourceRef) *ResourceRef {
	if obj == nil {
		return nil
	}
	labels := obj.GetLabels()
	annos := obj.GetAnnotations()

	if name, ns := labels[fluxHelmNameLabel], labels[fluxHelmNSLabel]; name != "" && ns != "" {
		ref := ResourceRef{
			Kind:      "HelmRelease",
			Group:     fluxHelmGroup,
			Namespace: ns,
			Name:      name,
		}
		if idx.AcceptsManagerHint(ref, target, annos) {
			return &ref
		}
	}
	if name, ns := labels[fluxKustomizeNameLabel], labels[fluxKustomizeNSLabel]; name != "" && ns != "" {
		ref := ResourceRef{
			Kind:      "Kustomization",
			Group:     fluxKustomizeGroup,
			Namespace: ns,
			Name:      name,
		}
		if idx.AcceptsManagerHint(ref, target, annos) {
			return &ref
		}
	}

	if id := annos[argoTrackingIDAnnotation]; id != "" {
		if ns, n, ok := parseArgoTrackingID(id); ok {
			return &ResourceRef{
				Kind:      "Application",
				Group:     argoApplicationGroup,
				Namespace: ns,
				Name:      n,
			}
		}
	}
	if n := labels[argoInstanceLabel]; n != "" {
		return &ResourceRef{
			Kind:      "Application",
			Group:     argoApplicationGroup,
			Namespace: "",
			Name:      n,
		}
	}

	if n := annos[helmReleaseNameAnno]; n != "" {
		// Helm releases are stored as Secrets in the release namespace; ref
		// kind "HelmRelease" here is the *logical* Helm install, not the Flux
		// HelmRelease CR. Group left empty to distinguish from Flux's CR.
		return &ResourceRef{
			Kind:      "HelmRelease",
			Namespace: annos[helmReleaseNSAnno],
			Name:      n,
		}
	}

	return nil
}

// parseArgoTrackingID parses ArgoCD's tracking-id annotation in either format:
//
//	"<appName>:<group>/<kind>:<resourceNs>/<resourceName>"               (default)
//	"<appNamespace>_<appName>:<group>/<kind>:<resourceNs>/<resourceName>" (namespaced)
//
// Returns (namespace, name, ok). The legacy single-name form yields an empty
// namespace — callers route to a search or skip the link.
func parseArgoTrackingID(value string) (namespace, name string, ok bool) {
	firstColon := strings.Index(value, ":")
	if firstColon < 0 {
		return "", "", false
	}
	head := value[:firstColon]
	sep := strings.Index(head, "_")
	if sep < 0 {
		if head == "" {
			return "", "", false
		}
		return "", head, true
	}
	ns := head[:sep]
	n := head[sep+1:]
	if n == "" {
		return "", "", false
	}
	return ns, n, true
}

// walkTopmostOwner follows EdgeManages edges upward from the queried node
// until the first GitOps manager or final owner, returning that ResourceRef.
// Returns nil when the queried resource has no owner in the topology. Cycle-
// safe: stops at the first revisit.
//
// When idx is non-nil, each step is an O(in-degree) hop via the inverted edge
// index, making the whole walk O(depth × avg-in-degree). When idx is nil, the
// function falls back to scanning topo.Edges once to build a transient owners
// map (O(E)) — fine for single-resource calls, but high-fanout callers
// (list/search enrichment) MUST pass a shared index to avoid O(N × E).
func walkTopmostOwner(kind, namespace, name string, topo *Topology, dp DynamicProvider, idx *RelationshipsIndex) *ResourceRef {
	return walkTopmostOwnerFromNodeID(buildNodeID(kind, namespace, name, dp), topo, dp, idx)
}

func walkTopmostOwnerFromNodeID(startID string, topo *Topology, dp DynamicProvider, idx *RelationshipsIndex) *ResourceRef {
	if topo == nil {
		return nil
	}

	// Owner lookup for the current node: returns the source ID of the first
	// EdgeManages edge pointing at it, or "" if none. Multiple owners are
	// rare (K8s allows multiple ownerReferences but only one controller);
	// the first wins, matching ownerReferences[].controller==true semantics.
	var ownerOf func(target string) string
	if idx != nil {
		ownerOf = func(target string) string {
			incoming, _ := idx.EdgesFor(target)
			for _, e := range incoming {
				if e.Type == EdgeManages {
					return e.Source
				}
			}
			return ""
		}
	} else {
		// Fallback: one-time O(E) scan to build target->source map.
		owners := make(map[string]string, len(topo.Edges))
		for _, e := range topo.Edges {
			if e.Type != EdgeManages {
				continue
			}
			if _, exists := owners[e.Target]; !exists {
				owners[e.Target] = e.Source
			}
		}
		ownerOf = func(target string) string { return owners[target] }
	}

	// Seed visited with the starting node so a cycle that loops back to the
	// queried resource (A→B→A) returns the first found owner (B) instead of
	// walking one extra hop and producing a self-referential ManagedBy ref.
	cur := startID
	visited := map[string]bool{cur: true}
	var topRef *ResourceRef
	for {
		next := ownerOf(cur)
		if next == "" {
			break
		}
		if visited[next] {
			break
		}
		visited[next] = true
		var ref *ResourceRef
		if idx != nil {
			ref = resourceRefForNode(idx.nodesByID[next], dp)
		} else {
			ref = resourceRefForNodeID(next, topo, dp)
		}
		if ref == nil {
			break
		}
		topRef = ref
		if IsGitOpsManagerKind(NodeKind(ref.Kind), ref.Group) {
			break
		}
		cur = next
	}
	return topRef
}
