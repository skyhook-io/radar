package server

import (
	"context"
	"net/http"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/capacityapi"
)

const (
	karpenterGroup            = "karpenter.sh"
	karpenterNodePoolKind     = "NodePool"
	karpenterNodePoolResource = "nodepools"
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
	gvr, found := discovery.GetGVRWithGroup(karpenterNodePoolKind, karpenterGroup)
	if !found {
		if discovery.GroupHadPartialDiscovery(karpenterGroup) {
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
	if conn == k8s.Current() {
		return s.canRead(r, karpenterGroup, karpenterNodePoolResource, "", "list")
	}
	// The server's permission cache and access reviews follow the installed
	// cluster. A request still bound to a replaced connection asks its own.
	user := auth.UserFromContext(r.Context())
	if user == nil || s.permCache == nil {
		return true
	}
	ctx, cancel := context.WithTimeout(r.Context(), accessReviewTimeout)
	defer cancel()
	allowed, _ := canReadUserSARWith(ctx, conn.Client(), user, karpenterGroup, karpenterNodePoolResource, "", "list")
	return allowed
}
