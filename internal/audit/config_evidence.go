package audit

import (
	"slices"
	"strings"

	"github.com/skyhook-io/radar/internal/k8s"
	bp "github.com/skyhook-io/radar/pkg/audit"
	"github.com/skyhook-io/radar/pkg/configrefs"
	"github.com/skyhook-io/radar/pkg/k8score"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

var typedConfigConsumers = []string{"pods", "deployments", "statefulsets", "daemonsets", "jobs", "cronjobs", "serviceaccounts", "ingresses"}

func typedConfigCoverage(cache *k8s.ResourceCache, resource, namespace string) bool {
	if cache.KindReadinessFor(resource) != k8score.KindReady {
		return false
	}
	if namespace == "" {
		return cache.IsKindClusterWide(resource)
	}
	return cache.KindCoversNamespace(resource, namespace)
}

type configDependencies struct {
	resources               []k8s.APIResource
	complete                map[string]bool
	clusterIssuerNamespaces []string
}

func discoverConfigDependencies(disc *k8s.ResourceDiscovery) configDependencies {
	deps := configDependencies{complete: map[string]bool{}}
	if disc == nil {
		return deps
	}
	resources, err := disc.GetAPIResources()
	if err != nil || len(resources) == 0 {
		return deps
	}
	deps.complete["ConfigMap"], deps.complete["Secret"] = true, true
	for gr, dependency := range configRefDependencies {
		if disc.GroupHadPartialDiscovery(gr.Group) {
			deps.complete["Secret"] = false
			if dependency.configMaps {
				deps.complete["ConfigMap"] = false
			}
		}
	}
	for _, resource := range resources {
		if dynamicConfigRefHandlerFor(schema.GroupVersionResource{Group: resource.Group, Version: resource.Version, Resource: resource.Name}) != nil {
			deps.resources = append(deps.resources, resource)
		}
	}
	return deps
}

func configReferencesComplete(cache *k8s.ResourceCache, kind, ns string, scope *ReadScope, deps configDependencies) bool {
	if !deps.complete[kind] {
		return false
	}
	if scope != nil && scope.Namespaces != nil && (ns == "" || !slices.Contains(scope.Namespaces, ns)) {
		return false
	}
	for _, resource := range typedConfigConsumers {
		if kind == "ConfigMap" && (resource == "serviceaccounts" || resource == "ingresses") {
			continue
		}
		if !typedConfigCoverage(cache, resource, ns) {
			return false
		}
	}
	dynamic := k8s.GetDynamicResourceCache()
	for _, res := range deps.resources {
		gvr := schema.GroupVersionResource{Group: res.Group, Version: res.Version, Resource: res.Name}
		dependency := configRefDependencies[gvr.GroupResource()]
		if kind == "ConfigMap" && !dependency.configMaps {
			continue
		}
		if gvr.Group == "cert-manager.io" && gvr.Resource == "clusterissuers" {
			if deps.clusterIssuerNamespaces == nil {
				return false
			}
			if ns != "" && !slices.Contains(deps.clusterIssuerNamespaces, ns) {
				continue
			}
		}
		sourceNS := ns
		if dependency.crossNamespace || !res.Namespaced {
			sourceNS = ""
		}
		if scope != nil && ((res.Namespaced && sourceNS == "" && scope.Namespaces != nil) || (!res.Namespaced && !scope.allows(gvr, ""))) {
			return false
		}
		if dynamic == nil || !dynamic.IsNamespaceSynced(gvr, sourceNS) {
			return false
		}
	}
	return true
}

func collectConfigEvidence(cache *k8s.ResourceCache, input *bp.CheckInput, namespaces []string, scope *ReadScope) *bp.ConfigReferenceEvidence {
	result := &bp.ConfigReferenceEvidence{CompleteNamespaces: map[string][]string{}, ReflectionsComplete: map[string]bool{}}
	metadata := func(input *bp.CheckInput) []configrefs.Object {
		var objects []configrefs.Object
		for _, cm := range input.ConfigMaps {
			objects = append(objects, configrefs.Metadata("ConfigMap", cm))
		}
		for _, sec := range input.Secrets {
			if scope.allows(schema.GroupVersionResource{Resource: "secrets"}, sec.Namespace) {
				objects = append(objects, configrefs.Metadata("Secret", sec))
			}
		}
		return objects
	}
	subjects := metadata(input)
	result.Objects = subjects
	reflections := configrefs.BuildReflections(result.Objects)
	reflectors := len(reflections.Sources)+len(reflections.Unresolved)+len(reflections.Links) > 0
	evidence, evidenceNamespaces := input, namespaces
	if reflectors {
		evidenceNamespaces = nil
		if scope != nil {
			evidenceNamespaces = scope.Namespaces
		}
		evidence = CollectTypedInput(cache, evidenceNamespaces)
		result.Objects = metadata(evidence)
	}
	deps := discoverConfigDependencies(k8s.GetResourceDiscovery())
	var deployments = evidence.Deployments
	if typedConfigCoverage(cache, "deployments", "") && (scope == nil || scope.Namespaces == nil) {
		deployments = ListNamespaced(cache.Deployments(), nil)
		deps.clusterIssuerNamespaces = certManagerClusterResourceNamespaces(deployments)
	} else {
		deployments = nil
	}
	// No ClusterIssuers means their controller's credential namespace is irrelevant.
	if dynamic := k8s.GetDynamicResourceCache(); deps.clusterIssuerNamespaces == nil && dynamic != nil {
		for _, res := range deps.resources {
			if res.Group != "cert-manager.io" || res.Name != "clusterissuers" {
				continue
			}
			gvr := schema.GroupVersionResource{Group: res.Group, Version: res.Version, Resource: res.Name}
			if scope.allows(gvr, "") && dynamic.IsClusterWideSynced(gvr) {
				if issuers, err := dynamic.ListWatched(gvr); err == nil && len(issuers) == 0 {
					deps.clusterIssuerNamespaces = []string{}
				}
			}
		}
	}
	result.Refs = bp.CollectConfigObjectRefs(evidence)
	result.Refs = append(result.Refs, listDynamicConfigObjectRefs(evidenceNamespaces, dynamicConfigRefOptions{Scope: scope, ServiceAccounts: evidence.ServiceAccounts, Deployments: deployments})...)
	result.Refs = slices.DeleteFunc(result.Refs, func(ref bp.ConfigObjectRef) bool {
		return !scope.allows(schema.GroupVersionResource{Resource: strings.ToLower(ref.Kind) + "s"}, ref.Namespace)
	})
	for _, kind := range []string{"ConfigMap", "Secret"} {
		resource := strings.ToLower(kind) + "s"
		seen := map[string]bool{}
		for _, object := range subjects {
			if object.Kind != kind || seen[object.Namespace] {
				continue
			}
			seen[object.Namespace] = true
			if typedConfigCoverage(cache, resource, object.Namespace) && configReferencesComplete(cache, kind, object.Namespace, scope, deps) {
				result.CompleteNamespaces[kind] = append(result.CompleteNamespaces[kind], object.Namespace)
			}
		}
		if reflectors {
			result.ReflectionsComplete[kind] = typedConfigCoverage(cache, resource, "") && configReferencesComplete(cache, kind, "", scope, deps) && (kind != "Secret" || scope == nil || scope.SecretNamespaces == nil)
		}
	}
	return result
}
