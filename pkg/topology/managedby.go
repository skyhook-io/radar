package topology

import (
	"strings"

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
	if ref := detectManagedByFromMeta(obj); ref != nil {
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

// detectManagedByFromMeta inspects labels/annotations on obj for GitOps / Helm
// ownership signals and returns the implied manager ref. Returns nil if no
// signal is present. Mirrors detectGitOpsOwner precedence in the web package.
func detectManagedByFromMeta(obj metav1.Object) *ResourceRef {
	if obj == nil {
		return nil
	}
	labels := obj.GetLabels()
	annos := obj.GetAnnotations()

	if name, ns := labels[fluxHelmNameLabel], labels[fluxHelmNSLabel]; name != "" && ns != "" {
		return &ResourceRef{
			Kind:      "HelmRelease",
			Group:     fluxHelmGroup,
			Namespace: ns,
			Name:      name,
		}
	}
	if name, ns := labels[fluxKustomizeNameLabel], labels[fluxKustomizeNSLabel]; name != "" && ns != "" {
		return &ResourceRef{
			Kind:      "Kustomization",
			Group:     fluxKustomizeGroup,
			Namespace: ns,
			Name:      name,
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
// until no further owner exists, returning the topmost ancestor's ResourceRef.
// Returns nil when the queried resource has no owner in the topology. Cycle-
// safe: stops at the first revisit.
//
// When idx is non-nil, each step is an O(in-degree) hop via the inverted edge
// index, making the whole walk O(depth × avg-in-degree). When idx is nil, the
// function falls back to scanning topo.Edges once to build a transient owners
// map (O(E)) — fine for single-resource calls, but high-fanout callers
// (list/search enrichment) MUST pass a shared index to avoid O(N × E).
func walkTopmostOwner(kind, namespace, name string, topo *Topology, dp DynamicProvider, idx *RelationshipsIndex) *ResourceRef {
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
	cur := buildNodeID(kind, namespace, name, dp)
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
		ref := parseNodeID(next, dp)
		if ref == nil {
			break
		}
		enrichRef(ref, dp)
		topRef = ref
		cur = next
	}
	return topRef
}
