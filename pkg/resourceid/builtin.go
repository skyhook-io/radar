package resourceid

import "strings"

// Builtin describes a resource Kubernetes ships, as opposed to one a CRD or an
// aggregated API adds. These are facts about the API, not about Radar: which
// of them Radar serves from typed caches is decided by its consumers.
type Builtin struct {
	Group string
	// Version is the version Radar reads the resource at. It is empty where the
	// served version still varies across supported clusters, so callers must
	// take it from discovery.
	Version    string
	Kind       string
	Resource   string
	Namespaced bool
	// Aliases are kubectl short names and other lowercase spellings accepted in
	// URLs and tool arguments, beyond the lowercase Kind and the plural.
	Aliases []string
}

// APIVersion returns the resource's apiVersion, or "" when Version varies.
func (b Builtin) APIVersion() string {
	if b.Version == "" {
		return ""
	}
	return APIVersion(b.Group, b.Version)
}

func (b Builtin) GroupKind() GroupKind {
	return GroupKind{Group: b.Group, Kind: b.Kind}
}

// Builtins is the one table of built-in resource identities. Keep it to
// resources Radar addresses somewhere; exhaustiveness is not the goal.
var Builtins = []Builtin{
	{Group: "", Version: "v1", Kind: "Pod", Resource: "pods", Namespaced: true, Aliases: []string{"po"}},
	{Group: "", Version: "v1", Kind: "Service", Resource: "services", Namespaced: true, Aliases: []string{"svc"}},
	{Group: "", Version: "v1", Kind: "ConfigMap", Resource: "configmaps", Namespaced: true, Aliases: []string{"cm"}},
	{Group: "", Version: "v1", Kind: "Secret", Resource: "secrets", Namespaced: true},
	{Group: "", Version: "v1", Kind: "Event", Resource: "events", Namespaced: true},
	{Group: "", Version: "v1", Kind: "Endpoints", Resource: "endpoints", Namespaced: true, Aliases: []string{"endpoint", "ep"}},
	{Group: "", Version: "v1", Kind: "PersistentVolumeClaim", Resource: "persistentvolumeclaims", Namespaced: true, Aliases: []string{"pvc", "pvcs"}},
	{Group: "", Version: "v1", Kind: "Node", Resource: "nodes", Aliases: []string{"no"}},
	{Group: "", Version: "v1", Kind: "Namespace", Resource: "namespaces", Aliases: []string{"ns"}},
	{Group: "", Version: "v1", Kind: "PersistentVolume", Resource: "persistentvolumes", Aliases: []string{"pv", "pvs"}},
	{Group: "", Version: "v1", Kind: "ServiceAccount", Resource: "serviceaccounts", Namespaced: true, Aliases: []string{"sa"}},
	{Group: "", Version: "v1", Kind: "LimitRange", Resource: "limitranges", Namespaced: true},
	{Group: "", Version: "v1", Kind: "ResourceQuota", Resource: "resourcequotas", Namespaced: true},
	{Group: "apps", Version: "v1", Kind: "Deployment", Resource: "deployments", Namespaced: true, Aliases: []string{"deploy", "deploys"}},
	{Group: "apps", Version: "v1", Kind: "DaemonSet", Resource: "daemonsets", Namespaced: true, Aliases: []string{"ds"}},
	{Group: "apps", Version: "v1", Kind: "StatefulSet", Resource: "statefulsets", Namespaced: true, Aliases: []string{"sts"}},
	{Group: "apps", Version: "v1", Kind: "ReplicaSet", Resource: "replicasets", Namespaced: true, Aliases: []string{"rs"}},
	{Group: "apps", Version: "v1", Kind: "ControllerRevision", Resource: "controllerrevisions", Namespaced: true},
	{Group: "batch", Version: "v1", Kind: "Job", Resource: "jobs", Namespaced: true},
	{Group: "batch", Version: "v1", Kind: "CronJob", Resource: "cronjobs", Namespaced: true, Aliases: []string{"cj"}},
	{Group: "autoscaling", Version: "v2", Kind: "HorizontalPodAutoscaler", Resource: "horizontalpodautoscalers", Namespaced: true, Aliases: []string{"hpa", "hpas"}},
	{Group: "networking.k8s.io", Version: "v1", Kind: "Ingress", Resource: "ingresses", Namespaced: true},
	{Group: "networking.k8s.io", Version: "v1", Kind: "NetworkPolicy", Resource: "networkpolicies", Namespaced: true, Aliases: []string{"netpol", "netpols"}},
	{Group: "networking.k8s.io", Version: "v1", Kind: "IngressClass", Resource: "ingressclasses"},
	{Group: "discovery.k8s.io", Version: "v1", Kind: "EndpointSlice", Resource: "endpointslices", Namespaced: true},
	{Group: "coordination.k8s.io", Version: "v1", Kind: "Lease", Resource: "leases", Namespaced: true},
	{Group: "scheduling.k8s.io", Version: "v1", Kind: "PriorityClass", Resource: "priorityclasses", Aliases: []string{"pc"}},
	{Group: "node.k8s.io", Version: "v1", Kind: "RuntimeClass", Resource: "runtimeclasses"},
	{Group: "admissionregistration.k8s.io", Version: "v1", Kind: "MutatingWebhookConfiguration", Resource: "mutatingwebhookconfigurations"},
	{Group: "admissionregistration.k8s.io", Version: "v1", Kind: "ValidatingWebhookConfiguration", Resource: "validatingwebhookconfigurations"},
	{Group: "storage.k8s.io", Version: "v1", Kind: "StorageClass", Resource: "storageclasses", Aliases: []string{"sc"}},
	{Group: "storage.k8s.io", Version: "v1", Kind: "VolumeAttachment", Resource: "volumeattachments"},
	{Group: "policy", Version: "v1", Kind: "PodDisruptionBudget", Resource: "poddisruptionbudgets", Namespaced: true, Aliases: []string{"pdb", "pdbs"}},
	{Group: "rbac.authorization.k8s.io", Version: "v1", Kind: "Role", Resource: "roles", Namespaced: true},
	{Group: "rbac.authorization.k8s.io", Version: "v1", Kind: "ClusterRole", Resource: "clusterroles"},
	{Group: "rbac.authorization.k8s.io", Version: "v1", Kind: "RoleBinding", Resource: "rolebindings", Namespaced: true},
	{Group: "rbac.authorization.k8s.io", Version: "v1", Kind: "ClusterRoleBinding", Resource: "clusterrolebindings"},
	// Dynamic resource allocation: served at v1beta1, v1beta2 or v1 depending on
	// the cluster, so the version comes from discovery.
	{Group: "resource.k8s.io", Kind: "ResourceClaim", Resource: "resourceclaims", Namespaced: true},
	{Group: "resource.k8s.io", Kind: "ResourceClaimTemplate", Resource: "resourceclaimtemplates", Namespaced: true},
	{Group: "resource.k8s.io", Kind: "ResourceSlice", Resource: "resourceslices"},
	{Group: "resource.k8s.io", Kind: "DeviceClass", Resource: "deviceclasses"},
}

var builtinByKind, builtinByName = func() (map[string]Builtin, map[string]Builtin) {
	byKind := make(map[string]Builtin, len(Builtins))
	byName := make(map[string]Builtin, len(Builtins)*3)
	for _, b := range Builtins {
		byKind[b.Kind] = b
		for _, name := range b.Names() {
			byName[name] = b
		}
	}
	return byKind, byName
}()

// Names returns every lowercase spelling that addresses b: its Kind, its
// plural resource, and its aliases.
func (b Builtin) Names() []string {
	names := []string{strings.ToLower(b.Kind), b.Resource}
	return append(names, b.Aliases...)
}

// BuiltinForKind returns the built-in resource with exactly this Kind.
func BuiltinForKind(kind string) (Builtin, bool) {
	b, ok := builtinByKind[kind]
	return b, ok
}

// BuiltinForName resolves a Kind, plural or alias, in any case, to a built-in
// resource ("deploy", "Deployments", "deployment" → apps Deployment). It says
// nothing about whether a CRD shadows that name; see ResolveCurrent.
func BuiltinForName(name string) (Builtin, bool) {
	b, ok := builtinByName[strings.ToLower(name)]
	return b, ok
}

// BuiltinGroup returns the canonical API group for a built-in Kubernetes Kind.
// The boolean distinguishes core-group kinds from unrecognized custom kinds.
func BuiltinGroup(kind string) (string, bool) {
	b, ok := builtinByKind[kind]
	return b.Group, ok
}

// GroupForBuiltinKind maps a built-in Kubernetes Kind to its API group. Returns
// "" for both core-group built-ins and unrecognized kinds; callers that must
// distinguish those cases should use BuiltinGroup.
func GroupForBuiltinKind(kind string) string {
	group, _ := BuiltinGroup(kind)
	return group
}

// BuiltinAPIVersion returns the apiVersion Radar reads a built-in Kind at, for
// restoring the TypeMeta that typed informer objects arrive without. It is
// false for unknown kinds and for built-ins whose served version varies.
func BuiltinAPIVersion(kind string) (string, bool) {
	b, ok := builtinByKind[kind]
	if !ok || b.Version == "" {
		return "", false
	}
	return b.APIVersion(), true
}
