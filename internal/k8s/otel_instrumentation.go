package k8s

import (
	"strings"

	"github.com/skyhook-io/radar/pkg/configrefs"
	"github.com/skyhook-io/radar/pkg/resourcecontext"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation"
	clientcache "k8s.io/client-go/tools/cache"
)

const instrumentationAnnotationPrefix = "instrumentation.opentelemetry.io/inject-"

var instrumentationLanguages = []string{"apache-httpd", "dotnet", "go", "java", "nginx", "nodejs", "php", "python", "ruby", "sdk"}

// AutoInstrumentationForObject resolves declarations only. It never starts a
// watch, reads Secret/config values, or asserts injection/delivery success.
// canRead must include both namespace scope and the exact inventory verb.
// A nil result with false means no context was produced; nil with true means
// the observation was unavailable or discarded after a connection change.
func AutoInstrumentationForObject(obj runtime.Object, canRead func(group, resource, namespace, verb string) bool) (result *resourcecontext.AutoInstrumentationContext, unavailable bool) {
	meta, annotations, path, kind, group, ok := instrumentationPodAnnotations(obj)
	template := path != "/metadata/annotations"
	if !ok {
		return nil, false
	}
	typed, dynamic, discovery := GetResourceCache(), GetDynamicResourceCache(), GetResourceDiscovery()
	if typed == nil || typed.ResourceCache == nil {
		return nil, true
	}
	gvr, known := discovery.GetGVRWithGroup("Instrumentation", "opentelemetry.io")
	known = known && gvr.Resource == "instrumentations"
	out := &resourcecontext.AutoInstrumentationContext{}
	subject := resourcecontext.ContextRef{Kind: kind, Group: group, Namespace: meta.GetNamespace(), Name: meta.GetName()}
	if template {
		for _, language := range instrumentationLanguages {
			key := instrumentationAnnotationPrefix + language
			if meta.GetAnnotations()[key] != "" {
				out.IgnoredWorkloadMetadataAnnotations = append(out.IgnoredWorkloadMetadataAnnotations, key)
			}
		}
	}
	// No installed native kind or explicit declaration: avoid adding lookup
	// uncertainty to unrelated workloads. An omitted block is not no injection.
	explicit := false
	for _, language := range instrumentationLanguages {
		if annotations[instrumentationAnnotationPrefix+language] != "" {
			explicit = true
			break
		}
	}
	if !known && !explicit && len(out.IgnoredWorkloadMetadataAnnotations) == 0 {
		return nil, false
	}
	var namespaceAnnotations map[string]string
	namespaceKnown := false
	// Namespace GETs carry their own name as the request namespace, so a
	// grant scoped to this Namespace is sufficient; listing all is unnecessary.
	if canRead("", "namespaces", meta.GetNamespace(), "get") {
		ns, found, readable := typed.ResourceCache.ReadableObject("namespaces", "", meta.GetNamespace())
		if found && readable {
			if ns, ok := ns.(*corev1.Namespace); ok {
				namespaceAnnotations = ns.Annotations
				namespaceKnown = true
			}
		}
	}
	out.NamespaceDefaultsUnknown = !namespaceKnown
	for _, language := range instrumentationLanguages {
		key := instrumentationAnnotationPrefix + language
		value, fromNamespace := configrefs.EffectiveInstrumentationAnnotation(namespaceAnnotations, annotations, key)
		if value == "" {
			continue
		}
		request := resourcecontext.InstrumentationRequest{Language: language, Source: subject, Resolution: resourcecontext.InstrumentationUnknown, AnnotationPath: path + "/instrumentation.opentelemetry.io~1inject-" + language}
		if fromNamespace {
			request.Source = resourcecontext.ContextRef{Kind: "Namespace", Name: meta.GetNamespace()}
			request.AnnotationPath = "/metadata/annotations/instrumentation.opentelemetry.io~1inject-" + language
		}
		switch {
		case !namespaceKnown && strings.EqualFold(annotations[key], "true"):
			// A hidden Namespace may carry a named selection inherited by true.
		case strings.EqualFold(value, "false"):
			request.Resolution = resourcecontext.InstrumentationDisabled
		case strings.EqualFold(value, "true"):
			request.AssumesOperatorCRDMode = true
			if known && dynamic != nil && canRead(gvr.Group, gvr.Resource, meta.GetNamespace(), "list") {
				objects, readable := dynamic.ReadableIndexed(gvr, meta.GetNamespace(), clientcache.NamespaceIndex, meta.GetNamespace())
				if readable {
					switch len(objects) {
					case 0:
						request.Resolution = resourcecontext.InstrumentationDefaultMissing
					case 1:
						// Native default selection uses List; its grant already
						// reveals the selected identity, even without Get.
						request.Resolution = resourcecontext.InstrumentationDefaultResolved
						request.Instrumentation = &resourcecontext.ContextRef{Kind: "Instrumentation", Group: gvr.Group, Namespace: meta.GetNamespace(), Name: objects[0].GetName()}
					default:
						request.Resolution = resourcecontext.InstrumentationDefaultAmbiguous
					}
				}
			}
		default:
			namespace, name, namespaced := strings.Cut(value, "/")
			if !namespaced {
				name, namespace = value, meta.GetNamespace()
			}
			if len(validation.IsDNS1123Label(namespace)) != 0 || len(validation.IsDNS1123Subdomain(name)) != 0 {
				request.Resolution = resourcecontext.InstrumentationInvalidReference
				break
			}
			request.Instrumentation = &resourcecontext.ContextRef{Kind: "Instrumentation", Group: "opentelemetry.io", Namespace: namespace, Name: name}
			if known && dynamic != nil && canRead(gvr.Group, gvr.Resource, namespace, "get") {
				_, found, readable := dynamic.ReadableObject(gvr, namespace, name)
				if readable {
					request.Resolution = resourcecontext.InstrumentationMissing
					if found {
						request.Resolution = resourcecontext.InstrumentationResolved
					}
				}
			}
		}
		out.RequestsIfPodCreatedNow = append(out.RequestsIfPodCreatedNow, request)
	}
	if GetResourceCache() != typed || GetDynamicResourceCache() != dynamic || GetResourceDiscovery() != discovery {
		return nil, true
	}
	if len(out.RequestsIfPodCreatedNow) == 0 && len(out.IgnoredWorkloadMetadataAnnotations) == 0 && !out.NamespaceDefaultsUnknown {
		return nil, false
	}
	return out, false
}

// No generic nesting guess: annotations must be on a real Pod template that
// Radar already understands, rather than arbitrary CRD metadata/spec keys.
func instrumentationPodAnnotations(obj runtime.Object) (metav1.Object, map[string]string, string, string, string, bool) {
	switch v := obj.(type) {
	case *corev1.Pod:
		return v, v.Annotations, "/metadata/annotations", "Pod", "", true
	case *appsv1.Deployment:
		return v, v.Spec.Template.Annotations, "/spec/template/metadata/annotations", "Deployment", "apps", true
	case *appsv1.StatefulSet:
		return v, v.Spec.Template.Annotations, "/spec/template/metadata/annotations", "StatefulSet", "apps", true
	case *appsv1.DaemonSet:
		return v, v.Spec.Template.Annotations, "/spec/template/metadata/annotations", "DaemonSet", "apps", true
	case *appsv1.ReplicaSet:
		return v, v.Spec.Template.Annotations, "/spec/template/metadata/annotations", "ReplicaSet", "apps", true
	case *batchv1.Job:
		return v, v.Spec.Template.Annotations, "/spec/template/metadata/annotations", "Job", "batch", true
	case *batchv1.CronJob:
		return v, v.Spec.JobTemplate.Spec.Template.Annotations, "/spec/jobTemplate/spec/template/metadata/annotations", "CronJob", "batch", true
	case *unstructured.Unstructured:
		if v.GroupVersionKind().Group == "argoproj.io" && v.GetKind() == "Rollout" {
			if template, ok := rolloutPodTemplate(v); ok {
				return v, template.Annotations, "/spec/template/metadata/annotations", "Rollout", "argoproj.io", true
			}
		}
	}
	return nil, nil, "", "", "", false
}
