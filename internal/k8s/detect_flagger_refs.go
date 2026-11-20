package k8s

import (
	"fmt"
	"time"

	"github.com/skyhook-io/radar/pkg/configrefs"
	"github.com/skyhook-io/radar/pkg/issuesapi"
	"github.com/skyhook-io/radar/pkg/resourceid"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func detectFlaggerMissingReferences(cache *ResourceCache, dynamicCache *DynamicResourceCache, discovery *ResourceDiscovery, namespace string, now time.Time) []Detection {
	gvr, known := discovery.GetGVRWithGroup("Canary", "flagger.app")
	if !known || gvr.Resource != "canaries" {
		return nil
	}
	canaries, err := dynamicCache.ListWatchedReadOnly(gvr)
	if err != nil {
		return nil
	}
	var out []Detection
	for _, canary := range canaries {
		if canary.GetDeletionTimestamp() != nil || namespace != "" && canary.GetNamespace() != namespace ||
			!dynamicCache.IsNamespaceSynced(gvr, canary.GetNamespace()) {
			continue
		}
		suspended, _, _ := unstructured.NestedBool(canary.Object, "spec", "suspend")
		if suspended {
			continue
		}
		if target, ok := configrefs.FlaggerTargetReference(canary); ok {
			targetGVR, found, readable := observedReference(cache, dynamicCache, discovery, target)
			if readable && !found {
				out = append(out, flaggerMissingReference(canary, target, targetGVR, "Missing Flagger target",
					fmt.Sprintf("Flagger's target reference looks up %s %q in namespace %q, which does not exist.", flaggerReferenceKind(target), target.Name, target.Namespace), now))
			}
		}
		skip, _, _ := unstructured.NestedBool(canary.Object, "spec", "skipAnalysis")
		if skip || configrefs.FlaggerAnalysis(canary) == nil {
			continue
		}
		for _, templateRef := range configrefs.FlaggerMetricTemplateReferences(canary) {
			templateGVR, found, readable := observedReference(cache, dynamicCache, discovery, templateRef)
			if !readable {
				continue
			}
			if !found {
				out = append(out, flaggerMissingReference(canary, templateRef, templateGVR, "Missing Flagger MetricTemplate",
					fmt.Sprintf("Canary analysis references MetricTemplate %q in namespace %q, which does not exist.", templateRef.Name, templateRef.Namespace), now))
				continue
			}
			template, exists, ready := dynamicCache.ReadableObject(templateGVR, templateRef.Namespace, templateRef.Name)
			if !ready || !exists || template.GetDeletionTimestamp() != nil {
				continue
			}
			secretRef, ok := configrefs.FlaggerProviderSecretReference(template)
			if !ok {
				continue
			}
			secretGVR, found, readable := observedReference(cache, dynamicCache, discovery, secretRef)
			if !readable || found {
				continue
			}
			issue := flaggerMissingReference(canary, secretRef, secretGVR, "Missing Flagger provider Secret",
				fmt.Sprintf("Canary analysis uses MetricTemplate %q in namespace %q, whose provider references Secret %q in that namespace, which does not exist.", templateRef.Name, templateRef.Namespace, secretRef.Name), now)
			issue.RequiredReads = append(issue.RequiredReads, issuesapi.EvidenceRead{Group: templateGVR.Group, Resource: templateGVR.Resource, Namespace: templateRef.Namespace, Verb: "list"})
			out = append(out, issue)
		}
	}
	return out
}

// observedReference reads the serving store atomically with its sync state.
// A lister miss from an unobserved namespace is never evidence of absence.
func observedReference(cache *ResourceCache, dynamicCache *DynamicResourceCache, discovery *ResourceDiscovery, ref resourceid.Ref) (gvr schema.GroupVersionResource, found, readable bool) {
	if builtin, ok := lookupTypedBuiltinGVR(ref.Kind); ok && builtin.Group == ref.Group {
		if cache == nil || cache.ResourceCache == nil {
			return builtin, false, false
		}
		_, found, readable = cache.ResourceCache.ReadableObject(builtin.Resource, ref.Namespace, ref.Name)
		return builtin, found, readable
	}
	gvr, known := discovery.GetGVRWithGroup(ref.Kind, ref.Group)
	if !known || dynamicCache == nil {
		return gvr, false, false
	}
	if ref.Group == "flagger.app" && ref.Kind == "MetricTemplate" && gvr.Resource != "metrictemplates" ||
		ref.Group == "serving.knative.dev" && ref.Kind == "Service" && gvr.Resource != "services" {
		return gvr, false, false
	}
	_, found, readable = dynamicCache.ReadableObject(gvr, ref.Namespace, ref.Name)
	return gvr, found, readable
}

func flaggerMissingReference(canary *unstructured.Unstructured, ref resourceid.Ref, gvr schema.GroupVersionResource, reason, message string, now time.Time) Detection {
	out := missingRefProblemSev(now, "Canary", "flagger.app", canary.GetNamespace(), canary.GetName(), "warning", reason, message, canary.GetCreationTimestamp().Time)
	out.RequiredReads = []issuesapi.EvidenceRead{{Group: gvr.Group, Resource: gvr.Resource, Namespace: ref.Namespace, Verb: "list"}}
	cause := "Metric checks using this reference cannot complete while it is missing. An analyzed rollout can fail or roll back; an idle Canary's next analyzed release is also affected. The stable primary may still be serving."
	if reason == "Missing Flagger target" {
		cause = "A missing declared target prevents Flagger from reconciling this Canary. The stable primary may still be serving."
	}
	return withFix(out,
		cause,
		fmt.Sprintf("Correct the reference or create %s %q in namespace %q. Inspect the Canary's events for the effect on its current rollout.", flaggerReferenceKind(ref), ref.Name, ref.Namespace))
}

func flaggerReferenceKind(ref resourceid.Ref) string {
	if ref.Kind == "Service" && ref.Group == "serving.knative.dev" {
		return "Knative Service"
	}
	return ref.Kind
}
