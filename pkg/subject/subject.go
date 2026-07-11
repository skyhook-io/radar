// Package subject is the shared resource-identity + grouping vocabulary for the
// platform. internal/issues is its production consumer today: it keys on
// ScopeForKind + StableID at depth-0 (see internal/issues/identity.go). The
// Tier-1 owner-walk and Tier-2 AppOverlay engine are the canonical model that
// pkg/topology's currently-independent logic (top_metrics.go:topOwnerForPod,
// pod_grouping.go:determineGroupKey, managedby.go) is meant to migrate ONTO —
// planned, not yet wired. Until that lands this package is the agreed
// vocabulary, NOT a second live "truth": topology still resolves its own
// grouping, so don't treat subject.Resolve as the running topology path yet.
//
//	Tier-1 Subject (owner-collapsed root controller, deterministic, label-free)
//	Tier-2 AppOverlay (declared-key 8-tier precedence, provenance/confidence/conflicts)
//
// PLACEMENT NOTE: pkg/subject imports only the canonical-key helper from
// pkg/resourceid (ResourceKey). It does NOT import internal/* or pkg/topology (the
// plan's layering rule). The Tier-1 owner walk is parameterized over an
// OwnerResolver interface — which a topology adapter must satisfy with a
// CONTROLLER-ownership resolver, NOT a raw walkTopmostOwner (see OwnerResolver's
// contract: EdgeManages also carries GitOps/Helm management edges, which are
// Tier-2, not identity). A heuristic single-pod walk is built in. Tier-2 takes
// only a metav1.Object's labels/annotations, so it has zero topology dependency.
package subject

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"

	"github.com/skyhook-io/radar/pkg/resourceid"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ============================= TIER 1: SUBJECT =============================

// Scope is the coarse bucket of a Subject — the UI section AND a hash input to
// the issue/check ID. Shared by issues + topology so they key on one enum.
// `unknown` is first-class.
type Scope string

const (
	ScopeUnknown  Scope = "unknown"
	ScopeWorkload Scope = "workload"
	ScopeService  Scope = "service"
	ScopeIngress  Scope = "ingress"
	ScopePVC      Scope = "pvc"
	ScopeNode     Scope = "node"
)

// Ref is the canonical {group,kind,namespace,name} identity. Field-identical to
// issues.Ref (a plain struct copy converts); topology.ResourceRef orders its
// fields differently and needs field-by-field conversion. Group empty => core group.
type Ref struct {
	Group     string `json:"group,omitempty"`
	Kind      string `json:"kind"`
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name"`
}

// Anchor names the explicit Tier-1 terminal bucket a Subject resolved into. It
// makes the bare-pod and static/mirror-pod terminals first-class instead of
// hiding them under "Pod", and records the operator-CR hop.
type Anchor string

const (
	AnchorOwnerCollapsed Anchor = "owner_collapsed" // Pod->RS->Deployment, Pod->Job->CronJob
	AnchorBare           Anchor = "bare"            // owner=nil  (was pod_grouping "standalone")
	AnchorNode           Anchor = "node"            // owner=Node (static/mirror pod)
	AnchorOperatorCR     Anchor = "operator_cr"     // CNPG Cluster / Strimzi Kafka / Crossplane XR
	AnchorSelf           Anchor = "self"            // non-pod resource is its own subject (CRDs, Service, PVC…)
)

// Subject is the deterministic Tier-1 identity spine. Ref is the owner-collapsed
// root controller; Scope = ScopeForKind(Ref.Kind); Anchor records HOW it
// resolved. Derived purely from CONTROLLER ownership (ownerReferences[].
// controller, or controllerRef-derived topology edges) — never from declarative
// "management" edges (Argo/Flux/Helm), which are Tier-2 AppOverlay.
type Subject struct {
	Ref    Ref
	Scope  Scope
	Anchor Anchor
}

// Key is the canonical group|kind|namespace|name string for s.Ref — a thin
// pass-through to pkg/resourceid.ResourceKey (NOT reinvented). This is the
// groupingKey fed to StableID; sharing it keeps issue grouping and audit
// deep-links from drifting.
func (s Subject) Key() string {
	return resourceid.ResourceKey(s.Ref.Group, s.Ref.Kind, s.Ref.Namespace, s.Ref.Name)
}

// ScopeForKind maps a Kind to its Scope, defaulting to unknown (NOT an error)
// for unrecognized kinds. Exported so checks can reuse it.
func ScopeForKind(kind string) Scope {
	switch kind {
	case "Pod", "Deployment", "Rollout", "StatefulSet", "DaemonSet", "ReplicaSet", "Job", "CronJob":
		return ScopeWorkload
	case "Service":
		return ScopeService
	case "Ingress":
		return ScopeIngress
	case "PersistentVolumeClaim":
		return ScopePVC
	case "Node":
		return ScopeNode
	}
	return ScopeUnknown
}

// OwnerResolver walks Tier-1 identity to the topmost controller WITHOUT
// pkg/subject importing pkg/topology.
//
// CONTRACT — Kubernetes CONTROLLER ownership only. ParentOf must follow the
// ownerReferences[].controller==true chain (Pod→ReplicaSet→Deployment,
// Pod→Job→CronJob, generated-workload→operator-CR). It MUST NOT follow
// declarative/application "management" relationships — an Argo Application,
// Flux Kustomization, or HelmRelease "manages" a Deployment, but it does NOT
// *own* it in the controller sense. Those belong in Tier-2 AppOverlay, never in
// the Subject. Collapsing a Deployment up to its Application here would erase
// the identity/overlay boundary the whole package exists to keep.
//
// CAUTION for a future topology adapter: do NOT wrap pkg/topology's
// walkTopmostOwner directly — it follows every EdgeManages edge, and EdgeManages
// is deliberately overloaded in the graph to include GitOps/Helm management
// edges (Argo Application→resource, GitRepository→Kustomization, …). Resolve
// from controllerReferences instead (the cache-aware k8s.topOwnerForPodResolved
// is the reference implementation), or filter the walk to controllerRef-derived
// edges. internal/issues passes an owner that is already controller-resolved by
// the detectors (a depth-0/1 walk).
//
// Returns the immediate controller-parent of the given ref, or (zero, false)
// when there is no further controller. Names already RS-hash-stripped per impl.
type OwnerResolver interface {
	// ParentOf returns the next CONTROLLER up the chain, or (zero, false) at root.
	ParentOf(child Ref) (parent Ref, ok bool)
}

// OperatorRootHook lets the resolver hop ONE level above an operator-generated
// workload to the operator CR (CNPG Cluster owns the StatefulSet, etc.). V1
// ships a curated allowlist impl (DefaultOperatorRoots); unknown operator CRs
// return false and degrade to the workload (raw-always).
type OperatorRootHook interface {
	// RootFor returns the operator-CR root owning `workload`, if recognized.
	RootFor(workload Ref) (root Ref, ok bool)
}

// maxOwnerWalkDepth bounds the multi-hop owner walk. Real K8s ownership chains
// are shallow (Pod->RS->Deployment is depth 2); a depth this high can only be
// reached by a corrupted/cyclic topology, and the built-in visited-set already
// stops cycles — this is belt-and-suspenders.
const maxOwnerWalkDepth = 16

// ResolveSubject is the Tier-1 entrypoint. It collapses ownerReferences to the
// root controller and classifies the terminal Anchor. It SUBSUMES:
//   - topOwnerForPod's single-hop RS->Deployment + name-strip (generalized to a
//     multi-hop walk via OwnerResolver), AND extends it with Job->CronJob,
//     owner=nil -> AnchorBare, owner=Node -> AnchorNode, operator-CR root hop.
//   - enrichIdentity / foldGroup's owner-else-self collapse (the single hop is
//     just a depth-1 walk with no further parent).
//
// Pass owners=nil for the pure "resource is its own subject" path. The
// owner-walk inputs (topo/dp/idx) are bound inside the OwnerResolver the caller
// injects, so this signature stays layering-clean.
func ResolveSubject(start Ref, owners OwnerResolver, ops OperatorRootHook) Subject {
	cur := start
	anchor := AnchorSelf

	if owners != nil {
		visited := map[string]bool{refKey(cur): true}
		chain := []Ref{cur}
		for i := 0; i < maxOwnerWalkDepth; i++ {
			parent, ok := owners.ParentOf(cur)
			if !ok {
				break
			}
			// owner=Node is a terminal bucket for static/mirror pods — never
			// collapse "up into" the Node as if it owned a workload.
			if parent.Kind == "Node" {
				anchor = AnchorNode
				break
			}
			if visited[refKey(parent)] {
				// Corrupt ownership cycle (e.g. A→B→A). A canonical identity must
				// be start-independent, so collapse every member to ONE
				// deterministic representative — the lexicographically smallest
				// key walked — instead of returning the last hop, which would
				// depend on where the walk began (resolving A→B but B→A).
				cur = minRef(chain)
				anchor = AnchorOwnerCollapsed
				break
			}
			visited[refKey(parent)] = true
			chain = append(chain, parent)
			cur = parent
			anchor = AnchorOwnerCollapsed
		}
	}

	// A pod with no owner (and no owner=Node terminal) is bare/standalone.
	if anchor == AnchorSelf && start.Kind == "Pod" {
		anchor = AnchorBare
	}

	// Operator-CR root hop: one level ABOVE the generated workload. Only when
	// the resolved controller is itself owned by a recognized operator CR.
	if ops != nil && anchor != AnchorNode {
		if root, ok := ops.RootFor(cur); ok {
			cur = root
			anchor = AnchorOperatorCR
		}
	}

	return Subject{
		Ref:    cur,
		Scope:  ScopeForKind(cur.Kind),
		Anchor: anchor,
	}
}

func refKey(r Ref) string {
	return resourceid.ResourceKey(r.Group, r.Kind, r.Namespace, r.Name)
}

// minRef returns the ref with the lexicographically smallest key — the
// deterministic, start-independent representative of an ownership cycle.
func minRef(refs []Ref) Ref {
	best := refs[0]
	bestKey := refKey(best)
	for _, r := range refs[1:] {
		if k := refKey(r); k < bestKey {
			best, bestKey = r, k
		}
	}
	return best
}

// HeuristicPodOwnerResolver is the built-in single-pod OwnerResolver: walks
// pod.OwnerReferences[].controller==true (controller-only, per the contract;
// pods with no controller ref resolve to no owner), RS->Deployment +
// StripReplicaSetHash, Job stays Job (CronJob hop handled by the multi-hop loop
// when a topology OwnerResolver provides the Job->CronJob parent).
//
// It resolves a parent ONLY for the Pod itself: it returns the Pod's controller
// once, then (zero, false) for anything else, since a bare Pod's
// OwnerReferences only describe one hop. Job->CronJob and deeper chains require
// a topology-backed OwnerResolver.
//
// APPROXIMATION (read before using): for a ReplicaSet owner it assumes
// RS->Deployment by stripping the pod-template hash. That is WRONG for
// ReplicaSets owned by an Argo Rollout or any other CRD controller — it
// fabricates a Deployment that doesn't exist (the exact phantom-owner class
// fixed in the cache-aware k8s.topOwnerForPodResolved path). Use this only as
// the label-free, no-cache fallback; when a cache/topology is available, inject
// an OwnerResolver that resolves the ReplicaSet's *real* controller instead.
type HeuristicPodOwnerResolver struct{ Pod metav1.Object }

func (p HeuristicPodOwnerResolver) ParentOf(child Ref) (Ref, bool) {
	if p.Pod == nil {
		return Ref{}, false
	}
	// Only the pod itself has a known parent in this single-pod resolver.
	if child.Kind != "Pod" || child.Name != p.Pod.GetName() || child.Namespace != p.Pod.GetNamespace() {
		return Ref{}, false
	}
	refs := p.Pod.GetOwnerReferences()
	pick := func(ref metav1.OwnerReference) (Ref, bool) {
		if ref.Kind == "ReplicaSet" {
			return Ref{Group: "apps", Kind: "Deployment", Namespace: p.Pod.GetNamespace(), Name: StripReplicaSetHash(ref.Name)}, true
		}
		return Ref{Group: groupFromAPIVersion(ref.APIVersion), Kind: ref.Kind, Namespace: p.Pod.GetNamespace(), Name: ref.Name}, true
	}
	for _, ref := range refs {
		if ref.Controller != nil && *ref.Controller {
			return pick(ref)
		}
	}
	// Controller-only, per the OwnerResolver contract: a pod with only
	// NON-controller ownerRefs has no canonical owner — it is its own subject.
	// (topOwnerForPod historically fell back to refs[0], collapsing pods under
	// an arbitrary non-controller owner; that is exactly the "management edge as
	// identity" the contract forbids, so it's dropped here.)
	return Ref{}, false
}

// groupFromAPIVersion extracts the API group from an ownerReference APIVersion
// ("apps/v1" -> "apps", "v1" -> ""). Pods own no native group-bearing
// references beyond apps/batch, but operator CRs (CNPG, Strimzi) carry their
// group here, which the operator-root hook keys on.
func groupFromAPIVersion(apiVersion string) string {
	if i := strings.Index(apiVersion, "/"); i >= 0 {
		return apiVersion[:i]
	}
	return ""
}

// StripReplicaSetHash moves verbatim from top_metrics.go (idx<=0 guard preserved).
func StripReplicaSetHash(name string) string {
	idx := strings.LastIndex(name, "-")
	if idx <= 0 {
		return name
	}
	return name[:idx]
}

// ============================ DETERMINISTIC IDs ============================

// StableID is the generic deterministic identity hash: sha256 of
// scope + "\x00" + groupingKey + "\x00" + discriminator, truncated [:8], hex.
// Subject produces subject identity; a surface composes its own ID from a
// subject key + a discriminator (Issues uses category; Checks could use a
// check-id). The exact byte layout is load-bearing — any change re-keys every
// existing record — so it must not be altered.
func StableID(scope Scope, groupingKey, discriminator string) string {
	sum := sha256.Sum256([]byte(string(scope) + "\x00" + groupingKey + "\x00" + discriminator))
	return hex.EncodeToString(sum[:8])
}

// ============================ UNIFIED ENTRYPOINT ============================

// Resolved is what every surface keys off: the deterministic Subject + optional
// overlay. App is nil whenever raw wins.
type Resolved struct {
	Subject Subject
	App     *AppOverlay
}

// Resolve composes Tier-1 + Tier-2. Tier-1 always populates Subject (never
// fails, never needs a label). Tier-2 is best-effort.
//
// IMPORTANT — the two inputs feed different tiers and are NOT interchangeable:
//   - owners drives ownership (Tier-1). obj is NOT consulted for the owner walk;
//     pass owners=nil only when start truly has no controller (Resolve(ref, obj,
//     nil, …) yields a bare/self Subject even if obj carries ownerRefs). To walk
//     a pod's owners, inject a resolver — HeuristicPodOwnerResolver{Pod: obj} for
//     the no-cache path, or a cache/controllerRef-backed one in production.
//   - obj supplies ONLY the labels/annotations for the Tier-2 overlay.
func Resolve(start Ref, obj metav1.Object, owners OwnerResolver, ops OperatorRootHook, allowBareApp bool) Resolved {
	return Resolved{
		Subject: ResolveSubject(start, owners, ops),
		App:     ResolveOverlay(obj, allowBareApp),
	}
}
