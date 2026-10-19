package topology

import (
	"errors"
	"fmt"
	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sort"
	"strings"

	"github.com/skyhook-io/radar/pkg/resourceid"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

type InventoryState string

const (
	InventoryPending     InventoryState = "pending"
	InventoryFailed      InventoryState = "failed"
	InventoryUncovered   InventoryState = "uncovered"
	InventoryUnavailable InventoryState = "unavailable"
)

type InventoryError struct {
	Group     string
	Resource  string
	State     InventoryState
	Namespace string
}

func (e *InventoryError) Error() string {
	switch e.State {
	case InventoryPending:
		return fmt.Sprintf("%s inventory is still syncing", e.Resource)
	case InventoryFailed:
		return fmt.Sprintf("%s inventory sync failed", e.Resource)
	case InventoryUncovered:
		if e.Namespace != "" {
			return fmt.Sprintf("%s inventory does not cover namespace %q", e.Resource, e.Namespace)
		}
		return fmt.Sprintf("%s inventory only covers selected namespaces; specify a watched namespace for a complete list", e.Resource)
	default:
		return fmt.Sprintf("%s inventory is not available", e.Resource)
	}
}

type InventoryWarning struct {
	Resource  string         `json:"resource"`
	Group     string         `json:"group,omitempty"`
	State     InventoryState `json:"state"`
	Namespace string         `json:"namespace,omitempty"`
	Message   string         `json:"message"`
}

func InventoryWarningFor(resource string, err error) InventoryWarning {
	w := InventoryWarning{Resource: resource, State: InventoryUnavailable}
	kind := resource
	if b, ok := resourceid.BuiltinForName(resource); ok {
		kind = b.Kind
		w.Resource = b.Resource
		w.Group = b.Group
	}
	var inventory *InventoryError
	if errors.As(err, &inventory) {
		w.State = inventory.State
		w.Namespace = inventory.Namespace
		if inventory.Resource != "" {
			w.Resource = inventory.Resource
		}
		if inventory.Group != "" {
			w.Group = inventory.Group
		}
	}
	switch w.State {
	case InventoryPending:
		w.Message = kind + " data is still loading"
	case InventoryFailed:
		w.Message = kind + " data did not finish loading before the sync deadline"
	case InventoryUncovered:
		if w.Namespace != "" {
			w.Message = fmt.Sprintf("%s data may be incomplete: Radar does not watch namespace %q", kind, w.Namespace)
		} else {
			w.Message = kind + " data may be incomplete: Radar only watches selected namespaces"
		}
	default:
		w.Message = kind + " data is not available in Radar's cache"
	}
	return w
}

type topologyInventoryTracker struct {
	provider   ResourceProvider
	namespaces []string
	warnings   map[string]InventoryWarning
}

func (t *topologyInventoryTracker) record(resource string, err error) {
	if err == nil {
		return
	}
	w := InventoryWarningFor(resource, err)
	if t.warnings == nil {
		t.warnings = map[string]InventoryWarning{}
	}
	key := w.Group + "/" + w.Resource + "/" + string(w.State) + "/" + w.Namespace
	t.warnings[key] = w
}
func readTopologyInventory[T any](t *topologyInventoryTracker, resource string, list func() (T, error)) (T, error) {
	if t == nil {
		return list()
	}
	if provider, ok := t.provider.(ResourceInventoryProvider); ok {
		if len(t.namespaces) == 0 {
			t.record(resource, provider.InventoryError(resource, ""))
		} else {
			for _, ns := range t.namespaces {
				t.record(resource, provider.InventoryError(resource, ns))
			}
		}
	}
	items, err := list()
	t.record(resource, err)
	var inventory *InventoryError
	if errors.As(err, &inventory) {
		return items, nil
	}
	return items, err
}
func (t *topologyInventoryTracker) snapshot() []InventoryWarning {
	keys := make([]string, 0, len(t.warnings))
	for k := range t.warnings {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var warnings []InventoryWarning
	for _, k := range keys {
		warnings = append(warnings, t.warnings[k])
	}
	return warnings
}

type inventoryDynamicProvider struct {
	DynamicProvider
	tracker *topologyInventoryTracker
}

// Bare-kind discovery can resolve a built-in with the same kind as a CRD.
func (p inventoryDynamicProvider) IsCRDGVR(gvr schema.GroupVersionResource) bool {
	return isCRDGVR(p.DynamicProvider, gvr, p.GetKindForGVR(gvr))
}

func (p inventoryDynamicProvider) record(gvr schema.GroupVersionResource, err error) {
	if err == nil {
		return
	}
	kind := p.GetKindForGVR(gvr)
	if kind == "" {
		kind = gvr.Resource
	}
	if gvr.Group != "" {
		kind += "." + gvr.Group
	}
	w := InventoryWarning{Resource: gvr.Resource, Group: gvr.Group, State: InventoryUnavailable, Message: kind + " data could not be read"}
	if p.tracker.warnings == nil {
		p.tracker.warnings = map[string]InventoryWarning{}
	}
	p.tracker.warnings[gvr.Group+"/"+gvr.Resource+"/unavailable"] = w
}
func (p inventoryDynamicProvider) List(gvr schema.GroupVersionResource, namespace string) ([]*unstructured.Unstructured, error) {
	items, err := p.DynamicProvider.List(gvr, namespace)
	p.record(gvr, err)
	return items, err
}
func (p inventoryDynamicProvider) ListNamespaces(gvr schema.GroupVersionResource, namespaces []string) ([]*unstructured.Unstructured, error) {
	items, err := p.DynamicProvider.ListNamespaces(gvr, namespaces)
	p.record(gvr, err)
	return items, err
}
func InventoryWarningsChanged(topo *Topology, provider ResourceProvider) bool {
	if topo == nil {
		return false
	}
	inventory, ok := provider.(ResourceInventoryProvider)
	if !ok {
		return false
	}
	for _, warning := range topo.IncompleteInventories {
		builtin, known := resourceid.BuiltinForName(warning.Resource)
		if !known || builtin.Group != warning.Group {
			continue
		}
		err := inventory.InventoryError(warning.Resource, warning.Namespace)
		if err == nil {
			return true
		}
		current := InventoryWarningFor(warning.Resource, err)
		if current.State != warning.State {
			return true
		}
	}
	return false
}

func relationshipInventorySources(kind, group string, obj any) map[string]bool {
	sources := map[string]bool{}
	add := func(names ...string) {
		for _, name := range names {
			sources[name] = true
		}
	}
	addRefs := func(refs workloadRefs) {
		if len(refs.configMaps) > 0 {
			add("configmaps")
		}
		if len(refs.secrets) > 0 {
			add("secrets")
		}
		if len(refs.pvcs) > 0 {
			add("persistentvolumeclaims")
		}
		if refs.serviceAccount != "" {
			add("serviceaccounts")
		}
	}
	builtin, known := resourceid.BuiltinForName(kind)
	if !known || builtin.Group != group {
		if group == "cert-manager.io" && strings.EqualFold(kind, "Certificate") {
			// Issuance uses a same-namespace Secret, and an Ingress can own
			// the Certificate. Unrelated workload inventories do not feed it.
			add("secrets", "ingresses")
			return sources
		}
		// Arbitrary custom controllers can own or reference built-ins.
		// Use the shared identity catalog instead of a second incomplete list.
		for _, resource := range resourceid.Builtins {
			add(resource.Resource)
		}
		if object, ok := obj.(*unstructured.Unstructured); ok && object.GetKind() == "Rollout" && resourceid.GroupFromAPIVersion(object.GetAPIVersion()) == "argoproj.io" {
			spec, _, _ := unstructured.NestedMap(object.Object, "spec", "template", "spec")
			refs, _ := extractWorkloadReferencesFromMap(spec, true)
			addRefs(refs)
		}
		return sources
	}
	add(builtin.Resource)
	workloads := []string{"pods", "deployments", "replicasets", "daemonsets", "statefulsets", "jobs", "cronjobs"}
	switch builtin.Kind {
	case "ConfigMap", "Secret", "ServiceAccount":
		add(workloads...)
	case "Node":
		add("pods")
	case "PersistentVolume", "StorageClass":
		add("persistentvolumes", "persistentvolumeclaims")
	case "PersistentVolumeClaim":
		add("persistentvolumes")
		add(workloads...)
	case "Pod", "Deployment", "ReplicaSet", "DaemonSet", "StatefulSet", "Job", "CronJob":
		add("services", "ingresses", "poddisruptionbudgets", "networkpolicies")
		switch builtin.Kind {
		case "Deployment":
			add("replicasets", "pods", "horizontalpodautoscalers")
		case "StatefulSet":
			add("pods", "horizontalpodautoscalers")
		case "ReplicaSet", "DaemonSet", "Job":
			add("pods")
		case "CronJob":
			add("jobs", "pods")
		}
	case "Service":
		add("pods", "deployments", "statefulsets", "daemonsets", "endpoints", "endpointslices", "ingresses")
	case "Ingress":
		add("services")
	case "NetworkPolicy", "PodDisruptionBudget":
		add(workloads...)
	}
	if metadata, ok := obj.(metav1.Object); ok {
		for _, owner := range metadata.GetOwnerReferences() {
			if b, known := resourceid.BuiltinForName(owner.Kind); known && b.Group == resourceid.GroupFromAPIVersion(owner.APIVersion) {
				add(b.Resource)
				if builtin.Kind == "Pod" && b.Kind == "ReplicaSet" && b.Group == "apps" {
					add("deployments")
				}
			}
		}
	}
	var podSpec *corev1.PodSpec
	switch object := obj.(type) {
	case *appsv1.Deployment:
		podSpec = &object.Spec.Template.Spec
	case *appsv1.StatefulSet:
		podSpec = &object.Spec.Template.Spec
	case *appsv1.DaemonSet:
		podSpec = &object.Spec.Template.Spec
	case *appsv1.ReplicaSet:
		podSpec = &object.Spec.Template.Spec
	case *batchv1.Job:
		podSpec = &object.Spec.Template.Spec
	case *batchv1.CronJob:
		podSpec = &object.Spec.JobTemplate.Spec.Template.Spec
	case *networkingv1.Ingress:
		for _, tls := range object.Spec.TLS {
			if tls.SecretName != "" {
				add("secrets")
				break
			}
		}
	case *autoscalingv2.HorizontalPodAutoscaler:
		target := object.Spec.ScaleTargetRef
		if b, known := resourceid.BuiltinForName(target.Kind); known && b.Group == resourceid.GroupFromAPIVersion(target.APIVersion) {
			add(b.Resource)
		}
	case *unstructured.Unstructured:
		path := []string{"spec", "template", "spec"}
		if builtin.Kind == "CronJob" {
			path = []string{"spec", "jobTemplate", "spec", "template", "spec"}
		}
		if spec, found, _ := unstructured.NestedMap(object.Object, path...); found {
			refs, _ := extractWorkloadReferencesFromMap(spec, true)
			addRefs(refs)
			if node, _ := spec["nodeName"].(string); node != "" {
				add("nodes")
			}
		}
		if builtin.Kind == "Ingress" {
			tls, _, _ := unstructured.NestedSlice(object.Object, "spec", "tls")
			for _, entry := range tls {
				if row, ok := entry.(map[string]any); ok {
					if name, _ := row["secretName"].(string); name != "" {
						add("secrets")
					}
				}
			}
		}
		if builtin.Kind == "HorizontalPodAutoscaler" {
			target, _, _ := unstructured.NestedString(object.Object, "spec", "scaleTargetRef", "kind")
			version, _, _ := unstructured.NestedString(object.Object, "spec", "scaleTargetRef", "apiVersion")
			if b, known := resourceid.BuiltinForName(target); known && b.Group == resourceid.GroupFromAPIVersion(version) {
				add(b.Resource)
			}
		}
	}
	if podSpec != nil {
		addRefs(extractWorkloadReferences(*podSpec, true))
		if podSpec.NodeName != "" {
			add("nodes")
		}
	}
	return sources
}

func relevantInventoryWarnings(topo *Topology, kind, group, namespace string, obj any, provider ResourceProvider) []InventoryWarning {
	if topo == nil || len(topo.IncompleteInventories) == 0 {
		return nil
	}
	sources := relationshipInventorySources(kind, group, obj)
	subject, builtinRoot := resourceid.BuiltinForName(kind)
	certificateRoot := group == "cert-manager.io" && strings.EqualFold(kind, "Certificate")
	namespaceLocalRoot := builtinRoot && subject.Group == group || certificateRoot
	var warnings []InventoryWarning
	for _, warning := range topo.IncompleteInventories {
		builtin, known := resourceid.BuiltinForName(warning.Resource)
		if known && builtin.Group == warning.Group {
			if !sources[warning.Resource] {
				continue
			}
		} else if subject, builtinRoot := resourceid.BuiltinForName(kind); builtinRoot && subject.Group == group {
			continue
		} else if certificateRoot && warning.Group != "cert-manager.io" && warning.Group != "acme.cert-manager.io" && warning.Group != "traefik.io" {
			continue
		}
		if warning.Namespace != "" && namespace != "" && warning.Namespace != namespace {
			continue
		}
		// Custom controllers can manage resources in other namespaces. Covering
		// only the root namespace does not establish their complete inventory.
		if warning.State == InventoryUncovered && namespace != "" && namespaceLocalRoot {
			if inventory, ok := provider.(ResourceInventoryProvider); ok && inventory.InventoryError(warning.Resource, namespace) == nil {
				continue
			}
		}
		warnings = append(warnings, warning)
	}
	return warnings
}

func NeighborhoodInventoryWarnings(topo *Topology, sub *Subgraph, opts NeighborhoodOptions, provider ResourceProvider, dp DynamicProvider) []InventoryWarning {
	if topo == nil || len(topo.IncompleteInventories) == 0 || len(sub.Nodes) == 0 {
		return nil
	}
	root := sub.Root
	obj, _ := lookupObjectMetadata(strings.ToLower(root.Kind), root.Namespace, root.Name, provider, dp)
	edges := edgeTypesForProfile(opts.Profile, sub.Nodes[0].Kind)
	var warnings []InventoryWarning
	for _, warning := range relevantInventoryWarnings(topo, root.Kind, root.Group, root.Namespace, obj, provider) {
		if !edges[EdgeConfigures] && (warning.Resource == "configmaps" || warning.Resource == "secrets" || warning.Resource == "serviceaccounts") {
			continue
		}
		if !edges[EdgeUses] && warning.Resource == "horizontalpodautoscalers" {
			continue
		}
		if !edges[EdgeProtects] && (warning.Resource == "networkpolicies" || warning.Resource == "poddisruptionbudgets") {
			continue
		}
		if !edges[EdgeExposes] && (warning.Resource == "services" || warning.Resource == "ingresses" || warning.Resource == "endpoints" || warning.Resource == "endpointslices") {
			continue
		}
		warnings = append(warnings, warning)
	}
	return warnings
}
