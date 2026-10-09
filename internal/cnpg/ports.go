package cnpg

import (
	"context"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/kubernetes"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/integration"
	"github.com/skyhook-io/radar/internal/issues"
	"github.com/skyhook-io/radar/internal/k8s"
	bp "github.com/skyhook-io/radar/pkg/audit"
)

type Access struct {
	CanRead     func(context.Context, string, string, string, string) bool
	Permission  func(context.Context, auth.Grant) string
	MetricsRead func(context.Context, string, string, string, string) bool
}

type Observations struct {
	FilterAudit func(*bp.ScanResults) *bp.ScanResults

	WorkspaceRead func(context.Context, *k8s.ResourceCache, integration.WorkspaceKind, []string, []string) (integration.KindAccess, []*unstructured.Unstructured)
	Issues        func(context.Context, []string) []issues.Issue

	Cache         *k8s.ResourceCache
	Connected     bool
	Discovery     *k8s.ResourceDiscovery
	Cluster       func(context.Context, string, string, ...auth.Grant) (*k8s.ResourceCache, *unstructured.Unstructured, error)
	OperatorScope func(context.Context) []string
	TypedScope    func(context.Context, *k8s.ResourceCache, []string, string, string) (integration.KindAccess, []string)
	DynamicList   func(context.Context, *k8s.ResourceCache, string, string, string) ([]*unstructured.Unstructured, error)
}

type ReadClients struct {
	Exec ExecFunc

	Typed kubernetes.Interface
	Proxy kubernetes.Interface
}

type Reader struct {
	Metrics Metrics

	Access         Access
	Observations   Observations
	Clients        ReadClients
	Identity       string
	ClusterContext string
}
