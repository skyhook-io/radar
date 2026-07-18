package server

import (
	"log"
	"net/http"

	capacitymodel "github.com/skyhook-io/radar/internal/capacity"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/autoscalerstatus"
	"github.com/skyhook-io/radar/pkg/capacityapi"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
)

const (
	autoscalerStatusNamespace     = "kube-system"
	autoscalerStatusConfigMapName = "cluster-autoscaler-status"
)

// loadCapacityAutoscalerStatus reads and parses the cluster-autoscaler status
// ConfigMap — the only cloud-API-free source of node-group bounds and
// autoscaler health. Every failure mode gets its own coverage verdict: an
// absent ConfigMap is a true observation ("not published"), not an error.
func (s *Server) loadCapacityAutoscalerStatus(r *http.Request, meta *capacityapi.ResponseMeta) *autoscalerstatus.Status {
	impact := []string{"groups.scaling", "groups.children", "summary.managers"}
	if !s.canRead(r, "", "configmaps", autoscalerStatusNamespace, "get") {
		meta.Coverage[capacityapi.CoverageAutoscalerStatus] = deniedCoverage("autoscaler_status_configmap_denied", impact)
		return nil
	}
	cache := k8s.GetResourceCache()
	if cache == nil || !capacityCacheCoversNamespace(cache, "configmaps", autoscalerStatusNamespace) {
		meta.Coverage[capacityapi.CoverageAutoscalerStatus] = unavailableCoverage("autoscaler_status_cache_scope", impact)
		return nil
	}
	lister := cache.ConfigMaps()
	if lister == nil {
		meta.Coverage[capacityapi.CoverageAutoscalerStatus] = unavailableCoverage("autoscaler_status_cache_unavailable", impact)
		return nil
	}
	configMap, err := lister.ConfigMaps(autoscalerStatusNamespace).Get(autoscalerStatusConfigMapName)
	if apierrors.IsNotFound(err) {
		meta.Coverage[capacityapi.CoverageAutoscalerStatus] = unavailableCoverage("autoscaler_status_not_published", impact)
		return nil
	}
	if err != nil {
		log.Printf("[capacity] Failed to read %s/%s: %v", autoscalerStatusNamespace, autoscalerStatusConfigMapName, err)
		meta.Coverage[capacityapi.CoverageAutoscalerStatus] = errorCoverage("autoscaler_status_read_failed", impact)
		return nil
	}
	status, err := autoscalerstatus.Parse(configMap.Data["status"])
	if err != nil {
		log.Printf("[capacity] Failed to parse %s/%s: %v", autoscalerStatusNamespace, autoscalerStatusConfigMapName, err)
		meta.Coverage[capacityapi.CoverageAutoscalerStatus] = errorCoverage("autoscaler_status_parse_failed", impact)
		return nil
	}
	coverage := capacityapi.NewSourceCoverage(capacityapi.CoverageAvailable, capacityapi.CoverageScopeCluster)
	coverage.ImpactFields = impact
	itemCount := len(status.NodeGroups)
	coverage.ItemCount = &itemCount
	// The autoscaler's own payload time, not our read time: healthy quiet
	// clusters publish hours-old payloads, and freshness must render as
	// "as of T", never as broken.
	coverage.ObservedAt = status.Time
	meta.Coverage[capacityapi.CoverageAutoscalerStatus] = coverage
	return &status
}

func (s *Server) attachCapacityGroups(r *http.Request, result capacityLoadResult, response *capacityapi.OverviewResponse) {
	autoscalerState := s.loadCapacityAutoscalerStatus(r, &response.ResponseMeta)
	groupsModel := capacitymodel.BuildGroupsModel(*result.snapshot, autoscalerState, result.model, result.meta.GeneratedAt)
	response.Groups = groupsModel.Groups
	response.OrphanAutoscalerGroups = groupsModel.OrphanAutoscalerGroups
	response.OrphanAutoscalerGroupsMeta = groupsModel.OrphanMeta
	response.Summary.Managers = groupsModel.Managers
	response.Summary.ClusterScheduling = groupsModel.ClusterScheduling
	if capacityCoverageObserved(result.meta.Coverage[capacityapi.CoverageNodes]) {
		unattributed := groupsModel.UnattributedNodeCount
		response.Summary.UnattributedNodeCount = &unattributed
	}
}
