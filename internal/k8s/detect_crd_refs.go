package k8s

import (
	"fmt"
	"log"
	"time"

	"github.com/skyhook-io/radar/internal/logsafe"
	"github.com/skyhook-io/radar/pkg/topology"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// DetectMissingCRDRefs scans curated CRDs for explicit by-name references that
// point at missing targets. Keep this list conservative: only refs where the
// source spec directly names an object and the controller cannot perform its
// job without that object belong in the live issue stream. Selector-based or
// lifecycle-created targets should stay in resource-context enrichment.
func DetectMissingCRDRefs(cache *ResourceCache, dynamicCache *DynamicResourceCache, discovery *ResourceDiscovery, namespace string) []Detection {
	if cache == nil || dynamicCache == nil || discovery == nil {
		return nil
	}
	now := time.Now()
	var out []Detection
	out = append(out, detectRolloutMissingServices(cache, dynamicCache, discovery, namespace, now)...)
	out = append(out, detectKEDAMissingScaleTargets(cache, dynamicCache, discovery, namespace, now)...)
	return out
}

func detectRolloutMissingServices(cache *ResourceCache, dynamicCache *DynamicResourceCache, discovery *ResourceDiscovery, namespace string, now time.Time) []Detection {
	svcLister := cache.Services()
	if svcLister == nil {
		return nil
	}
	gvr, ok := discovery.GetGVRWithGroup("Rollout", "argoproj.io")
	if !ok {
		return nil
	}
	rollouts := listDynamicForMissingRefs(dynamicCache, gvr, namespace, "Rollout")
	var out []Detection
	for _, ro := range rollouts {
		age := now.Sub(ro.GetCreationTimestamp().Time)
		seen := map[string]bool{}
		for _, ref := range rolloutServiceRefs(ro) {
			if ref.name == "" || seen[ref.name] {
				continue
			}
			seen[ref.name] = true
			_, err := svcLister.Services(ro.GetNamespace()).Get(ref.name)
			checked, exists := rolloutServiceLookupResult(ro.GetNamespace(), ro.GetName(), ref.name, err)
			if !checked || exists {
				continue
			}
			out = append(out, withFix(missingRefProblemSev("Rollout", "argoproj.io", ro.GetNamespace(), ro.GetName(),
				"warning", "Missing Rollout Service",
				fmt.Sprintf("%s references Service %q which does not exist", ref.path, ref.name),
				age),
				fmt.Sprintf("Service %q doesn't exist, so the Rollout controller can't shift traffic during a rollout.", ref.name),
				fmt.Sprintf("Point the Rollout's %s at an existing Service in namespace %q, or create Service %q if the rollout should still use it.", ref.path, ro.GetNamespace(), ref.name)))
		}
	}
	return out
}

func rolloutServiceLookupResult(namespace, rolloutName, serviceName string, err error) (checked bool, exists bool) {
	if err == nil {
		return true, true
	}
	if apierrors.IsNotFound(err) {
		return true, false
	}
	log.Printf("[missing-refs] failed to verify Rollout %s/%s service ref %s: %s", logsafe.Sanitize(namespace), logsafe.Sanitize(rolloutName), logsafe.Sanitize(serviceName), logsafe.Sanitize(err.Error()))
	return false, false
}

type namedRef struct {
	path string
	name string
}

func rolloutServiceRefs(ro *unstructured.Unstructured) []namedRef {
	var refs []namedRef
	for _, item := range []struct {
		path []string
		name string
	}{
		{[]string{"spec", "strategy", "canary", "stableService"}, "spec.strategy.canary.stableService"},
		{[]string{"spec", "strategy", "canary", "canaryService"}, "spec.strategy.canary.canaryService"},
		{[]string{"spec", "strategy", "blueGreen", "activeService"}, "spec.strategy.blueGreen.activeService"},
		{[]string{"spec", "strategy", "blueGreen", "previewService"}, "spec.strategy.blueGreen.previewService"},
	} {
		name, found, _ := unstructured.NestedString(ro.Object, item.path...)
		if found && name != "" {
			refs = append(refs, namedRef{path: item.name, name: name})
		}
	}
	return refs
}

func detectKEDAMissingScaleTargets(cache *ResourceCache, dynamicCache *DynamicResourceCache, discovery *ResourceDiscovery, namespace string, now time.Time) []Detection {
	gvr, ok := discovery.GetGVRWithGroup("ScaledObject", "keda.sh")
	if !ok {
		return nil
	}
	scaledObjects := listDynamicForMissingRefs(dynamicCache, gvr, namespace, "ScaledObject")
	var out []Detection
	for _, so := range scaledObjects {
		ref, ok := kedaScaleTargetRef(so)
		if !ok {
			continue
		}
		checked, exists := scaleTargetExists(cache, dynamicCache, discovery, so.GetNamespace(), ref)
		if !checked || exists {
			continue
		}
		age := now.Sub(so.GetCreationTimestamp().Time)
		out = append(out, withFix(missingRefProblemSev("ScaledObject", "keda.sh", so.GetNamespace(), so.GetName(),
			"warning", "Missing scaleTargetRef",
			fmt.Sprintf("spec.scaleTargetRef references %s %q which does not exist", ref.kind, ref.name),
			age),
			fmt.Sprintf("%s %q doesn't exist, so KEDA has nothing to scale.", ref.kind, ref.name),
			fmt.Sprintf("Point spec.scaleTargetRef at an existing workload in namespace %q, remove the ScaledObject if the target is obsolete, or create %s %q if it should still exist.", so.GetNamespace(), ref.kind, ref.name)))
	}
	return out
}

type scaleTargetRef struct {
	apiGroup string
	kind     string
	name     string
}

func kedaScaleTargetRef(so *unstructured.Unstructured) (scaleTargetRef, bool) {
	name, _, _ := unstructured.NestedString(so.Object, "spec", "scaleTargetRef", "name")
	kind, _, _ := unstructured.NestedString(so.Object, "spec", "scaleTargetRef", "kind")
	apiVersion, _, _ := unstructured.NestedString(so.Object, "spec", "scaleTargetRef", "apiVersion")
	if name == "" {
		return scaleTargetRef{}, false
	}
	if kind == "" {
		kind = "Deployment"
	}
	return scaleTargetRef{
		apiGroup: topology.APIVersionGroup(apiVersion),
		kind:     kind,
		name:     name,
	}, true
}

func scaleTargetExists(cache *ResourceCache, dynamicCache *DynamicResourceCache, discovery *ResourceDiscovery, namespace string, ref scaleTargetRef) (checked bool, exists bool) {
	switch ref.kind {
	case "Deployment":
		if ref.apiGroup != "" && ref.apiGroup != "apps" {
			return false, false
		}
		l := cache.Deployments()
		if l == nil {
			return false, false
		}
		_, err := l.Deployments(namespace).Get(ref.name)
		return scaleTargetLookupResult("Deployment", namespace, ref.name, err)
	case "StatefulSet":
		if ref.apiGroup != "" && ref.apiGroup != "apps" {
			return false, false
		}
		l := cache.StatefulSets()
		if l == nil {
			return false, false
		}
		_, err := l.StatefulSets(namespace).Get(ref.name)
		return scaleTargetLookupResult("StatefulSet", namespace, ref.name, err)
	case "DaemonSet":
		if ref.apiGroup != "" && ref.apiGroup != "apps" {
			return false, false
		}
		l := cache.DaemonSets()
		if l == nil {
			return false, false
		}
		_, err := l.DaemonSets(namespace).Get(ref.name)
		return scaleTargetLookupResult("DaemonSet", namespace, ref.name, err)
	case "Rollout":
		if ref.apiGroup != "" && ref.apiGroup != "argoproj.io" {
			return false, false
		}
		gvr, ok := discovery.GetGVRWithGroup("Rollout", "argoproj.io")
		if !ok {
			return false, false
		}
		return dynamicScaleTargetExists(dynamicCache, gvr, namespace, "Rollout", ref.name)
	default:
		return false, false
	}
}

func scaleTargetLookupResult(kind, namespace, name string, err error) (checked bool, exists bool) {
	if err == nil {
		return true, true
	}
	if apierrors.IsNotFound(err) {
		return true, false
	}
	log.Printf("[missing-refs] failed to verify %s %s/%s scaleTargetRef: %s", logsafe.Sanitize(kind), logsafe.Sanitize(namespace), logsafe.Sanitize(name), logsafe.Sanitize(err.Error()))
	return false, false
}

func dynamicScaleTargetExists(dynamicCache *DynamicResourceCache, gvr schema.GroupVersionResource, namespace, kind, name string) (checked bool, exists bool) {
	items, err := dynamicCache.ListWatched(gvr)
	if err != nil {
		log.Printf("[missing-refs] failed to verify %s %s/%s scaleTargetRef: %s", logsafe.Sanitize(kind), logsafe.Sanitize(namespace), logsafe.Sanitize(name), logsafe.Sanitize(err.Error()))
		return false, false
	}
	for _, item := range items {
		if item.GetNamespace() == namespace && item.GetName() == name {
			return true, true
		}
	}
	return true, false
}

func listDynamicForMissingRefs(dynamicCache *DynamicResourceCache, gvr schema.GroupVersionResource, namespace, kind string) []*unstructured.Unstructured {
	var items []*unstructured.Unstructured
	var err error
	if namespace == "" {
		items, err = dynamicCache.ListWatched(gvr)
	} else {
		items, err = dynamicCache.List(gvr, namespace)
	}
	if err != nil {
		log.Printf("[missing-refs] failed to list %s.%s: %s", logsafe.Sanitize(kind), logsafe.Sanitize(gvr.Group), logsafe.Sanitize(err.Error()))
		return nil
	}
	return items
}
