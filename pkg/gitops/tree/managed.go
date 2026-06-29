package tree

import (
	"sort"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// Argo CD's per-resource tracking conventions.
//
// Modern Argo (default since Argo CD 2.x) stamps every managed resource with
// the tracking-id annotation. The value format is:
//
//   <app-name>:<group>/<kind>:<namespace>/<name>
//
// Legacy Argo configurations (operator opt-in via the
// `--application-instance-name` controller flag, or older deployments) use a
// label instead. The label key is configurable, but the overwhelmingly
// common value is `app.kubernetes.io/instance`. Custom keys aren't tried
// here — the caller can pass the value through `extraLabels` if they need
// to support a custom convention.
const (
	ArgoTrackingAnnotation = "argocd.argoproj.io/tracking-id"
	ArgoInstanceLabel      = "app.kubernetes.io/instance"
)

// ArgoTrackingMatches reports whether the given resource carries an Argo CD
// tracking identifier whose application name matches `app`.
//
// Returns true when EITHER:
//   - annotation `argocd.argoproj.io/tracking-id` starts with `<app>:`
//     (modern Argo). The colon anchor matters — without it `web` would
//     accidentally claim `web-staging`'s resources.
//   - label `app.kubernetes.io/instance == <app>` (legacy / operator
//     opt-in).
func ArgoTrackingMatches(obj *unstructured.Unstructured, app string) bool {
	if obj == nil || app == "" {
		return false
	}
	if ann := obj.GetAnnotations(); ann != nil {
		if tid, ok := ann[ArgoTrackingAnnotation]; ok && strings.HasPrefix(tid, app+":") {
			return true
		}
	}
	if lbl := obj.GetLabels(); lbl != nil {
		if v, ok := lbl[ArgoInstanceLabel]; ok && v == app {
			return true
		}
	}
	return false
}

// BuildManagedTree assembles a `ResourceTree` from a flat list of resources
// already filtered to those managed by the named Argo Application. Used by
// the /api/gitops/managed-resources endpoint to render the destination-side
// view of an Argo app when the controller lives in a different cluster.
//
// The root is a synthetic Application node — we never had the actual
// Argo Application CRD on this cluster (that's the whole point: we're on
// the destination side). The caller passes `appNamespace` only as a hint
// for display; the synthetic root's namespace doesn't gate any RBAC and
// isn't required to match a real namespace.
//
// Each matched object becomes a `RoleDeclared` child with a single `owns`
// edge from the synthetic root. Per-node Sync defaults to "Synced" (the
// resource exists in-cluster, so Argo's reconcile loop has at least
// applied it once); Health is left to enrichNodeFromObject's per-kind
// derivation from .status.
func BuildManagedTree(app, appNamespace string, matched []*unstructured.Unstructured) *ResourceTree {
	rootRef := ResourceRef{
		Group:     "argoproj.io",
		Kind:      "Application",
		Namespace: appNamespace,
		Name:      app,
	}
	root := Node{
		ID:   nodeID(rootRef),
		Ref:  rootRef,
		Role: RoleRoot,
		Tool: ToolArgoCD,
		Data: map[string]any{
			"namespace": appNamespace,
			"group":     rootRef.Group,
		},
	}

	nodes := make([]Node, 0, len(matched)+1)
	nodes = append(nodes, root)
	edges := make([]Edge, 0, len(matched))

	// Stable order: namespace then kind then name. Mirrors the sort the
	// regular tree builder applies in materialize() (role-first there, but
	// here every non-root is RoleDeclared so role-first collapses to
	// kind-then-name).
	sort.Slice(matched, func(i, j int) bool {
		if ni, nj := matched[i].GetNamespace(), matched[j].GetNamespace(); ni != nj {
			return ni < nj
		}
		if ki, kj := matched[i].GetKind(), matched[j].GetKind(); ki != kj {
			return ki < kj
		}
		return matched[i].GetName() < matched[j].GetName()
	})

	for _, obj := range matched {
		ref := ResourceRef{
			Group:     apiGroup(obj),
			Kind:      obj.GetKind(),
			Namespace: obj.GetNamespace(),
			Name:      obj.GetName(),
			UID:       string(obj.GetUID()),
		}
		node := Node{
			ID:   nodeID(ref),
			Ref:  ref,
			Role: RoleDeclared,
			Tool: ToolArgoCD,
			// Default Sync to Synced — the resource exists in-cluster, so
			// reconcile has applied at least once. Drift (live vs git) can
			// only be computed against the controller's source; the
			// destination view doesn't know.
			//
			// Health is intentionally left unset here. Per-kind health
			// derivation (Pod phase, Deployment readiness, etc.) requires
			// either a status walker or topology-level signal; both live
			// on the controller-side tree builder and aren't replicated
			// here. Surfacing per-resource health from the destination
			// view is V2 — see managed-resources docs for the rationale.
			Sync: "Synced",
			Data: map[string]any{"namespace": ref.Namespace, "group": ref.Group},
		}
		node = enrichNodeFromObject(node, obj)
		nodes = append(nodes, node)
		edges = append(edges, Edge{
			Source: root.ID,
			Target: node.ID,
			Type:   EdgeOwns,
		})
	}

	return &ResourceTree{
		Root:    root,
		Nodes:   nodes,
		Edges:   edges,
		Summary: summarize(nodes),
	}
}
