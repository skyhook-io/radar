package server

import (
	"log"
	"net/http"

	capacitymodel "github.com/skyhook-io/radar/internal/capacity"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/autoscalerstatus"
	"github.com/skyhook-io/radar/pkg/capacityapi"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	corelisters "k8s.io/client-go/listers/core/v1"
)

const (
	autoscalerStatusNamespace     = "kube-system"
	autoscalerStatusConfigMapName = "cluster-autoscaler-status"
)

// capacityConfigMapSource is the cache surface the autoscaler-status read
// needs: informer scope plus the ConfigMap lister. Narrowed to an interface so
// the coverage-verdict/detection pairing is testable without an informer.
type capacityConfigMapSource interface {
	capacityInformerScope
	ConfigMaps() corelisters.ConfigMapLister
}

// loadCapacityAutoscalerStatus reads and parses the cluster-autoscaler status
// ConfigMap — the only cloud-API-free source of node-group bounds and
// autoscaler health. Every failure mode gets its own coverage verdict: an
// absent ConfigMap is a true observation ("not published"), not an error. The
// returned detection carries that distinction to the groups engine, which
// otherwise cannot tell "no autoscaler here" from "we were not allowed to look".
func (s *Server) loadCapacityAutoscalerStatus(r *http.Request, meta *capacityapi.ResponseMeta) (*autoscalerstatus.Status, capacitymodel.AutoscalerDetection) {
	var cache capacityConfigMapSource
	if resourceCache := k8s.GetResourceCache(); resourceCache != nil {
		cache = resourceCache
	}
	status, coverage, detection := capacityAutoscalerStatus(s.canRead(r, "", "configmaps", autoscalerStatusNamespace, "get"), cache)
	meta.Coverage[capacityapi.CoverageAutoscalerStatus] = coverage
	return status, detection
}

func capacityAutoscalerStatus(allowed bool, cache capacityConfigMapSource) (*autoscalerstatus.Status, capacityapi.SourceCoverage, capacitymodel.AutoscalerDetection) {
	impact := []string{"groups.scaling", "groups.children", "summary.managers"}
	// Only kube-system is ever probed, so every verdict below is a statement
	// about that one namespace — carry it on the wire so nothing renders
	// "not published" as a cluster-wide fact.
	unavailable := func(coverage capacityapi.SourceCoverage) (*autoscalerstatus.Status, capacityapi.SourceCoverage, capacitymodel.AutoscalerDetection) {
		coverage.Namespaces = []string{autoscalerStatusNamespace}
		return nil, coverage, capacitymodel.AutoscalerUnavailable
	}
	if !allowed {
		return unavailable(deniedCoverage("autoscaler_status_configmap_denied", impact))
	}
	if cache == nil || !capacityCacheCoversNamespace(cache, "configmaps", autoscalerStatusNamespace) {
		return unavailable(unavailableCoverage("autoscaler_status_cache_scope", impact))
	}
	lister := cache.ConfigMaps()
	if lister == nil {
		return unavailable(unavailableCoverage("autoscaler_status_cache_unavailable", impact))
	}
	configMap, err := lister.ConfigMaps(autoscalerStatusNamespace).Get(autoscalerStatusConfigMapName)
	if apierrors.IsNotFound(err) {
		coverage := unavailableCoverage("autoscaler_status_not_published", impact)
		coverage.Namespaces = []string{autoscalerStatusNamespace}
		return nil, coverage, capacitymodel.AutoscalerNotPublished
	}
	if err != nil {
		log.Printf("[capacity] Failed to read %s/%s: %v", autoscalerStatusNamespace, autoscalerStatusConfigMapName, err)
		return unavailable(errorCoverage("autoscaler_status_read_failed", impact))
	}
	status, err := autoscalerstatus.Parse(configMap.Data["status"])
	if err != nil {
		log.Printf("[capacity] Failed to parse %s/%s: %v", autoscalerStatusNamespace, autoscalerStatusConfigMapName, err)
		return unavailable(errorCoverage("autoscaler_status_parse_failed", impact))
	}
	coverage := capacityapi.NewSourceCoverage(capacityapi.CoverageAvailable, capacityapi.CoverageScopeCluster)
	coverage.ImpactFields = impact
	coverage.Namespaces = []string{autoscalerStatusNamespace}
	itemCount := len(status.NodeGroups)
	coverage.ItemCount = &itemCount
	// The autoscaler's own payload time, not our read time: healthy quiet
	// clusters publish hours-old payloads, and freshness must render as
	// "as of T", never as broken.
	coverage.ObservedAt = status.Time
	return &status, coverage, capacitymodel.AutoscalerObserved
}

func (s *Server) loadKarpenterlessCapacityModel(r *http.Request, result *capacityLoadResult) {
	now := result.meta.GeneratedAt
	nodes := s.loadCapacityNodes(r, &result.meta, now)
	pods := s.loadCapacityPods(r, &result.meta, now)
	snapshot := capacitymodel.Snapshot{
		GeneratedAt: now,
		Nodes:       nodes,
		Pods:        pods,
		Coverage:    result.meta.Coverage,
	}
	model := capacitymodel.Build(snapshot)
	result.model = &model
	result.snapshot = &snapshot
}

func (s *Server) attachCapacityGroups(r *http.Request, result capacityLoadResult, response *capacityapi.OverviewResponse) {
	autoscalerState, detection := s.loadCapacityAutoscalerStatus(r, &response.ResponseMeta)
	groupsModel := capacitymodel.BuildGroupsModel(*result.snapshot, autoscalerState, detection, result.model, result.meta.GeneratedAt)
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
