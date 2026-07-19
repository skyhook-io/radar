package server

import (
	"net/http"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/capacityapi"
)

const (
	karpenterGroup            = "karpenter.sh"
	karpenterNodePoolKind     = "NodePool"
	karpenterNodePoolResource = "nodepools"
)

func (s *Server) karpenterCapability(r *http.Request) k8s.IntegrationCapability {
	connection := k8s.GetConnectionStatus()
	switch connection.State {
	case k8s.StateConnecting:
		return k8s.IntegrationCapability{State: capacityapi.IntegrationSyncing, ReasonCode: "cluster_connecting"}
	case k8s.StateConnected:
	default:
		return k8s.IntegrationCapability{State: capacityapi.IntegrationSyncing, ReasonCode: "cluster_disconnected"}
	}
	if k8s.GetClient() == nil {
		return k8s.IntegrationCapability{State: capacityapi.IntegrationSyncing, ReasonCode: "cluster_client_unavailable"}
	}
	discovery := k8s.GetResourceDiscovery()
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
	if !s.canRead(r, karpenterGroup, karpenterNodePoolResource, "", "list") {
		return k8s.IntegrationCapability{State: capacityapi.IntegrationDenied, ReasonCode: "nodepools_list_denied"}
	}

	cache := k8s.GetDynamicResourceCache()
	if cache == nil {
		return k8s.IntegrationCapability{State: capacityapi.IntegrationSyncing, ReasonCode: "dynamic_cache_unavailable"}
	}
	if cache.IsSynced(gvr) {
		return k8s.IntegrationCapability{State: capacityapi.IntegrationAvailable}
	}
	if err := cache.EnsureWatching(gvr); err != nil {
		// The CRD exists but this deployment's identity cannot watch it. For a
		// single-identity deployment that is denial, not a cache hiccup — the
		// per-user canRead above only covers auth-proxy mode. Mapping it to
		// denied lets the Overview soften to the cluster-only shape while the
		// Karpenter screens refuse, instead of an eternally empty "available".
		return k8s.IntegrationCapability{State: capacityapi.IntegrationDenied, ReasonCode: "nodepools_watch_denied", CacheUnavailable: true}
	}
	return k8s.IntegrationCapability{State: capacityapi.IntegrationSyncing, ReasonCode: "nodepools_syncing"}
}
