package gitops

import (
	"strings"

	"github.com/skyhook-io/radar/pkg/resourceid"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// StringValue returns v as a string, or "" if v is not a string.
// Convenience helper for unstructured map[string]any access where typed
// assertions would otherwise litter the call sites.
func StringValue(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

// IsInClusterDestination reports whether an Argo Application deploys to the
// cluster Radar is connected to — spec.destination is the local API server or
// the "in-cluster" name — as opposed to a remote hub-spoke destination. Radar's
// per-user SARs authorize against the local cluster only, so the desired/live
// manifests of a remote destination cannot be authorized here. An Application
// with no destination is invalid (Argo reports InvalidSpecError and deploys
// nothing), so it is not local either: its status.resources may be left over
// from a destination it no longer names. Fail closed: a nil Application is
// treated as not-in-cluster.
func IsInClusterDestination(app *unstructured.Unstructured) bool {
	if app == nil {
		return false
	}
	return IsInClusterArgoDestination(argoDestination(app))
}

// IsInClusterArgoDestination reports whether an Argo CD destination, given by
// name or server, is Argo CD's in-cluster destination: the name in-cluster or
// the in-cluster API server address. Any other destination may still be this
// cluster, registered under another name or its external address.
func IsInClusterArgoDestination(name, server string) bool {
	return strings.EqualFold(name, "in-cluster") || isLocalAPIServer(server)
}

// ArgoComparedDestination returns the destination an Application's
// status.resources describes: status.sync.comparedTo.destination, which Argo
// CD records with each comparison, or spec.destination before one does. Until
// Argo CD compares against a changed destination, the list still names the
// old destination's objects.
func ArgoComparedDestination(app *unstructured.Unstructured) (name, server, namespace string) {
	if app == nil {
		return "", "", ""
	}
	name, server, namespace = argoDestinationAt(app, "status", "sync", "comparedTo", "destination")
	if name == "" && server == "" {
		name, server, namespace = argoDestinationAt(app, "spec", "destination")
	}
	return name, server, namespace
}

func argoDestinationAt(app *unstructured.Unstructured, path ...string) (name, server, namespace string) {
	raw, _, _ := unstructured.NestedFieldNoCopy(app.Object, path...)
	destination, _ := raw.(map[string]any)
	name, _ = destination["name"].(string)
	server, _ = destination["server"].(string)
	namespace, _ = destination["namespace"].(string)
	return strings.TrimSpace(name), strings.TrimSpace(server), namespace
}

// HasArgoDestination reports whether an Argo Application names a destination
// cluster at all, by server or by name.
func HasArgoDestination(app *unstructured.Unstructured) bool {
	if app == nil {
		return false
	}
	name, server := argoDestination(app)
	return name != "" || server != ""
}

func argoDestination(app *unstructured.Unstructured) (name, server string) {
	name, _, _ = unstructured.NestedString(app.Object, "spec", "destination", "name")
	server, _, _ = unstructured.NestedString(app.Object, "spec", "destination", "server")
	return strings.TrimSpace(name), strings.TrimSpace(server)
}

// ArgoSyncPrunesEverything reports whether the last reported status.resources
// list is nonempty and every entry has requiresPruning set. It does not account
// for resource Prune=false/confirm options or a stale list, and does not prove
// that a pruning sync will delete every resource. Sync status is independent:
// IgnoreExtraneous resources can leave such an Application Synced.
func ArgoSyncPrunesEverything(app *unstructured.Unstructured) bool {
	if app == nil {
		return false
	}
	resources, _, _ := unstructured.NestedSlice(app.Object, "status", "resources")
	if len(resources) == 0 {
		return false
	}
	for _, r := range resources {
		m, ok := r.(map[string]any)
		if !ok {
			return false
		}
		if prune, _ := m["requiresPruning"].(bool); !prune {
			return false
		}
	}
	return true
}

// ArgoEmptyRenderGuardHolds applies the test Argo's auto-sync makes before it
// refuses with "auto-sync will wipe out all resources": auto-sync is on with
// prune and without allowEmpty, the app is OutOfSync, and a sync would prune
// everything. That condition is not re-evaluated while a sync window blocks
// auto-sync, so it can outlive the empty render or a policy change; callers
// confirm it against this instead of trusting its presence.
func ArgoEmptyRenderGuardHolds(app *unstructured.Unstructured) bool {
	if app == nil {
		return false
	}
	automated, found, _ := unstructured.NestedMap(app.Object, "spec", "syncPolicy", "automated")
	if !found {
		return false
	}
	if enabled, ok, _ := unstructured.NestedBool(automated, "enabled"); ok && !enabled {
		return false
	}
	if prune, _, _ := unstructured.NestedBool(automated, "prune"); !prune {
		return false
	}
	if allowEmpty, _, _ := unstructured.NestedBool(automated, "allowEmpty"); allowEmpty {
		return false
	}
	sync, _, _ := unstructured.NestedString(app.Object, "status", "sync", "status")
	return strings.EqualFold(sync, "OutOfSync") && ArgoSyncPrunesEverything(app)
}

// ArgoConditionMessages returns the messages of an Argo Application's
// status.conditions entries of one type, in controller order.
func ArgoConditionMessages(app *unstructured.Unstructured, condType string) []string {
	if app == nil {
		return nil
	}
	conditions, _, _ := unstructured.NestedSlice(app.Object, "status", "conditions")
	var out []string
	for _, c := range conditions {
		m, ok := c.(map[string]any)
		if !ok || StringValue(m["type"]) != condType {
			continue
		}
		out = append(out, StringValue(m["message"]))
	}
	return out
}

// ArgoCLIName is how the argocd CLI addresses an Application: namespace/name,
// which resolves both control-plane and any-namespace Applications.
func ArgoCLIName(app *unstructured.Unstructured) string {
	if app == nil || app.GetName() == "" {
		return ""
	}
	if app.GetNamespace() == "" {
		return app.GetName()
	}
	return app.GetNamespace() + "/" + app.GetName()
}

// ArgoAppliesElsewhere reports whether an Application's list, whether Radar
// can read it or not, describes a destination Argo CD names and that isn't
// this cluster. An Application naming no destination isn't known to apply
// anywhere.
func ArgoAppliesElsewhere(app *unstructured.Unstructured) bool {
	name, server, _ := ArgoComparedDestination(app)
	return (name != "" || server != "") && !IsInClusterArgoDestination(name, server)
}

// FluxTargetsLocalCluster reports whether a Flux Kustomization or HelmRelease
// applies to the cluster it runs in. spec.kubeConfig points it at a cluster
// Radar can't confirm is this one, whose objects its inventory then names.
// Fail closed: a nil object is not local.
func FluxTargetsLocalCluster(obj *unstructured.Unstructured) bool {
	if obj == nil {
		return false
	}
	_, remote, _ := unstructured.NestedMap(obj.Object, "spec", "kubeConfig")
	return !remote
}

// isLocalAPIServer matches the in-cluster Kubernetes API server URL Argo records
// for a same-cluster destination (kubernetes.default.svc, with or without a
// scheme, port, or trailing dot). Any other host isn't confirmed to be this
// cluster.
func isLocalAPIServer(server string) bool {
	if server == "" {
		return false
	}
	h := server
	if i := strings.Index(h, "://"); i >= 0 {
		h = h[i+3:]
	}
	if i := strings.IndexAny(h, "/?#"); i >= 0 {
		h = h[:i]
	}
	if i := strings.LastIndex(h, ":"); i >= 0 {
		h = h[:i]
	}
	h = strings.TrimSuffix(strings.ToLower(h), ".")
	// Accept every in-cluster form: the short names and the service FQDN with any
	// cluster domain (kubernetes.default.svc.cluster.local, or a custom domain).
	return h == "kubernetes" || h == "kubernetes.default" ||
		h == "kubernetes.default.svc" || strings.HasPrefix(h, "kubernetes.default.svc.")
}

// ParseFluxInventoryID parses Flux's namespace_name_group_kind inventory key
// from the right so resource names containing underscores remain intact.
// Flux writes a colon in an RBAC object's name as a double underscore
// (system:metrics-server is system__metrics-server) and reads every "__" back
// as a colon; no other kind's name can hold either.
func ParseFluxInventoryID(id string) (group, kind, namespace, name string, ok bool) {
	group, kind, namespace, name, ok = fluxInventoryParts(id)
	if !ok {
		return "", "", "", "", false
	}
	return group, kind, namespace, strings.ReplaceAll(name, "__", ":"), true
}

// fluxInventoryParts slices an inventory ID into its parts without copying,
// the name still encoded. namespace_name_group_kind: a name is the only part
// that can hold an underscore. Slicing rather than splitting reads a whole
// inventory without an allocation per entry; only decoding a colon allocates.
func fluxInventoryParts(id string) (group, kind, namespace, name string, ok bool) {
	last := strings.LastIndexByte(id, '_')
	if last < 0 {
		return "", "", "", "", false
	}
	beforeGroup := strings.LastIndexByte(id[:last], '_')
	first := strings.IndexByte(id, '_')
	if beforeGroup <= first {
		return "", "", "", "", false
	}
	kind = id[last+1:]
	group = resourceid.NormalizeGroup(id[beforeGroup+1 : last])
	namespace = id[:first]
	name = id[first+1 : beforeGroup]
	if kind == "" || name == "" {
		return "", "", "", "", false
	}
	return group, kind, namespace, name, true
}

// GitOps controller families, as reported on the wire.
const (
	ToolArgoCD = "argocd"
	ToolFluxCD = "fluxcd"
)

// ToolFor returns the controller family that owns objects of this API group
// and kind, or "" when Radar has no GitOps view for that identity. The kind alone is
// not identity: SIG Apps (app.k8s.io) defines an Application too. Argo CD
// shares argoproj.io with Argo Rollouts, Workflows and Events, so it is
// matched by kind as well. Flux uses the supported operation registry.
func ToolFor(group, kind string) string {
	if group == "argoproj.io" {
		switch kind {
		case "Application", "ApplicationSet", "AppProject":
			return ToolArgoCD
		}
		return ""
	}
	if entry, err := ResolveFluxKind(kind); err == nil && entry.GVR.Group == group {
		return ToolFluxCD
	}
	return ""
}

// ToolOf is ToolFor for a live object, read from its apiVersion and kind.
func ToolOf(obj *unstructured.Unstructured) string {
	if obj == nil {
		return ""
	}
	return ToolFor(resourceid.GroupFromAPIVersion(obj.GetAPIVersion()), obj.GetKind())
}
