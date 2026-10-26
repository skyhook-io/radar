package k8s

import (
	"context"
	"fmt"
	pkgauth "github.com/skyhook-io/radar/pkg/auth"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/skyhook-io/radar/pkg/ownership"
	"github.com/skyhook-io/radar/pkg/readreason"
	"github.com/skyhook-io/radar/pkg/topology"
)

// DeletionPreview shares the caller-scoped deletion projection between REST
// and MCP. It refuses an answer across cache replacement or discovery startup.
func DeletionPreview(ctx context.Context, obj *unstructured.Unstructured, operation context.Context, access func(*ownership.Catalog) ownership.Access) *topology.CascadeDeletePreview {
	p := &topology.CascadeDeletePreview{Root: topology.ResourceRef{Group: obj.GroupVersionKind().Group, Kind: obj.GetKind(), Namespace: obj.GetNamespace(), Name: obj.GetName()}, Dependents: []topology.ResourceRef{}, UnavailableReason: string(readreason.ClusterConnecting)}
	if operation.Err() != nil {
		p.UnavailableReason = string(readreason.ConnectionChanged)
		return p
	}
	s, err := ownershipSource.Begin()
	if err != nil {
		return p
	}
	if len(s.Catalog().DependentKinds()) == 0 {
		if s.Catalog().Complete() {
			p.UnavailableReason = string(readreason.ReadError)
		}
		return p
	}
	result := topology.PreviewDeletion(ctx, ownership.NewView(s, access(s.Catalog())), obj)
	if s.ValidateCaches() != nil || operation.Err() != nil {
		p.UnavailableReason = string(readreason.ConnectionChanged)
		return p
	}
	return result
}

// DeletionClientSnapshot captures the caller client and the operation lifecycle
// together. Client construction runs outside the brief dependency capture.
func DeletionClientSnapshot(ctx context.Context) (dynamic.Interface, string, context.Context, error) {
	var operation context.Context
	var name string
	var client dynamic.Interface
	var config *rest.Config
	if !CaptureClusterReads(func() {
		operation = OperationContext()
		client, name = GetDynamicClientSnapshot()
		config, _ = GetConfigSnapshot()
	}) {
		return nil, "", nil, ErrContextConfigurationBusy
	}
	if user := pkgauth.UserFromContext(ctx); user != nil {
		if config == nil {
			return nil, name, operation, fmt.Errorf("K8s config not initialized")
		}
		var err error
		client, err = dynamic.NewForConfig(UserRESTConfig(config, user))
		if err != nil {
			return nil, name, operation, err
		}
	}
	if err := operation.Err(); err != nil {
		return nil, name, operation, fmt.Errorf("cluster connection changed while preparing deletion: %w", err)
	}
	return client, name, operation, nil
}
