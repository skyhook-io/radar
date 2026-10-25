package k8s

import (
	"context"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/skyhook-io/radar/pkg/ownership"
	"github.com/skyhook-io/radar/pkg/readreason"
	"github.com/skyhook-io/radar/pkg/topology"
)

// DeletionPreview shares the caller-scoped deletion projection between REST
// and MCP. It refuses an answer across cache replacement or discovery startup.
func DeletionPreview(ctx context.Context, obj *unstructured.Unstructured, access func(*ownership.Catalog) ownership.Access) *topology.CascadeDeletePreview {
	p := &topology.CascadeDeletePreview{Root: topology.ResourceRef{Group: obj.GroupVersionKind().Group, Kind: obj.GetKind(), Namespace: obj.GetNamespace(), Name: obj.GetName()}, Dependents: []topology.ResourceRef{}, UnavailableReason: string(readreason.ClusterConnecting)}
	s, err := ownershipSource.Begin()
	if err != nil || len(s.Catalog().DependentKinds()) == 0 {
		return p
	}
	result := topology.PreviewDeletion(ctx, ownership.NewView(s, access(s.Catalog())), obj)
	if s.ValidateCaches() != nil {
		p.UnavailableReason = string(readreason.ConnectionChanged)
		return p
	}
	return result
}
