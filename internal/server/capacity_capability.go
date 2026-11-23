package server

import (
	"net/http"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/capacityapi"
	"github.com/skyhook-io/radar/pkg/karpenter"
)

func (s *Server) karpenterCapability(r *http.Request, conn *k8s.Connection) k8s.IntegrationCapability {
	connection := k8s.GetConnectionStatus()
	switch connection.State {
	case k8s.StateConnecting:
		return k8s.IntegrationCapability{State: capacityapi.IntegrationSyncing, ReasonCode: "cluster_connecting"}
	case k8s.StateConnected:
	default:
		return k8s.IntegrationCapability{State: capacityapi.IntegrationSyncing, ReasonCode: "cluster_disconnected"}
	}
	if conn.Client() == nil {
		return k8s.IntegrationCapability{State: capacityapi.IntegrationSyncing, ReasonCode: "cluster_client_unavailable"}
	}
	discovery := conn.Discovery()
	if discovery == nil {
		return k8s.IntegrationCapability{State: capacityapi.IntegrationSyncing, ReasonCode: "discovery_unavailable"}
	}
	gvr, found := discovery.GetGVRWithGroup(karpenter.NodePoolKind, karpenter.Group)
	if !found {
		if discovery.GroupHadPartialDiscovery(karpenter.Group) {
			return k8s.IntegrationCapability{State: capacityapi.IntegrationSyncing, ReasonCode: "karpenter_discovery_partial"}
		}
		return k8s.IntegrationCapability{State: capacityapi.IntegrationNotDetected}
	}
	if !s.canListNodePools(r, conn) {
		return k8s.IntegrationCapability{State: capacityapi.IntegrationDenied, ReasonCode: "nodepools_list_denied"}
	}

	cache := conn.DynamicResourceCache()
	if cache == nil {
		return k8s.IntegrationCapability{State: capacityapi.IntegrationSyncing, ReasonCode: "dynamic_cache_unavailable"}
	}
	if cache.IsSynced(gvr) {
		return k8s.IntegrationCapability{State: capacityapi.IntegrationAvailable}
	}
	if err := cache.EnsureWatching(gvr); err != nil {
		// The CRD exists but this deployment's identity cannot watch it. For a
		// single-identity deployment that is denial, not a cache hiccup — the
		// per-user list check above only covers auth-proxy mode. Mapping it to
		// denied lets the Overview soften to the cluster-only shape while the
		// Karpenter screens refuse, instead of an eternally empty "available".
		return k8s.IntegrationCapability{State: capacityapi.IntegrationDenied, ReasonCode: "nodepools_watch_denied", CacheUnavailable: true}
	}
	return k8s.IntegrationCapability{State: capacityapi.IntegrationSyncing, ReasonCode: "nodepools_syncing"}
}

func (s *Server) canListNodePools(r *http.Request, conn *k8s.Connection) bool {
	user := auth.UserFromContext(r.Context())
	if user == nil || s.permCache == nil {
		return true
	}
	return conn.CanListNodePools(r.Context(), user.Username, user.Groups)
}
