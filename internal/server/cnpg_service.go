package server

import (
	"context"
	"net/http"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"

	"github.com/skyhook-io/radar/internal/auth"
	cnpgsvc "github.com/skyhook-io/radar/internal/cnpg"
	"github.com/skyhook-io/radar/internal/integration"
	"github.com/skyhook-io/radar/internal/issues"
	"github.com/skyhook-io/radar/internal/k8s"
	prometheuspkg "github.com/skyhook-io/radar/internal/prometheus"
	bp "github.com/skyhook-io/radar/pkg/audit"
)

type cnpgReadAdapter struct {
	*cnpgsvc.Reader
	dynamic       dynamic.Interface
	config        *rest.Config
	actionContext string
}

func (s *Server) cnpgActionClients(r *http.Request, req integration.ActionRequest) (cnpgsvc.ActionClients, error) {
	reader := s.cnpgReader(r)
	if reader.dynamic == nil {
		return cnpgsvc.ActionClients{}, integration.RefuseAction(http.StatusServiceUnavailable, "", "cluster client not available — check cluster connection")
	}
	if err := integration.CheckReviewedContext(req.ReviewedContext, reader.actionContext); err != nil {
		return cnpgsvc.ActionClients{}, err
	}
	return cnpgsvc.ActionClients{Dynamic: reader.dynamic, Typed: reader.Clients.Typed, Exec: reader.Clients.Exec}, nil
}

func (s *Server) cnpgReader(r *http.Request) *cnpgReadAdapter {
	var promClient *prometheuspkg.Client
	var cache *k8s.ResourceCache
	var discovery *k8s.ResourceDiscovery
	var dynamicCache *k8s.DynamicResourceCache
	var operation context.Context
	var identity, clusterContext, target string
	var connected, forced bool
	adapter := &cnpgReadAdapter{Reader: &cnpgsvc.Reader{}}
	k8s.CaptureClusterReads(func() {
		operation = k8s.OperationContext()
		cache, discovery, dynamicCache = k8s.GetResourceCache(), k8s.GetResourceDiscovery(), k8s.GetDynamicResourceCache()
		connected = k8s.IsConnected()
		identity, clusterContext = cnpgRuntimeIdentity(r), k8s.ActiveClusterContext()
		forced, target = k8s.ForceNamespaceScope, k8s.GetNamespaceScopeTarget()
		promClient = prometheuspkg.GetClient()
		adapter.dynamic, adapter.actionContext = s.getDynamicClientSnapshotForRequest(r)
		adapter.config = s.getConfigForRequest(r)
		adapter.Clients = cnpgsvc.ReadClients{Typed: s.getClientForRequest(r), Proxy: cnpgRuntimeClient(r)}
		adapter.Clients.Exec = cnpgsvc.NewExec(adapter.Clients.Typed, adapter.config)
	})
	current := func() bool { return operation != nil && operation.Err() == nil }
	boundBudget := func(ctx context.Context) *syncBudget {
		budget := newSyncBudget(ctx)
		budget.bound, budget.discovery, budget.dynamicCache = true, discovery, dynamicCache
		return budget
	}
	budget := boundBudget(r.Context())
	auditConfig := getAuditConfig()
	clients := adapter.Clients
	adapter.Reader = &cnpgsvc.Reader{
		ClusterContext: clusterContext,
		Identity:       identity,
		Metrics: cnpgsvc.Metrics{
			Connection: func(ctx context.Context) (bool, error) {
				if promClient == nil {
					return false, nil
				}
				_, _, err := promClient.EnsureConnected(ctx)
				return true, err
			},
			PVCUsage: promClient.QueryPVCUsage, CNPGScope: promClient.ResolveCNPGScope, PVCScope: promClient.ResolvePVCScope,
			History: promClient.QueryCNPGHistory, FleetLag: promClient.QueryCNPGFleetLag, FleetSlots: promClient.QueryCNPGFleetSlots, DiskGrowth: promClient.QueryCNPGDiskGrowth,
		},
		Access: cnpgsvc.Access{
			CanRead: func(ctx context.Context, group, resource, namespace, verb string) bool {
				if !current() {
					return false
				}
				allowed := s.canRead(r.WithContext(ctx), group, resource, namespace, verb)
				return current() && allowed
			},
			Permission: func(ctx context.Context, grant auth.Grant) string {
				if !current() {
					return integration.PermissionDenied
				}
				permission := s.grantPermission(r.WithContext(ctx), grant)
				if !current() {
					return integration.PermissionDenied
				}
				return permission
			},
			MetricsRead: func(ctx context.Context, group, resource, namespace, verb string) bool {
				if !current() {
					return false
				}
				allowed := s.prometheusAuthGate(r.WithContext(ctx), group, resource, namespace, verb)
				return current() && allowed
			},
		},
		Observations: cnpgsvc.Observations{
			FilterAudit: func(results *bp.ScanResults) *bp.ScanResults { return applyAuditSettings(results, auditConfig) },
			WorkspaceRead: func(ctx context.Context, cache *k8s.ResourceCache, k integration.WorkspaceKind, namespaces, groups []string) (integration.KindAccess, []*unstructured.Unstructured) {
				if !current() {
					return integration.KindAccess{State: integration.KindCoverageSyncing}, nil
				}
				access, items := s.readWorkspaceKind(r.WithContext(ctx), cache, k, namespaces, groups, budget)
				if !current() {
					return integration.KindAccess{State: integration.KindCoverageSyncing}, nil
				}
				return access, items
			},
			Issues: func(ctx context.Context, namespaces []string) []issues.Issue {
				if !current() {
					return nil
				}
				rows := s.cnpgIssueRows(r.WithContext(ctx), namespaces)
				if !current() {
					return nil
				}
				return rows
			},
			Cache: cache, Discovery: discovery, Connected: connected,
			Cluster: func(ctx context.Context, namespace, name string, grants ...auth.Grant) (*k8s.ResourceCache, *unstructured.Unstructured, error) {
				if !connected || !current() {
					return nil, nil, cnpgsvc.ErrCNPGDisconnected
				}
				if err := s.authorizeCNPGCachedRead(r.WithContext(ctx), namespace, "clusters", grants...); err != nil {
					return nil, nil, err
				}
				if cache == nil {
					return nil, nil, &cnpgsvc.ReadFailure{Status: http.StatusServiceUnavailable, Message: "resource cache not available"}
				}
				if !current() {
					return nil, nil, cnpgsvc.ErrCNPGDisconnected
				}
				objects, err := listDynamicSyncedWithin(ctx, cache, "Cluster", cnpgsvc.Group, namespace, boundBudget(ctx))
				obj, err := cnpgsvc.SelectCluster(objects, err, namespace, name)
				obj, err = cnpgCachedResourceResult(obj, err, "Cluster", namespace, name)
				if !current() {
					return nil, nil, cnpgsvc.ErrCNPGDisconnected
				}
				return cache, obj, err
			},
			OperatorScope: func(ctx context.Context) []string {
				if !current() {
					return []string{}
				}
				var requested []string
				if forced {
					if target == "" {
						return []string{}
					}
					requested = []string{target}
				}
				namespaces := s.getUserNamespaces(r.WithContext(ctx), requested)
				if !current() {
					return []string{}
				}
				return namespaces
			},
			TypedScope: func(ctx context.Context, cache *k8s.ResourceCache, namespaces []string, group, resource string) (integration.KindAccess, []string) {
				if !current() {
					return integration.KindAccess{State: integration.KindCoverageSyncing}, []string{}
				}
				access, read := s.typedKindScopeWithCandidates(r.WithContext(ctx), cache, namespaces, group, resource, func() []string { return namespaceNamesInCache(cache) })
				if !current() {
					return integration.KindAccess{State: integration.KindCoverageSyncing}, []string{}
				}
				return access, read
			},
			DynamicList: func(ctx context.Context, cache *k8s.ResourceCache, kind, group, namespace string) ([]*unstructured.Unstructured, error) {
				if !current() {
					return nil, integration.ErrDynamicNotSynced
				}
				return listDynamicSyncedWithin(ctx, cache, kind, group, namespace, boundBudget(ctx))
			},
		},
		Clients: clients,
	}
	return adapter
}
