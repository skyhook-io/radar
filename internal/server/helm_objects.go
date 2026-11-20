package server

import (
	"net/http"

	"github.com/skyhook-io/radar/internal/helm"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/readscope"
	"github.com/skyhook-io/radar/internal/search"
)

// helmObjectScope builds the caller's view of cached objects for the Helm
// inventory, with the same gates as handleSearch: the namespaces they may see,
// exact-kind checks for the tightly gated kinds, and per-kind checks for
// cluster-scoped ones. The namespace picker narrows rows, not the read: a
// release's cluster-scoped objects and those in other namespaces still count.
func (s *Server) helmObjectScope(r *http.Request) *helm.ObjectScope {
	provider := search.NewCacheProvider()
	if provider == nil {
		return nil
	}
	ceiling, checkFailed := s.getUserNamespacesWithStatus(r, nil)
	var pin *string
	if k8s.ForceNamespaceScope {
		target := k8s.GetNamespaceScopeTarget()
		pin = &target
	}
	kindRBAC := s.computeSearchKindRBAC(r)
	access := search.ScanAccess{
		Selection:            search.SelectNamespaces(provider, search.Query{}, readscope.NamespaceAccess{Namespaces: ceiling, Checked: !checkFailed}, pin),
		NamespaceCheckFailed: checkFailed,
		TypedKindAccess:      kindRBAC,
		CanReadClusterScoped: func(_, group, resource string) (bool, bool) {
			return s.canReadDecision(r, group, resource, "", "list")
		},
		DynamicKindAccess: s.searchKindAccess(r, kindRBAC),
	}
	return &helm.ObjectScope{Provider: provider, Access: access, Requested: helm.RequestedNamespaces(parseNamespaces(r.URL.Query()), pin)}
}
