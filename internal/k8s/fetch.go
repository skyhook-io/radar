package k8s

import (
	"fmt"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	policyv1 "k8s.io/api/policy/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	storagev1 "k8s.io/api/storage/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// ErrUnknownKind is returned by FetchResourceList/FetchResource when the kind
// is not a built-in typed resource. The caller should fall through to the dynamic cache.
var ErrUnknownKind = fmt.Errorf("unknown typed kind")

// builtinGVRs maps lowercase built-in kind forms to their canonical
// GroupVersionResource. Versions are the GA group/versions every supported
// cluster serves.
//
// One table, two jobs:
//   - TypedKindOwnsGroup decides typed-vs-dynamic routing for the resource
//     GET/LIST handlers. Only kinds with typed cache listers participate there.
//   - BuiltinGVR is a static fallback for live/dynamic fetches when API
//     discovery can't resolve a built-in's GVR (partial discovery under
//     restricted RBAC, or a transient refresh miss). The drift/insights live
//     GET (GetDynamicWithGroupPreserveLastApplied) can't use the typed cache —
//     it needs last-applied, which the typed cache strips — so without this it
//     would silently return nil and drop drift for built-in managed resources.
//
// Keep typed=true in sync with the typed switches in this file and internal/server.
var builtinGVRs, typedBuiltinGVRs, builtinKindByResource = func() (map[string]schema.GroupVersionResource, map[string]schema.GroupVersionResource, map[string]string) {
	defs := []struct {
		forms    []string
		group    string
		version  string
		resource string
		kind     string
		typed    bool
	}{
		{[]string{"pod", "pods", "po"}, "", "v1", "pods", "Pod", true},
		{[]string{"service", "services", "svc"}, "", "v1", "services", "Service", true},
		{[]string{"configmap", "configmaps", "cm"}, "", "v1", "configmaps", "ConfigMap", true},
		{[]string{"secret", "secrets"}, "", "v1", "secrets", "Secret", true},
		{[]string{"event", "events"}, "", "v1", "events", "Event", true},
		{[]string{"endpoint", "endpoints", "ep"}, "", "v1", "endpoints", "Endpoints", false},
		{[]string{"persistentvolumeclaim", "persistentvolumeclaims", "pvc", "pvcs"}, "", "v1", "persistentvolumeclaims", "PersistentVolumeClaim", true},
		{[]string{"node", "nodes", "no"}, "", "v1", "nodes", "Node", true},
		{[]string{"namespace", "namespaces", "ns"}, "", "v1", "namespaces", "Namespace", true},
		{[]string{"persistentvolume", "persistentvolumes", "pv", "pvs"}, "", "v1", "persistentvolumes", "PersistentVolume", true},
		{[]string{"serviceaccount", "serviceaccounts", "sa"}, "", "v1", "serviceaccounts", "ServiceAccount", true},
		{[]string{"limitrange", "limitranges"}, "", "v1", "limitranges", "LimitRange", true},
		{[]string{"resourcequota", "resourcequotas"}, "", "v1", "resourcequotas", "ResourceQuota", true},
		{[]string{"deployment", "deployments", "deploy", "deploys"}, "apps", "v1", "deployments", "Deployment", true},
		{[]string{"daemonset", "daemonsets", "ds"}, "apps", "v1", "daemonsets", "DaemonSet", true},
		{[]string{"statefulset", "statefulsets", "sts"}, "apps", "v1", "statefulsets", "StatefulSet", true},
		{[]string{"replicaset", "replicasets", "rs"}, "apps", "v1", "replicasets", "ReplicaSet", true},
		{[]string{"job", "jobs"}, "batch", "v1", "jobs", "Job", true},
		{[]string{"cronjob", "cronjobs", "cj"}, "batch", "v1", "cronjobs", "CronJob", true},
		{[]string{"hpa", "hpas", "horizontalpodautoscaler", "horizontalpodautoscalers"}, "autoscaling", "v2", "horizontalpodautoscalers", "HorizontalPodAutoscaler", true},
		{[]string{"ingress", "ingresses"}, "networking.k8s.io", "v1", "ingresses", "Ingress", true},
		{[]string{"networkpolicy", "networkpolicies", "netpol", "netpols"}, "networking.k8s.io", "v1", "networkpolicies", "NetworkPolicy", true},
		{[]string{"ingressclass", "ingressclasses"}, "networking.k8s.io", "v1", "ingressclasses", "IngressClass", true},
		{[]string{"endpointslice", "endpointslices"}, "discovery.k8s.io", "v1", "endpointslices", "EndpointSlice", false},
		{[]string{"lease", "leases"}, "coordination.k8s.io", "v1", "leases", "Lease", false},
		{[]string{"priorityclass", "priorityclasses", "pc"}, "scheduling.k8s.io", "v1", "priorityclasses", "PriorityClass", false},
		{[]string{"runtimeclass", "runtimeclasses"}, "node.k8s.io", "v1", "runtimeclasses", "RuntimeClass", false},
		{[]string{"mutatingwebhookconfiguration", "mutatingwebhookconfigurations"}, "admissionregistration.k8s.io", "v1", "mutatingwebhookconfigurations", "MutatingWebhookConfiguration", false},
		{[]string{"validatingwebhookconfiguration", "validatingwebhookconfigurations"}, "admissionregistration.k8s.io", "v1", "validatingwebhookconfigurations", "ValidatingWebhookConfiguration", false},
		{[]string{"volumeattachment", "volumeattachments"}, "storage.k8s.io", "v1", "volumeattachments", "VolumeAttachment", false},
		{[]string{"poddisruptionbudget", "poddisruptionbudgets", "pdb", "pdbs"}, "policy", "v1", "poddisruptionbudgets", "PodDisruptionBudget", true},
		{[]string{"storageclass", "storageclasses", "sc"}, "storage.k8s.io", "v1", "storageclasses", "StorageClass", true},
		{[]string{"role", "roles"}, "rbac.authorization.k8s.io", "v1", "roles", "Role", true},
		{[]string{"clusterrole", "clusterroles"}, "rbac.authorization.k8s.io", "v1", "clusterroles", "ClusterRole", true},
		{[]string{"rolebinding", "rolebindings"}, "rbac.authorization.k8s.io", "v1", "rolebindings", "RoleBinding", true},
		{[]string{"clusterrolebinding", "clusterrolebindings"}, "rbac.authorization.k8s.io", "v1", "clusterrolebindings", "ClusterRoleBinding", true},
	}
	m := make(map[string]schema.GroupVersionResource)
	typed := make(map[string]schema.GroupVersionResource)
	kinds := make(map[string]string)
	for _, d := range defs {
		gvr := schema.GroupVersionResource{Group: d.group, Version: d.version, Resource: d.resource}
		kinds[d.resource] = d.kind
		for _, f := range d.forms {
			m[f] = gvr
			if d.typed {
				typed[f] = gvr
			}
		}
	}
	return m, typed, kinds
}()

// lookupTypedBuiltinGVR returns the canonical GVR for a built-in kind (or alias)
// that is served by the typed cache, regardless of group. Callers that must
// be CRD-collision-safe should pair this with a group check (see
// TypedKindOwnsGroup); an empty group in the caller's hands conventionally
// means "the built-in resource" across the server (the REST/MCP handlers
// dispatch the same way).
func lookupTypedBuiltinGVR(kind string) (schema.GroupVersionResource, bool) {
	gvr, ok := typedBuiltinGVRs[strings.ToLower(kind)]
	return gvr, ok
}

// builtinKindForResource returns the canonical CamelCase Kind for a built-in
// plural resource name ("deployments" → "Deployment"). Used to stamp
// apiVersion/kind on unstructured conversions of typed lister objects, which
// carry no TypeMeta.
func builtinKindForResource(resource string) (string, bool) {
	k, ok := builtinKindByResource[resource]
	return k, ok
}

// TypedKindOwnsGroup reports whether (kind, group) names a built-in kind
// addressed by its own API group — i.e. it must resolve via the typed cache,
// not the dynamic/CRD cache. `deployments`+`apps` is a typed lookup;
// `services`+`serving.knative.dev` is a CRD (dynamic) lookup; `services` with
// an empty group is the core typed Service. Handlers use this to gate the "explicit group ⇒
// dynamic cache" dispatch so built-in workloads addressed with their real group
// don't fall through to the dynamic cache (which has no informer for them).
func TypedKindOwnsGroup(kind, group string) bool {
	gvr, ok := typedBuiltinGVRs[strings.ToLower(kind)]
	return ok && gvr.Group == group
}

// CanonicalBuiltinKind returns the canonical plural resource name for a built-in
// kind or kubectl-style alias. Unknown kinds are returned lowercased so CRD
// lookups keep their original dynamic-cache behavior.
func CanonicalBuiltinKind(kind string) string {
	k := strings.ToLower(kind)
	if gvr, ok := builtinGVRs[k]; ok {
		return gvr.Resource
	}
	return k
}

// BuiltinGVR returns the canonical GroupVersionResource for a built-in kind in
// the given group, for use as a static fallback when API discovery can't
// resolve it. group must match the kind's canonical group ("" for core kinds);
// a mismatch (e.g. a CRD whose plural shadows a built-in) returns ok=false so
// the caller keeps treating it as unknown rather than mis-resolving.
func BuiltinGVR(kind, group string) (schema.GroupVersionResource, bool) {
	gvr, ok := builtinGVRs[strings.ToLower(kind)]
	if !ok || gvr.Group != group {
		return schema.GroupVersionResource{}, false
	}
	return gvr, true
}

// BuiltinGVRAnyGroup returns the canonical GroupVersionResource for a built-in
// kind or kubectl-style alias without requiring the caller to know its API
// group. Use this only where an omitted group intentionally means "the built-in
// resource if this name is built-in"; CRD-collision-safe paths should keep using
// BuiltinGVR with an explicit group check.
func BuiltinGVRAnyGroup(kind string) (schema.GroupVersionResource, bool) {
	gvr, ok := builtinGVRs[strings.ToLower(kind)]
	if !ok {
		return schema.GroupVersionResource{}, false
	}
	return gvr, true
}

// ToRuntimeObjects converts a typed slice to []runtime.Object using generics.
func ToRuntimeObjects[T runtime.Object](items []T) []runtime.Object {
	out := make([]runtime.Object, len(items))
	for i, item := range items {
		out[i] = item
	}
	return out
}

// FetchResourceList returns typed resources as []runtime.Object.
// Returns ErrUnknownKind when the kind should fall through to dynamic cache.
// Returns a "forbidden:" prefixed error string when RBAC forbids access.
func FetchResourceList(cache *ResourceCache, kind string, namespaces []string) ([]runtime.Object, error) {
	kind = CanonicalBuiltinKind(kind)

	// listPerNs merges results across namespaces using generic conversion.
	listPerNs := func(listAll func() ([]runtime.Object, error), listNs func(string) ([]runtime.Object, error)) ([]runtime.Object, error) {
		if namespaces == nil {
			return listAll()
		}
		if len(namespaces) == 1 {
			return listNs(namespaces[0])
		}
		var merged []runtime.Object
		for _, ns := range namespaces {
			items, err := listNs(ns)
			if err != nil {
				return nil, err
			}
			merged = append(merged, items...)
		}
		return merged, nil
	}

	switch kind {
	case "pods":
		if cache.Pods() == nil {
			return nil, fmt.Errorf("forbidden: pods")
		}
		return listPerNs(
			func() ([]runtime.Object, error) {
				items, err := cache.Pods().List(labels.Everything())
				if err != nil {
					return nil, err
				}
				return ToRuntimeObjects(items), nil
			},
			func(ns string) ([]runtime.Object, error) {
				items, err := cache.Pods().Pods(ns).List(labels.Everything())
				if err != nil {
					return nil, err
				}
				return ToRuntimeObjects(items), nil
			},
		)
	case "services":
		if cache.Services() == nil {
			return nil, fmt.Errorf("forbidden: services")
		}
		return listPerNs(
			func() ([]runtime.Object, error) {
				items, err := cache.Services().List(labels.Everything())
				if err != nil {
					return nil, err
				}
				return ToRuntimeObjects(items), nil
			},
			func(ns string) ([]runtime.Object, error) {
				items, err := cache.Services().Services(ns).List(labels.Everything())
				if err != nil {
					return nil, err
				}
				return ToRuntimeObjects(items), nil
			},
		)
	case "deployments":
		if cache.Deployments() == nil {
			return nil, fmt.Errorf("forbidden: deployments")
		}
		return listPerNs(
			func() ([]runtime.Object, error) {
				items, err := cache.Deployments().List(labels.Everything())
				if err != nil {
					return nil, err
				}
				return ToRuntimeObjects(items), nil
			},
			func(ns string) ([]runtime.Object, error) {
				items, err := cache.Deployments().Deployments(ns).List(labels.Everything())
				if err != nil {
					return nil, err
				}
				return ToRuntimeObjects(items), nil
			},
		)
	case "daemonsets":
		if cache.DaemonSets() == nil {
			return nil, fmt.Errorf("forbidden: daemonsets")
		}
		return listPerNs(
			func() ([]runtime.Object, error) {
				items, err := cache.DaemonSets().List(labels.Everything())
				if err != nil {
					return nil, err
				}
				return ToRuntimeObjects(items), nil
			},
			func(ns string) ([]runtime.Object, error) {
				items, err := cache.DaemonSets().DaemonSets(ns).List(labels.Everything())
				if err != nil {
					return nil, err
				}
				return ToRuntimeObjects(items), nil
			},
		)
	case "statefulsets":
		if cache.StatefulSets() == nil {
			return nil, fmt.Errorf("forbidden: statefulsets")
		}
		return listPerNs(
			func() ([]runtime.Object, error) {
				items, err := cache.StatefulSets().List(labels.Everything())
				if err != nil {
					return nil, err
				}
				return ToRuntimeObjects(items), nil
			},
			func(ns string) ([]runtime.Object, error) {
				items, err := cache.StatefulSets().StatefulSets(ns).List(labels.Everything())
				if err != nil {
					return nil, err
				}
				return ToRuntimeObjects(items), nil
			},
		)
	case "replicasets":
		if cache.ReplicaSets() == nil {
			return nil, fmt.Errorf("forbidden: replicasets")
		}
		return listPerNs(
			func() ([]runtime.Object, error) {
				items, err := cache.ReplicaSets().List(labels.Everything())
				if err != nil {
					return nil, err
				}
				return ToRuntimeObjects(items), nil
			},
			func(ns string) ([]runtime.Object, error) {
				items, err := cache.ReplicaSets().ReplicaSets(ns).List(labels.Everything())
				if err != nil {
					return nil, err
				}
				return ToRuntimeObjects(items), nil
			},
		)
	case "ingresses":
		if cache.Ingresses() == nil {
			return nil, fmt.Errorf("forbidden: ingresses")
		}
		return listPerNs(
			func() ([]runtime.Object, error) {
				items, err := cache.Ingresses().List(labels.Everything())
				if err != nil {
					return nil, err
				}
				return ToRuntimeObjects(items), nil
			},
			func(ns string) ([]runtime.Object, error) {
				items, err := cache.Ingresses().Ingresses(ns).List(labels.Everything())
				if err != nil {
					return nil, err
				}
				return ToRuntimeObjects(items), nil
			},
		)
	case "configmaps":
		if cache.ConfigMaps() == nil {
			return nil, fmt.Errorf("forbidden: configmaps")
		}
		return listPerNs(
			func() ([]runtime.Object, error) {
				items, err := cache.ConfigMaps().List(labels.Everything())
				if err != nil {
					return nil, err
				}
				return ToRuntimeObjects(items), nil
			},
			func(ns string) ([]runtime.Object, error) {
				items, err := cache.ConfigMaps().ConfigMaps(ns).List(labels.Everything())
				if err != nil {
					return nil, err
				}
				return ToRuntimeObjects(items), nil
			},
		)
	case "secrets":
		if cache.Secrets() == nil {
			return nil, fmt.Errorf("forbidden: secrets")
		}
		return listPerNs(
			func() ([]runtime.Object, error) {
				items, err := cache.Secrets().List(labels.Everything())
				if err != nil {
					return nil, err
				}
				return ToRuntimeObjects(items), nil
			},
			func(ns string) ([]runtime.Object, error) {
				items, err := cache.Secrets().Secrets(ns).List(labels.Everything())
				if err != nil {
					return nil, err
				}
				return ToRuntimeObjects(items), nil
			},
		)
	case "events":
		if cache.Events() == nil {
			return nil, fmt.Errorf("forbidden: events")
		}
		return listPerNs(
			func() ([]runtime.Object, error) {
				items, err := cache.Events().List(labels.Everything())
				if err != nil {
					return nil, err
				}
				return ToRuntimeObjects(items), nil
			},
			func(ns string) ([]runtime.Object, error) {
				items, err := cache.Events().Events(ns).List(labels.Everything())
				if err != nil {
					return nil, err
				}
				return ToRuntimeObjects(items), nil
			},
		)
	case "persistentvolumeclaims":
		if cache.PersistentVolumeClaims() == nil {
			return nil, fmt.Errorf("forbidden: persistentvolumeclaims")
		}
		return listPerNs(
			func() ([]runtime.Object, error) {
				items, err := cache.PersistentVolumeClaims().List(labels.Everything())
				if err != nil {
					return nil, err
				}
				return ToRuntimeObjects(items), nil
			},
			func(ns string) ([]runtime.Object, error) {
				items, err := cache.PersistentVolumeClaims().PersistentVolumeClaims(ns).List(labels.Everything())
				if err != nil {
					return nil, err
				}
				return ToRuntimeObjects(items), nil
			},
		)
	case "jobs":
		if cache.Jobs() == nil {
			return nil, fmt.Errorf("forbidden: jobs")
		}
		return listPerNs(
			func() ([]runtime.Object, error) {
				items, err := cache.Jobs().List(labels.Everything())
				if err != nil {
					return nil, err
				}
				return ToRuntimeObjects(items), nil
			},
			func(ns string) ([]runtime.Object, error) {
				items, err := cache.Jobs().Jobs(ns).List(labels.Everything())
				if err != nil {
					return nil, err
				}
				return ToRuntimeObjects(items), nil
			},
		)
	case "cronjobs":
		if cache.CronJobs() == nil {
			return nil, fmt.Errorf("forbidden: cronjobs")
		}
		return listPerNs(
			func() ([]runtime.Object, error) {
				items, err := cache.CronJobs().List(labels.Everything())
				if err != nil {
					return nil, err
				}
				return ToRuntimeObjects(items), nil
			},
			func(ns string) ([]runtime.Object, error) {
				items, err := cache.CronJobs().CronJobs(ns).List(labels.Everything())
				if err != nil {
					return nil, err
				}
				return ToRuntimeObjects(items), nil
			},
		)
	case "horizontalpodautoscalers":
		if cache.HorizontalPodAutoscalers() == nil {
			return nil, fmt.Errorf("forbidden: horizontalpodautoscalers")
		}
		return listPerNs(
			func() ([]runtime.Object, error) {
				items, err := cache.HorizontalPodAutoscalers().List(labels.Everything())
				if err != nil {
					return nil, err
				}
				return ToRuntimeObjects(items), nil
			},
			func(ns string) ([]runtime.Object, error) {
				items, err := cache.HorizontalPodAutoscalers().HorizontalPodAutoscalers(ns).List(labels.Everything())
				if err != nil {
					return nil, err
				}
				return ToRuntimeObjects(items), nil
			},
		)
	case "nodes":
		if cache.Nodes() == nil {
			return nil, fmt.Errorf("forbidden: nodes")
		}
		items, err := cache.Nodes().List(labels.Everything())
		if err != nil {
			return nil, err
		}
		return ToRuntimeObjects(items), nil
	case "namespaces":
		if cache.Namespaces() == nil {
			return nil, fmt.Errorf("forbidden: namespaces")
		}
		items, err := cache.Namespaces().List(labels.Everything())
		if err != nil {
			return nil, err
		}
		return ToRuntimeObjects(items), nil
	case "persistentvolumes":
		if cache.PersistentVolumes() == nil {
			return nil, fmt.Errorf("forbidden: persistentvolumes")
		}
		items, err := cache.PersistentVolumes().List(labels.Everything())
		if err != nil {
			return nil, err
		}
		return ToRuntimeObjects(items), nil
	case "storageclasses":
		if cache.StorageClasses() == nil {
			return nil, fmt.Errorf("forbidden: storageclasses")
		}
		items, err := cache.StorageClasses().List(labels.Everything())
		if err != nil {
			return nil, err
		}
		return ToRuntimeObjects(items), nil
	case "poddisruptionbudgets":
		if cache.PodDisruptionBudgets() == nil {
			return nil, fmt.Errorf("forbidden: poddisruptionbudgets")
		}
		return listPerNs(
			func() ([]runtime.Object, error) {
				items, err := cache.PodDisruptionBudgets().List(labels.Everything())
				if err != nil {
					return nil, err
				}
				return ToRuntimeObjects(items), nil
			},
			func(ns string) ([]runtime.Object, error) {
				items, err := cache.PodDisruptionBudgets().PodDisruptionBudgets(ns).List(labels.Everything())
				if err != nil {
					return nil, err
				}
				return ToRuntimeObjects(items), nil
			},
		)
	case "networkpolicies":
		if cache.NetworkPolicies() == nil {
			return nil, fmt.Errorf("forbidden: networkpolicies")
		}
		return listPerNs(
			func() ([]runtime.Object, error) {
				items, err := cache.NetworkPolicies().List(labels.Everything())
				if err != nil {
					return nil, err
				}
				return ToRuntimeObjects(items), nil
			},
			func(ns string) ([]runtime.Object, error) {
				items, err := cache.NetworkPolicies().NetworkPolicies(ns).List(labels.Everything())
				if err != nil {
					return nil, err
				}
				return ToRuntimeObjects(items), nil
			},
		)
	case "serviceaccounts":
		if cache.ServiceAccounts() == nil {
			return nil, fmt.Errorf("forbidden: serviceaccounts")
		}
		return listPerNs(
			func() ([]runtime.Object, error) {
				items, err := cache.ServiceAccounts().List(labels.Everything())
				if err != nil {
					return nil, err
				}
				return ToRuntimeObjects(items), nil
			},
			func(ns string) ([]runtime.Object, error) {
				items, err := cache.ServiceAccounts().ServiceAccounts(ns).List(labels.Everything())
				if err != nil {
					return nil, err
				}
				return ToRuntimeObjects(items), nil
			},
		)
	case "limitranges":
		if cache.LimitRanges() == nil {
			return nil, fmt.Errorf("forbidden: limitranges")
		}
		return listPerNs(
			func() ([]runtime.Object, error) {
				items, err := cache.LimitRanges().List(labels.Everything())
				if err != nil {
					return nil, err
				}
				return ToRuntimeObjects(items), nil
			},
			func(ns string) ([]runtime.Object, error) {
				items, err := cache.LimitRanges().LimitRanges(ns).List(labels.Everything())
				if err != nil {
					return nil, err
				}
				return ToRuntimeObjects(items), nil
			},
		)
	case "resourcequotas":
		if cache.ResourceQuotas() == nil {
			return nil, fmt.Errorf("forbidden: resourcequotas")
		}
		return listPerNs(
			func() ([]runtime.Object, error) {
				items, err := cache.ResourceQuotas().List(labels.Everything())
				if err != nil {
					return nil, err
				}
				return ToRuntimeObjects(items), nil
			},
			func(ns string) ([]runtime.Object, error) {
				items, err := cache.ResourceQuotas().ResourceQuotas(ns).List(labels.Everything())
				if err != nil {
					return nil, err
				}
				return ToRuntimeObjects(items), nil
			},
		)
	case "roles":
		if cache.Roles() == nil {
			return nil, fmt.Errorf("forbidden: roles")
		}
		return listPerNs(
			func() ([]runtime.Object, error) {
				items, err := cache.Roles().List(labels.Everything())
				if err != nil {
					return nil, err
				}
				return ToRuntimeObjects(items), nil
			},
			func(ns string) ([]runtime.Object, error) {
				items, err := cache.Roles().Roles(ns).List(labels.Everything())
				if err != nil {
					return nil, err
				}
				return ToRuntimeObjects(items), nil
			},
		)
	case "clusterroles":
		if cache.ClusterRoles() == nil {
			return nil, fmt.Errorf("forbidden: clusterroles")
		}
		items, err := cache.ClusterRoles().List(labels.Everything())
		if err != nil {
			return nil, err
		}
		return ToRuntimeObjects(items), nil
	case "rolebindings":
		if cache.RoleBindings() == nil {
			return nil, fmt.Errorf("forbidden: rolebindings")
		}
		return listPerNs(
			func() ([]runtime.Object, error) {
				items, err := cache.RoleBindings().List(labels.Everything())
				if err != nil {
					return nil, err
				}
				return ToRuntimeObjects(items), nil
			},
			func(ns string) ([]runtime.Object, error) {
				items, err := cache.RoleBindings().RoleBindings(ns).List(labels.Everything())
				if err != nil {
					return nil, err
				}
				return ToRuntimeObjects(items), nil
			},
		)
	case "clusterrolebindings":
		if cache.ClusterRoleBindings() == nil {
			return nil, fmt.Errorf("forbidden: clusterrolebindings")
		}
		items, err := cache.ClusterRoleBindings().List(labels.Everything())
		if err != nil {
			return nil, err
		}
		return ToRuntimeObjects(items), nil
	case "ingressclasses":
		if cache.IngressClasses() == nil {
			return nil, fmt.Errorf("forbidden: ingressclasses")
		}
		items, err := cache.IngressClasses().List(labels.Everything())
		if err != nil {
			return nil, err
		}
		return ToRuntimeObjects(items), nil
	default:
		return nil, ErrUnknownKind
	}
}

// FetchResource returns a single typed resource as runtime.Object.
// Returns ErrUnknownKind when the kind should fall through to dynamic cache.
func FetchResource(cache *ResourceCache, kind, namespace, name string) (runtime.Object, error) {
	kind = CanonicalBuiltinKind(kind)

	switch kind {
	case "pods":
		if cache.Pods() == nil {
			return nil, fmt.Errorf("forbidden: pods")
		}
		return cache.Pods().Pods(namespace).Get(name)
	case "services":
		if cache.Services() == nil {
			return nil, fmt.Errorf("forbidden: services")
		}
		return cache.Services().Services(namespace).Get(name)
	case "deployments":
		if cache.Deployments() == nil {
			return nil, fmt.Errorf("forbidden: deployments")
		}
		return cache.Deployments().Deployments(namespace).Get(name)
	case "daemonsets":
		if cache.DaemonSets() == nil {
			return nil, fmt.Errorf("forbidden: daemonsets")
		}
		return cache.DaemonSets().DaemonSets(namespace).Get(name)
	case "statefulsets":
		if cache.StatefulSets() == nil {
			return nil, fmt.Errorf("forbidden: statefulsets")
		}
		return cache.StatefulSets().StatefulSets(namespace).Get(name)
	case "replicasets":
		if cache.ReplicaSets() == nil {
			return nil, fmt.Errorf("forbidden: replicasets")
		}
		return cache.ReplicaSets().ReplicaSets(namespace).Get(name)
	case "ingresses":
		if cache.Ingresses() == nil {
			return nil, fmt.Errorf("forbidden: ingresses")
		}
		return cache.Ingresses().Ingresses(namespace).Get(name)
	case "configmaps":
		if cache.ConfigMaps() == nil {
			return nil, fmt.Errorf("forbidden: configmaps")
		}
		return cache.ConfigMaps().ConfigMaps(namespace).Get(name)
	case "secrets":
		if cache.Secrets() == nil {
			return nil, fmt.Errorf("forbidden: secrets")
		}
		return cache.Secrets().Secrets(namespace).Get(name)
	case "events":
		if cache.Events() == nil {
			return nil, fmt.Errorf("forbidden: events")
		}
		return cache.Events().Events(namespace).Get(name)
	case "persistentvolumeclaims":
		if cache.PersistentVolumeClaims() == nil {
			return nil, fmt.Errorf("forbidden: persistentvolumeclaims")
		}
		return cache.PersistentVolumeClaims().PersistentVolumeClaims(namespace).Get(name)
	case "horizontalpodautoscalers":
		if cache.HorizontalPodAutoscalers() == nil {
			return nil, fmt.Errorf("forbidden: horizontalpodautoscalers")
		}
		return cache.HorizontalPodAutoscalers().HorizontalPodAutoscalers(namespace).Get(name)
	case "jobs":
		if cache.Jobs() == nil {
			return nil, fmt.Errorf("forbidden: jobs")
		}
		return cache.Jobs().Jobs(namespace).Get(name)
	case "cronjobs":
		if cache.CronJobs() == nil {
			return nil, fmt.Errorf("forbidden: cronjobs")
		}
		return cache.CronJobs().CronJobs(namespace).Get(name)
	case "nodes":
		if cache.Nodes() == nil {
			return nil, fmt.Errorf("forbidden: nodes")
		}
		return cache.Nodes().Get(name)
	case "namespaces":
		if cache.Namespaces() == nil {
			return nil, fmt.Errorf("forbidden: namespaces")
		}
		return cache.Namespaces().Get(name)
	case "persistentvolumes":
		if cache.PersistentVolumes() == nil {
			return nil, fmt.Errorf("forbidden: persistentvolumes")
		}
		return cache.PersistentVolumes().Get(name)
	case "storageclasses":
		if cache.StorageClasses() == nil {
			return nil, fmt.Errorf("forbidden: storageclasses")
		}
		return cache.StorageClasses().Get(name)
	case "poddisruptionbudgets":
		if cache.PodDisruptionBudgets() == nil {
			return nil, fmt.Errorf("forbidden: poddisruptionbudgets")
		}
		return cache.PodDisruptionBudgets().PodDisruptionBudgets(namespace).Get(name)
	case "networkpolicies":
		if cache.NetworkPolicies() == nil {
			return nil, fmt.Errorf("forbidden: networkpolicies")
		}
		return cache.NetworkPolicies().NetworkPolicies(namespace).Get(name)
	case "serviceaccounts":
		if cache.ServiceAccounts() == nil {
			return nil, fmt.Errorf("forbidden: serviceaccounts")
		}
		return cache.ServiceAccounts().ServiceAccounts(namespace).Get(name)
	case "limitranges":
		if cache.LimitRanges() == nil {
			return nil, fmt.Errorf("forbidden: limitranges")
		}
		return cache.LimitRanges().LimitRanges(namespace).Get(name)
	case "resourcequotas":
		if cache.ResourceQuotas() == nil {
			return nil, fmt.Errorf("forbidden: resourcequotas")
		}
		return cache.ResourceQuotas().ResourceQuotas(namespace).Get(name)
	case "roles":
		if cache.Roles() == nil {
			return nil, fmt.Errorf("forbidden: roles")
		}
		return cache.Roles().Roles(namespace).Get(name)
	case "clusterroles":
		if cache.ClusterRoles() == nil {
			return nil, fmt.Errorf("forbidden: clusterroles")
		}
		return cache.ClusterRoles().Get(name)
	case "rolebindings":
		if cache.RoleBindings() == nil {
			return nil, fmt.Errorf("forbidden: rolebindings")
		}
		return cache.RoleBindings().RoleBindings(namespace).Get(name)
	case "clusterrolebindings":
		if cache.ClusterRoleBindings() == nil {
			return nil, fmt.Errorf("forbidden: clusterrolebindings")
		}
		return cache.ClusterRoleBindings().Get(name)
	case "ingressclasses":
		if cache.IngressClasses() == nil {
			return nil, fmt.Errorf("forbidden: ingressclasses")
		}
		return cache.IngressClasses().Get(name)
	default:
		return nil, ErrUnknownKind
	}
}

// SetTypeMeta sets the APIVersion and Kind fields on typed resources.
// Kubernetes informers don't populate these fields, but users expect to see them.
func SetTypeMeta(resource any) {
	switch r := resource.(type) {
	case *corev1.Pod:
		r.APIVersion = "v1"
		r.Kind = "Pod"
	case *corev1.Service:
		r.APIVersion = "v1"
		r.Kind = "Service"
	case *corev1.Node:
		r.APIVersion = "v1"
		r.Kind = "Node"
	case *corev1.Namespace:
		r.APIVersion = "v1"
		r.Kind = "Namespace"
	case *corev1.Event:
		r.APIVersion = "v1"
		r.Kind = "Event"
	case *corev1.ConfigMap:
		r.APIVersion = "v1"
		r.Kind = "ConfigMap"
	case *corev1.Secret:
		r.APIVersion = "v1"
		r.Kind = "Secret"
	case *corev1.PersistentVolumeClaim:
		r.APIVersion = "v1"
		r.Kind = "PersistentVolumeClaim"
	case *appsv1.Deployment:
		r.APIVersion = "apps/v1"
		r.Kind = "Deployment"
	case *appsv1.DaemonSet:
		r.APIVersion = "apps/v1"
		r.Kind = "DaemonSet"
	case *appsv1.StatefulSet:
		r.APIVersion = "apps/v1"
		r.Kind = "StatefulSet"
	case *appsv1.ReplicaSet:
		r.APIVersion = "apps/v1"
		r.Kind = "ReplicaSet"
	case *networkingv1.Ingress:
		r.APIVersion = "networking.k8s.io/v1"
		r.Kind = "Ingress"
	case *batchv1.Job:
		r.APIVersion = "batch/v1"
		r.Kind = "Job"
	case *batchv1.CronJob:
		r.APIVersion = "batch/v1"
		r.Kind = "CronJob"
	case *autoscalingv2.HorizontalPodAutoscaler:
		r.APIVersion = "autoscaling/v2"
		r.Kind = "HorizontalPodAutoscaler"
	case *corev1.PersistentVolume:
		r.APIVersion = "v1"
		r.Kind = "PersistentVolume"
	case *storagev1.StorageClass:
		r.APIVersion = "storage.k8s.io/v1"
		r.Kind = "StorageClass"
	case *policyv1.PodDisruptionBudget:
		r.APIVersion = "policy/v1"
		r.Kind = "PodDisruptionBudget"
	case *networkingv1.NetworkPolicy:
		r.APIVersion = "networking.k8s.io/v1"
		r.Kind = "NetworkPolicy"
	case *networkingv1.IngressClass:
		r.APIVersion = "networking.k8s.io/v1"
		r.Kind = "IngressClass"
	case *corev1.ServiceAccount:
		r.APIVersion = "v1"
		r.Kind = "ServiceAccount"
	case *corev1.LimitRange:
		r.APIVersion = "v1"
		r.Kind = "LimitRange"
	case *corev1.ResourceQuota:
		r.APIVersion = "v1"
		r.Kind = "ResourceQuota"
	case *rbacv1.Role:
		r.APIVersion = "rbac.authorization.k8s.io/v1"
		r.Kind = "Role"
	case *rbacv1.ClusterRole:
		r.APIVersion = "rbac.authorization.k8s.io/v1"
		r.Kind = "ClusterRole"
	case *rbacv1.RoleBinding:
		r.APIVersion = "rbac.authorization.k8s.io/v1"
		r.Kind = "RoleBinding"
	case *rbacv1.ClusterRoleBinding:
		r.APIVersion = "rbac.authorization.k8s.io/v1"
		r.Kind = "ClusterRoleBinding"
	}
}
