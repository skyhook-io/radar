package k8score

import (
	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	policyv1 "k8s.io/api/policy/v1"
	storagev1 "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// DropManagedFields is a SharedInformer transform that reduces memory usage
// by removing managedFields and heavy annotations from cached objects.
// This is the union of transforms used by both Radar and skyhook-connector.
func DropManagedFields(obj any) (any, error) {
	if meta, ok := obj.(metav1.Object); ok {
		meta.SetManagedFields(nil)
	}

	// Special handling for Events — aggressively strip to essentials
	if event, ok := obj.(*corev1.Event); ok {
		return &corev1.Event{
			ObjectMeta: metav1.ObjectMeta{
				Name:              event.Name,
				Namespace:         event.Namespace,
				UID:               event.UID,
				ResourceVersion:   event.ResourceVersion,
				CreationTimestamp: event.CreationTimestamp,
			},
			InvolvedObject: event.InvolvedObject,
			Reason:         event.Reason,
			Message:        event.Message,
			Type:           event.Type,
			Count:          event.Count,
			FirstTimestamp: event.FirstTimestamp,
			LastTimestamp:  event.LastTimestamp,
		}, nil
	}

	// Drop heavy annotations from common resources
	switch obj.(type) {
	case *corev1.Pod, *corev1.Service, *corev1.Node, *corev1.Namespace,
		*corev1.PersistentVolumeClaim, *corev1.PersistentVolume,
		*corev1.ConfigMap, *corev1.Secret, *corev1.ServiceAccount,
		*appsv1.Deployment, *appsv1.DaemonSet, *appsv1.StatefulSet, *appsv1.ReplicaSet,
		*networkingv1.Ingress, *networkingv1.IngressClass,
		*batchv1.Job, *batchv1.CronJob,
		*autoscalingv2.HorizontalPodAutoscaler,
		*policyv1.PodDisruptionBudget, *storagev1.StorageClass:
		if meta, ok := obj.(metav1.Object); ok && meta.GetAnnotations() != nil {
			delete(meta.GetAnnotations(), "kubectl.kubernetes.io/last-applied-configuration")
		}
	}

	return obj, nil
}

// DropUnstructuredManagedFields is the dynamic-cache counterpart of
// DropManagedFields. It's the SharedInformer transform for dynamic
// informers — which carry *unstructured.Unstructured — and mirrors the
// typed-cache transform's intent: shrink cached objects before they hit
// the store.
//
// Mutates in-place. This is correct here: SharedInformer transforms run
// exactly once per object before the cache stores it, and the object is
// not visible to any other reader until after the transform returns. Do
// NOT call this from places that hand you an object off the cache —
// those need StripUnstructuredFields which deep-copies.
//
// Always strips:
//   - metadata.managedFields (like DropManagedFields does for typed kinds)
//   - kubectl.kubernetes.io/last-applied-configuration annotation
//
// For CustomResourceDefinitions specifically, also strips:
//   - spec.versions[].schema — the OpenAPI v3 schema. On operator-heavy
//     clusters a single CRD's schema is 50-100KB (think ArgoCD, cert-manager,
//     Istio) and a cluster with 100+ CRDs easily produces a multi-MB list
//     response. Radar's UI doesn't render CRD schemas anywhere — the
//     resource browser shows CRDs with generic name/age columns, and any
//     future "inspect CRD schema" feature would fetch fresh from the API
//     server rather than relying on cached list data.
//   - spec.conversion — the conversion webhook config (caBundle + URL).
//     Also not rendered. The K8s API server applies conversion webhooks
//     itself; we only need to display metadata about them, not the
//     runtime config.
//
// Explicitly preserves:
//   - spec.versions[].name and served/storage/deprecated flags
//   - spec.versions[].additionalPrinterColumns — these drive column hints
//     in generic resource list views
//   - spec.group, spec.names, spec.scope, status.* — all the fields that
//     describe what the CRD is, as opposed to what shape instances take
func DropUnstructuredManagedFields(obj any) (any, error) {
	u, ok := obj.(*unstructured.Unstructured)
	if !ok {
		// Dynamic informer should never hand us anything else, but be
		// defensive — return the object unchanged rather than erroring,
		// because a transform error is fatal for the informer.
		return obj, nil
	}

	unstructured.RemoveNestedField(u.Object, "metadata", "managedFields")

	if annotations := u.GetAnnotations(); annotations != nil {
		delete(annotations, "kubectl.kubernetes.io/last-applied-configuration")
		if len(annotations) == 0 {
			u.SetAnnotations(nil)
		} else {
			u.SetAnnotations(annotations)
		}
	}

	if u.GetKind() == "CustomResourceDefinition" {
		// Strip .schema from each version entry. Preserve everything
		// else (name, served, storage, deprecated, additionalPrinterColumns).
		if versions, found, _ := unstructured.NestedSlice(u.Object, "spec", "versions"); found {
			for i, v := range versions {
				vm, ok := v.(map[string]any)
				if !ok {
					continue
				}
				delete(vm, "schema")
				versions[i] = vm
			}
			// SetNestedSlice can only fail on non-serializable types; slices
			// of map[string]any are always fine. Ignoring the error is safe.
			_ = unstructured.SetNestedSlice(u.Object, versions, "spec", "versions")
		}

		// spec.conversion holds the webhook clientConfig (caBundle + URL)
		// for conversion webhooks. Not rendered by Radar.
		unstructured.RemoveNestedField(u.Object, "spec", "conversion")
	}

	return u, nil
}

// StripUnstructuredFields removes managedFields and the last-applied-configuration
// annotation from an unstructured object. Returns a deep copy — the cached object
// is never mutated. Safe for use by both Radar and skyhook-connector.
func StripUnstructuredFields(u *unstructured.Unstructured) *unstructured.Unstructured {
	if u == nil {
		return nil
	}

	cp := u.DeepCopy()

	unstructured.RemoveNestedField(cp.Object, "metadata", "managedFields")

	annotations := cp.GetAnnotations()
	if annotations != nil {
		delete(annotations, "kubectl.kubernetes.io/last-applied-configuration")
		if len(annotations) == 0 {
			cp.SetAnnotations(nil)
		} else {
			cp.SetAnnotations(annotations)
		}
	}

	return cp
}
