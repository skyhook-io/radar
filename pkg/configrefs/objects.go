package configrefs

import (
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
)

// ArgoRolloutsGroup is the API group of Argo Rollouts, whose Rollout declares
// a pod template the way a Deployment does.
const ArgoRolloutsGroup = "argoproj.io"

// VisitObjectReferences visits the ConfigMaps, Secrets,
// PersistentVolumeClaims and ServiceAccount an object declares for its Pods,
// as it reads each: a Pod's spec, the pod template of a Deployment,
// StatefulSet, DaemonSet, ReplicaSet, Job, CronJob or Argo Rollout, or an
// Ingress's TLS Secrets. Repeated declarations are visited separately, each
// with its path. ok is false for any other object. A Rollout whose template
// doesn't decode visits the declarations that can still be read and returns
// the decoding error; one with no template declares nothing.
func VisitObjectReferences(obj any, visit func(PodSpecReference)) (ok bool, err error) {
	switch o := obj.(type) {
	case *corev1.Pod:
		VisitPodSpecReferences(o.Namespace, o.Spec, visit)
	case *appsv1.Deployment:
		VisitPodSpecReferences(o.Namespace, o.Spec.Template.Spec, visit)
	case *appsv1.StatefulSet:
		VisitPodSpecReferences(o.Namespace, o.Spec.Template.Spec, visit)
	case *appsv1.DaemonSet:
		VisitPodSpecReferences(o.Namespace, o.Spec.Template.Spec, visit)
	case *appsv1.ReplicaSet:
		VisitPodSpecReferences(o.Namespace, o.Spec.Template.Spec, visit)
	case *batchv1.Job:
		VisitPodSpecReferences(o.Namespace, o.Spec.Template.Spec, visit)
	case *batchv1.CronJob:
		VisitPodSpecReferences(o.Namespace, o.Spec.JobTemplate.Spec.Template.Spec, visit)
	case *networkingv1.Ingress:
		for _, ref := range IngressTLSSecretReferences(o) {
			visit(PodSpecReference{Ref: ref, Path: "spec.tls[].secretName"})
		}
	case *unstructured.Unstructured:
		if !IsArgoRollout(o) {
			return false, nil
		}
		template, found, err := unstructured.NestedMap(o.Object, "spec", "template", "spec")
		if err != nil {
			return true, err
		}
		if !found {
			return true, nil
		}
		return true, VisitPodSpecMapReferences(o.GetNamespace(), template, visit)
	default:
		return false, nil
	}
	return true, nil
}

// IsArgoRollout reports whether u is an Argo Rollout.
func IsArgoRollout(u *unstructured.Unstructured) bool {
	if u == nil || u.GetKind() != "Rollout" {
		return false
	}
	group := u.GroupVersionKind().Group
	return group == ArgoRolloutsGroup
}

// VisitPodSpecMapReferences visits the declarations of a PodSpec held as an
// unstructured map. When the whole spec doesn't decode, it decodes each
// declaring part on its own, so an unrelated malformed field doesn't hide
// the declarations that can still be read, and returns the decoding error.
func VisitPodSpecMapReferences(namespace string, spec map[string]any, visit func(PodSpecReference)) error {
	var podSpec corev1.PodSpec
	err := runtime.DefaultUnstructuredConverter.FromUnstructured(spec, &podSpec)
	if err == nil {
		VisitPodSpecReferences(namespace, podSpec, visit)
		return nil
	}
	collect := func(part map[string]any) {
		var p corev1.PodSpec
		if runtime.DefaultUnstructuredConverter.FromUnstructured(part, &p) == nil {
			VisitPodSpecReferences(namespace, p, visit)
		}
	}
	for _, field := range []string{"containers", "initContainers", "ephemeralContainers"} {
		containers, _ := spec[field].([]any)
		for _, raw := range containers {
			container, _ := raw.(map[string]any)
			for _, envField := range []string{"env", "envFrom"} {
				entries, _ := container[envField].([]any)
				for _, entry := range entries {
					if envField == "env" {
						variable, _ := entry.(map[string]any)
						entry = map[string]any{"valueFrom": variable["valueFrom"]}
					}
					collect(map[string]any{field: []any{map[string]any{envField: []any{entry}}}})
				}
			}
		}
	}
	volumes, _ := spec["volumes"].([]any)
	for _, raw := range volumes {
		volume, _ := raw.(map[string]any)
		for field, value := range volume {
			if field == "projected" {
				projection, _ := value.(map[string]any)
				sources, _ := projection["sources"].([]any)
				for _, source := range sources {
					collect(map[string]any{"volumes": []any{map[string]any{"projected": map[string]any{"sources": []any{source}}}}})
				}
			} else {
				collect(map[string]any{"volumes": []any{map[string]any{field: value}}})
			}
		}
	}
	pullSecrets, _ := spec["imagePullSecrets"].([]any)
	for _, secret := range pullSecrets {
		collect(map[string]any{"imagePullSecrets": []any{secret}})
	}
	collect(map[string]any{"serviceAccountName": spec["serviceAccountName"]})
	return err
}
