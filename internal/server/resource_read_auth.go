package server

import (
	"fmt"
	"net/http"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/readscope"
	"github.com/skyhook-io/radar/pkg/readreason"
	"github.com/skyhook-io/radar/pkg/resourceapi"
)

// listAuthorization is what a list may read after the per-user RBAC gates:
// the namespaces (nil: every namespace), and the caller's gaps within the
// namespaces they can see — the kind denied by exact authorization there, or
// a check that couldn't complete.
type listAuthorization struct {
	namespaces []string
	gaps       []resourceapi.Gap
}

// preflightResourceList runs the per-user RBAC gates shared by the REST
// (/api/resources/{kind}, counts) and AI (/api/ai/resources/{kind}) list
// paths. It assumes the caller has already populated `namespaces` via
// parseNamespacesForUser (which primes the canI cache that canRead relies on).
//
// A failure means nothing may be read: a denied cluster-scoped kind, no
// namespace access, or the kind denied in every namespace. Otherwise Secrets, Roles, RoleBindings,
// LimitRanges and Trivy reports — which need the caller's exact list permission, not
// namespace visibility — are narrowed to the namespaces it allows, and the
// rest become gaps.
func (s *Server) preflightResourceList(r *http.Request, kind, group string, namespaces []string) (listAuthorization, *resourceReadFailure) {
	// "namespaces" is cluster-scoped at the K8s API. Full Namespace objects
	// (labels, annotations, spec) require explicit list-namespaces SAR.
	// AllowedNamespaces is NOT a sufficient fallback: list-pods-in-alpha
	// SAR-confirms namespace existence and pod read access, not get-namespace-
	// alpha (which would require ClusterRole on namespaces). The namespace
	// picker uses /api/namespaces, which serves a synthesized {name, status}
	// view filtered by AllowedNamespaces — restricted users keep their picker
	// without leaking Namespace metadata via this resource-browser path.
	if kind == "namespaces" || kind == "namespace" {
		if allowed, checked := s.canReadDecision(r, "", "namespaces", "", "list"); !allowed {
			return listAuthorization{}, permissionFailure(checked, "insufficient permissions to list namespaces")
		}
		return listAuthorization{}, nil
	}

	// Cluster-only kinds (Nodes, PVs, StorageClasses, ClusterRoles, cluster-
	// scoped CRDs) have no namespace dimension — gate via SAR. Run BEFORE the
	// readscope.NoNamespaceAccess check so a user with explicit cluster-scoped RBAC but
	// no namespace access can still read those resources.
	isClusterScoped, gvrGroup, gvrResource := k8s.ClassifyKindScope(kind, group)
	if isClusterScoped {
		if allowed, checked := s.canReadDecision(r, gvrGroup, gvrResource, "", "list"); !allowed {
			return listAuthorization{}, permissionFailure(checked, fmt.Sprintf("insufficient permissions to list %s", kind))
		}
		// Cluster-scoped reads have no namespace dimension. Once the
		// resource-level SAR passes, force the later typed/dynamic cache paths
		// through their cluster-wide branch even if the user also has a
		// namespace view preference.
		return listAuthorization{}, nil
	}

	if readscope.NoNamespaceAccess(namespaces) {
		return listAuthorization{namespaces: namespaces}, readFailuref(readreason.UserNoNamespaceAccess, "no namespace access")
	}

	// Per-kind RBAC inside a namespace. Helm release storage IS K8s Secrets,
	// so the chart can grant the SA cluster-wide secrets (rbac.secrets,
	// rbac.helm, auth.mode != "none", or cloud.enabled — see deploy/helm/
	// radar/templates/clusterrole.yaml). When any of those triggers fires
	// the cache holds every secret in the cluster, so per-user RBAC must
	// gate the read. Roles, RoleBindings, LimitRanges and Trivy Operator reports (an
	// ExposedSecretReport quotes the leaked credential) get the same exact
	// gate. Other namespaced kinds use namespace visibility.
	exactGroup := group
	if kind == "secrets" || kind == "secret" {
		exactGroup = ""
	}
	resourceGroup, resource := k8s.ExactNamespaceAccessResource(kind, exactGroup)
	if resource == "" {
		return listAuthorization{namespaces: namespaces}, nil
	}
	if namespaces == nil {
		// A caller with cluster-wide namespace access (e.g. picked up via
		// DiscoverNamespaces stage 1: cluster-wide list pods) reads every
		// namespace's objects — gate on the cluster-scope SAR.
		if allowed, checked := s.canReadDecision(r, resourceGroup, resource, "", "list"); !allowed {
			return listAuthorization{}, permissionFailure(checked, fmt.Sprintf("insufficient permissions to list %s", resource))
		}
		return listAuthorization{}, nil
	}
	allowed, denied, unchecked := s.partitionNamespacesByCanRead(r, resourceGroup, resource, "list", namespaces)
	authz := listAuthorization{namespaces: allowed}
	if len(denied) > 0 {
		authz.gaps = append(authz.gaps, gapOf(readreason.UserForbidden, denied))
	}
	if len(unchecked) > 0 {
		authz.gaps = append(authz.gaps, gapOf(readreason.UserPermissionCheckFailed, unchecked))
	}
	if len(allowed) == 0 {
		// authz.gaps say which namespaces; the caller's target carries them.
		return authz, permissionFailure(len(unchecked) == 0, fmt.Sprintf("insufficient permissions to list %s", resource))
	}
	return authz, nil
}

// preflightResourceGet runs the per-user RBAC gates that must pass before any
// single-resource GET fetch. Mirrors the kind/scope-aware logic used by both
// the REST handler (handleGetResource) and the AI handler (handleAIGetResource)
// so future RBAC adjustments stay in lockstep across both surfaces.
//
// Inputs are the already-normalized (kind, namespace, name, group); callers
// must collapse the cluster-scoped "_" placeholder before calling. Returns
// nil when the request passes the gates, or the coded failure the caller
// writes.
//
// Three gates, run in this order:
//  1. kind == "namespaces"        → full Namespace object requires get-namespaces SAR
//  2. cluster-scoped (Node/CRD/…) → per-kind get SAR (ClassifyKindScope)
//  3. namespaced                   → namespace access via getUserNamespaces,
//     namespace_not_found for a caller who can see every namespace, then
//     per-namespace get SAR for Secrets, Roles, RoleBindings, LimitRanges and Trivy reports
func (s *Server) preflightResourceGet(r *http.Request, kind, namespace, name, group string) *resourceReadFailure {
	return s.preflightResourceGetFrom(r, readCache(), kind, namespace, name, group)
}

func (s *Server) preflightResourceGetFrom(r *http.Request, cache *k8s.ResourceCache, kind, namespace, name, group string) (failure *resourceReadFailure) {
	defer func() {
		if readCache() != cache {
			failure = readFailuref(readreason.ConnectionChanged, "%s", errReadHandlesChanged)
		}
	}()

	isNamespacesKind := kind == "namespaces" || kind == "namespace"
	isClusterScoped, gvrGroup, gvrResource := k8s.ClassifyKindScope(kind, group)
	switch {
	case isNamespacesKind:
		// Full Namespace object access requires explicit get-namespaces SAR.
		// Read access to resources IN a namespace (list pods etc.) does not
		// imply read access to the Namespace object itself. Restricted users
		// without ClusterRole on namespaces get 403 here.
		if allowed, checked := s.canReadDecision(r, "", "namespaces", "", "get"); !allowed {
			return permissionFailure(checked, fmt.Sprintf("no access to namespace %q", name))
		}
	case isClusterScoped:
		if allowed, checked := s.canReadDecision(r, gvrGroup, gvrResource, "", "get"); !allowed {
			return permissionFailure(checked, fmt.Sprintf("no access to %s (cluster-scoped resource requires explicit RBAC)", kind))
		}
	case namespace != "":
		selection, code := s.requestNamespaceSelection(r, cache, []string{namespace})
		if code == readreason.UserPermissionCheckFailed {
			return readFailuref(code, "Radar could not determine your namespace access; retry when the permission check is available")
		}
		if len(selection.Gaps) > 0 {
			switch selection.Gaps[0].Code {
			case readreason.UserNamespaceNotVisible:
				return readFailuref(readreason.UserNamespaceNotVisible, "namespace %q is not among the namespaces you can see, or does not exist", namespace)
			case readreason.NamespaceNotFound:
				return readFailuref(readreason.NamespaceNotFound, "namespace %q doesn't exist in this cluster", namespace)
			}
		}
		if gvr, ok := k8s.BuiltinGVRAnyGroup(kind); ok && k8s.RequiresExactNamespaceAccess(gvr) && (group == "" || group == gvr.Group || gvr.Resource == "secrets") {
			if allowed, checked := s.canReadDecision(r, gvr.Group, gvr.Resource, namespace, "get"); !allowed {
				return permissionFailure(checked, fmt.Sprintf("no access to %s in namespace %q", gvr.Resource, namespace))
			}
		}
		if gvr, ok := k8s.TrivyReportGVR(kind, group); ok && k8s.RequiresExactNamespaceAccess(gvr) {
			if allowed, checked := s.canReadDecision(r, gvr.Group, gvr.Resource, namespace, "get"); !allowed {
				return permissionFailure(checked, fmt.Sprintf("no access to %s in namespace %q", kind, namespace))
			}
		}
	default:
		// Empty namespace and not a recognized cluster-scoped kind: an empty
		// namespace means the target is cluster-scoped, but ClassifyKindScope
		// couldn't identify it (an undiscovered CRD), so no SAR ran. While
		// discovery has not initialized yet (progressive startup, context
		// switch), "unrecognized" means "not discovered yet", not "does not
		// exist" — answer retryable rather than a terminal error for a deep
		// link that resolves seconds later. Still fail closed either way: the
		// resource is never served ungated.
		if k8s.GetResourceDiscovery() == nil {
			return readFailuref(readreason.ClusterConnecting, "%s is not discovered yet, please retry shortly", kind)
		}
		return readFailuref(readreason.KindUnknown, "cannot verify access to %q (unrecognized cluster-scoped resource)", kind)
	}
	return nil
}

func (s *Server) requestNamespaceSelection(r *http.Request, cache *k8s.ResourceCache, requested []string) (readscope.Selection, readreason.Code) {
	ceiling, failed := s.getUserNamespacesWithStatus(r, nil)
	var pin *string
	if k8s.ForceNamespaceScope {
		target := k8s.GetNamespaceScopeTarget()
		pin = &target
	}
	var missing []string
	if !failed && ceiling == nil && requested != nil {
		missing = cache.KnownMissingNamespaces(requested)
	}
	return readscope.ResolveNamespaces(requested, readscope.NamespaceAccess{Namespaces: ceiling, Checked: !failed}, pin, missing)
}

// outsideNamespacePin refuses a read of one namespace Radar's --namespace-scope
// pin excludes: its cache holds nothing there, so a miss would read as absence.
func outsideNamespacePin(namespace string) *resourceReadFailure {
	if namespace != "" && k8s.ForceNamespaceScope {
		pin := k8s.GetNamespaceScopeTarget()
		selection, _ := readscope.ResolveNamespaces([]string{namespace}, readscope.NamespaceAccess{Checked: true}, &pin, nil)
		if len(selection.Gaps) > 0 {
			return readFailuref(selection.Gaps[0].Code, "namespace %q is outside Radar's --namespace-scope pin", namespace)
		}
	}
	return nil
}
