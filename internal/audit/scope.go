package audit

import (
	"slices"

	"github.com/skyhook-io/radar/internal/k8s"
	"golang.org/x/sync/errgroup"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// ReadScope authorizes Secret and cluster-scoped subjects. Other namespaced
// subjects retain Radar's namespace-visibility policy.
// Nil namespace slices allow all namespaces; allocated-empty slices allow none.
// ClusterResources is an explicit grant map: absent entries deny access.
type ReadScope struct {
	Namespaces       []string
	SecretNamespaces []string
	ClusterResources map[string]bool
}

// ResolveReadScope keeps namespace visibility separate from Secret and cluster-resource grants.
// A nil scope is the local, unauthenticated mode.
func ResolveReadScope(namespaces, secretNamespaces []string, canList func(group, resource, namespace string) bool) *ReadScope {
	var watched []schema.GroupVersionResource
	if dynamic := k8s.GetDynamicResourceCache(); dynamic != nil {
		watched = dynamic.WatchedGVRs()
	}
	return resolveReadScope(namespaces, secretNamespaces, watched, canList)
}

func resolveReadScope(namespaces, secretNamespaces []string, watched []schema.GroupVersionResource, canList func(group, resource, namespace string) bool) *ReadScope {
	if namespaces != nil && len(namespaces) == 0 {
		return &ReadScope{Namespaces: []string{}, SecretNamespaces: []string{}}
	}
	scope := &ReadScope{Namespaces: slices.Clone(namespaces), SecretNamespaces: slices.Clone(secretNamespaces), ClusterResources: map[string]bool{}}
	seen := map[schema.GroupResource]bool{}
	var groups []schema.GroupResource
	for _, gvr := range watched {
		if gvr.Group == "pkg.crossplane.io" || gvr.Group == "apiextensions.crossplane.io" {
			continue
		}
		gr := gvr.GroupResource()
		if !seen[gr] {
			groups = append(groups, gr)
			seen[gr] = true
		}
	}
	grants := make([]bool, len(groups))
	var checks errgroup.Group
	checks.SetLimit(16)
	for i, gr := range groups {
		checks.Go(func() error { grants[i] = canList(gr.Group, gr.Resource, ""); return nil })
	}
	_ = checks.Wait()
	for i, gr := range groups {
		scope.ClusterResources[gr.String()] = grants[i]
	}
	slices.Sort(scope.Namespaces)
	slices.Sort(scope.SecretNamespaces)
	return scope
}

func (s *ReadScope) allows(gvr schema.GroupVersionResource, ns string) bool {
	if s == nil {
		return true
	}
	if ns == "" {
		return s.ClusterResources[gvr.GroupResource().String()]
	}
	if s.Namespaces != nil && !slices.Contains(s.Namespaces, ns) {
		return false
	}
	return gvr.Group != "" || gvr.Resource != "secrets" || s.SecretNamespaces == nil || slices.Contains(s.SecretNamespaces, ns)
}

func (s *ReadScope) subjectNamespaces(selected []string) []string {
	if s == nil || s.Namespaces == nil {
		return selected
	}
	if len(selected) == 0 {
		return slices.Clone(s.Namespaces)
	}
	out := []string{}
	for _, ns := range selected {
		if slices.Contains(s.Namespaces, ns) {
			out = append(out, ns)
		}
	}
	return out
}

func (s *ReadScope) hasSecretSubjects(selected []string) bool {
	if s == nil || s.SecretNamespaces == nil {
		return true
	}
	for _, ns := range s.SecretNamespaces {
		if len(selected) == 0 || slices.Contains(selected, ns) {
			return true
		}
	}
	return false
}
