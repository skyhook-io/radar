package cnpg

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/selection"

	auth "github.com/skyhook-io/radar/internal/auth"
	integration "github.com/skyhook-io/radar/internal/integration"
	"github.com/skyhook-io/radar/internal/k8s"
	prometheuspkg "github.com/skyhook-io/radar/internal/prometheus"
	"github.com/skyhook-io/radar/pkg/prom"
)

const (
	cnpgPVCRoleLabel        = "cnpg.io/pvcRole"
	instanceNameLabel       = "cnpg.io/instanceName"
	cnpgTablespaceNameLabel = "cnpg.io/tablespaceName"

	cnpgPVCRoleData       = "PG_DATA"
	cnpgPVCRoleWAL        = "PG_WAL"
	cnpgPVCRoleTablespace = "PG_TABLESPACE"

	storageStateOK              = "ok"
	cnpgStorageStatePartial     = "partial"
	storageStateDenied          = "denied"
	cnpgStorageStateUnavailable = "unavailable"
	cnpgStorageStateError       = "error"

	cnpgUsageStateOK           = "ok"
	cnpgUsageStateNoSeries     = "noSeries"
	cnpgUsageStateInvalid      = "invalid"
	cnpgUsageStateNoPrometheus = "noPrometheus"
	cnpgUsageStateDenied       = "denied"
	cnpgUsageStateError        = "error"
	usageStateNotRead          = "notRead"

	cnpgDiskWarningRatio  = 0.80
	cnpgDiskCriticalRatio = 0.90

	usageSource = "Prometheus kubelet_volume_stats_used_bytes / kubelet_volume_stats_capacity_bytes"
)

// The fleet reads at most this many namespaces per request, a few at a time:
// each costs two Prometheus queries.
var (
	fleetDiskMaxNamespaces = 64
	fleetDiskConcurrency   = 4
)

// CNPGStorageCoverage states whether one source could be read and, when not,
// the grant that would allow it.
type CNPGStorageCoverage struct {
	integration.ReadSource
	// Isolation, on usage read from Prometheus: how the series were tied to this cluster.
	Isolation *prometheuspkg.SeriesIsolation `json:"isolation,omitempty"`
}

// CNPGClusterStorageResponse is GET /api/cnpg/clusters/{namespace}/{name}/storage.
type CNPGClusterStorageResponse struct {
	Cluster   CNPGRuntimeObjectRef `json:"cluster"`
	SampledAt string               `json:"sampledAt"`
	// Volumes: listing the claims. Usage: Prometheus kubelet volume stats.
	// WAL: the instance manager and exporter through pods/proxy.
	Volumes   CNPGStorageCoverage     `json:"volumes"`
	Usage     CNPGStorageCoverage     `json:"usage"`
	UsageFrom string                  `json:"usageSource"`
	WAL       CNPGStorageCoverage     `json:"wal"`
	Expansion CNPGStorageExpansion    `json:"expansion"`
	Instances []CNPGStorageInstance   `json:"instances"`
	Findings  []CNPGStorageFinding    `json:"findings"`
	Excluded  []CNPGStorageExcludedPV `json:"excluded,omitempty"`
}

// CNPGStorageExpansion names where each volume's size is declared: the only
// place a resize can be made, which the operator then applies to the claims.
type CNPGStorageExpansion struct {
	Targets []CNPGStorageTarget `json:"targets"`
	// ResizeInUseVolumes is spec.storage.resizeInUseVolumes; absent means the
	// field is unset, which CNPG treats as true.
	ResizeInUseVolumes *bool `json:"resizeInUseVolumes,omitempty"`
}

type CNPGStorageTarget struct {
	Role         string `json:"role"`
	Tablespace   string `json:"tablespace,omitempty"`
	Field        string `json:"field"`
	Declared     string `json:"declared,omitempty"`
	StorageClass string `json:"storageClass,omitempty"`
}

type CNPGStorageInstance struct {
	Name    string              `json:"name"`
	Role    string              `json:"role"`
	Volumes []CNPGStorageVolume `json:"volumes"`
	WAL     *CNPGStorageWAL     `json:"wal,omitempty"`
}

type CNPGStorageVolume struct {
	Claim          string                 `json:"claim"`
	Role           string                 `json:"role"`
	Tablespace     string                 `json:"tablespace,omitempty"`
	Phase          string                 `json:"phase,omitempty"`
	Requested      string                 `json:"requested,omitempty"`
	RequestedBytes *int64                 `json:"requestedBytes,omitempty"`
	Capacity       string                 `json:"capacity,omitempty"`
	CapacityBytes  *int64                 `json:"capacityBytes,omitempty"`
	StorageClass   CNPGStorageClassFact   `json:"storageClass"`
	Resize         CNPGStorageResize      `json:"resize"`
	ClusterState   string                 `json:"clusterState,omitempty"`
	Usage          CNPGStorageVolumeUsage `json:"usage"`
}

// CNPGStorageClassFact: AllowVolumeExpansion is absent when the class could
// not be read; a readable class without the field does not allow expansion.
type CNPGStorageClassFact struct {
	Name                 string `json:"name,omitempty"`
	AllowVolumeExpansion *bool  `json:"allowVolumeExpansion,omitempty"`
	Reason               string `json:"reason,omitempty"`
}

// CNPGStorageResize carries what the claim itself reports about a resize.
type CNPGStorageResize struct {
	Conditions []CNPGStorageResizeCondition `json:"conditions,omitempty"`
	// AllocatedStatus is status.allocatedResourceStatuses.storage.
	AllocatedStatus string `json:"allocatedStatus,omitempty"`
	// Pending: the request is larger than the capacity the claim reports.
	Pending bool `json:"pending,omitempty"`
}

type CNPGStorageResizeCondition struct {
	Type    string `json:"type"`
	Message string `json:"message,omitempty"`
	Since   string `json:"since,omitempty"`
}

// CNPGStorageVolumeUsage carries figures only when State is ok.
type CNPGStorageVolumeUsage struct {
	State         string   `json:"state"`
	UsedBytes     *int64   `json:"usedBytes,omitempty"`
	CapacityBytes *int64   `json:"capacityBytes,omitempty"`
	Ratio         *float64 `json:"ratio,omitempty"`
}

// CNPGStorageWAL keeps each measure separate: the WAL directory's size, the
// segments waiting to be archived and the WAL each slot retains overlap, so
// they are never added up.
type CNPGStorageWAL struct {
	Status                 CNPGRuntimeSource `json:"status"`
	Metrics                CNPGRuntimeSource `json:"metrics"`
	Volume                 string            `json:"volume,omitempty"`
	SizeBytes              *float64          `json:"sizeBytes,omitempty"`
	Segments               *float64          `json:"segments,omitempty"`
	ReadyToArchive         *int              `json:"readyToArchive,omitempty"`
	LastArchivedAt         string            `json:"lastArchivedAt,omitempty"`
	LastFailedAt           string            `json:"lastFailedAt,omitempty"`
	LastFailedWal          string            `json:"lastFailedWal,omitempty"`
	ArchivingFailed        bool              `json:"archivingFailed,omitempty"`
	Slots                  []CNPGSlotBytes   `json:"slots,omitempty"`
	SlotInventory          []CNPGSlotStatus  `json:"slotInventory"`
	SlotInventoryTruncated bool              `json:"slotInventoryTruncated,omitempty"`
}

type CNPGStorageFinding struct {
	Severity   string  `json:"severity"`
	Instance   string  `json:"instance"`
	Claim      string  `json:"claim"`
	Role       string  `json:"role"`
	Tablespace string  `json:"tablespace,omitempty"`
	Ratio      float64 `json:"ratio"`
	Message    string  `json:"message"`
}

// CNPGStorageExcludedPV is a claim labelled for the Cluster that the Cluster
// does not own, or that names no instance. A label is something any object
// can carry, so it is listed rather than counted as the Cluster's volume.
type CNPGStorageExcludedPV struct {
	Claim  string `json:"claim"`
	Reason string `json:"reason"`
}

func (s *Reader) ClusterStorage(ctx context.Context, namespace, name string) (*CNPGClusterStorageResponse, error) {
	cache, cluster, err := s.Observations.Cluster(ctx, namespace, name)
	if err != nil {
		return nil, err
	}
	resp := CNPGClusterStorageResponse{
		Cluster:   CNPGRuntimeObjectRef{Namespace: namespace, Name: name, UID: cluster.GetUID()},
		SampledAt: time.Now().UTC().Format(time.RFC3339),
		UsageFrom: usageSource,
		Expansion: cnpgStorageExpansionOf(cluster),
		Instances: []CNPGStorageInstance{},
		Findings:  []CNPGStorageFinding{},
	}

	claims, excluded, volumes := s.clusterClaims(ctx, cache, cluster)
	resp.Volumes, resp.Excluded = volumes, excluded
	classes := s.storageClasses(ctx, cache, claims)
	states := cnpgClusterPVCStates(cluster)

	usage := CNPGStorageCoverage{ReadSource: integration.ReadSource{State: usageStateNotRead, Reason: "no volumes to measure"}}
	var batch prometheuspkg.PVCUsageBatch
	if len(claims) > 0 {
		usage, batch = s.claimUsage(ctx, namespace, claimNames(claims), historyAnchors(cache, cluster))
	}
	resp.Usage = usage

	byInstance := map[string]*CNPGStorageInstance{}
	instance := func(n string) *CNPGStorageInstance {
		if in, ok := byInstance[n]; ok {
			return in
		}
		in := &CNPGStorageInstance{Name: n, Role: cnpgStorageInstanceRole(cluster, n), Volumes: []CNPGStorageVolume{}}
		byInstance[n] = in
		return in
	}
	for _, n := range cnpgStatusStrings(cluster, "instanceNames") {
		instance(n)
	}
	for _, pvc := range claims {
		v := cnpgStorageVolumeOf(pvc, classes, states)
		v.Usage = cnpgVolumeUsage(usage, batch, pvc.Name)
		in := instance(pvc.Labels[instanceNameLabel])
		in.Volumes = append(in.Volumes, v)
	}

	resp.WAL, err = s.storageWAL(ctx, cache, cluster, byInstance)
	if err != nil {
		return nil, err
	}

	for _, in := range byInstance {
		sort.Slice(in.Volumes, func(i, j int) bool {
			a, b := in.Volumes[i], in.Volumes[j]
			if a.Role != b.Role {
				return cnpgPVCRoleRank(a.Role) < cnpgPVCRoleRank(b.Role)
			}
			return a.Claim < b.Claim
		})
		resp.Instances = append(resp.Instances, *in)
		resp.Findings = append(resp.Findings, cnpgDiskFindings(in.Name, in.Volumes, resp.Usage.Isolation)...)
	}
	sort.Slice(resp.Instances, func(i, j int) bool { return resp.Instances[i].Name < resp.Instances[j].Name })
	sort.SliceStable(resp.Findings, func(i, j int) bool { return resp.Findings[i].Ratio > resp.Findings[j].Ratio })
	return &resp, nil
}

// cnpgClusterClaims lists the claims the Cluster owns, after the caller's own
// list grant — reading a Cluster never implies reading its claims.
func (s *Reader) clusterClaims(ctx context.Context, cache *k8s.ResourceCache, cluster *unstructured.Unstructured) ([]*corev1.PersistentVolumeClaim, []CNPGStorageExcludedPV, CNPGStorageCoverage) {
	namespace := cluster.GetNamespace()
	grant := cnpgGrantListPVCs.In(namespace).Ref()
	if !s.Access.CanRead(ctx, "", "persistentvolumeclaims", namespace, "list") {
		return nil, nil, CNPGStorageCoverage{ReadSource: integration.ReadSource{State: storageStateDenied, Grant: grant}}
	}
	candidates, reason := cnpgCachedClaims(cache, namespace, labels.SelectorFromSet(labels.Set{clusterLabel: cluster.GetName()}))
	if reason != "" {
		return nil, nil, CNPGStorageCoverage{ReadSource: integration.ReadSource{State: cnpgStorageStateUnavailable, Grant: grant, Reason: reason}}
	}
	owned, excluded := cnpgOwnedClaims(candidates, cluster)
	return owned, excluded, CNPGStorageCoverage{ReadSource: integration.ReadSource{State: storageStateOK, Grant: grant}}
}

// cnpgCachedClaims reads claims from Radar's informer. What the informer does
// not hold is unread, not empty, so that is a reason rather than no claims.
func cnpgCachedClaims(cache *k8s.ResourceCache, namespace string, selector labels.Selector) ([]*corev1.PersistentVolumeClaim, string) {
	lister := cache.PersistentVolumeClaims()
	if lister == nil {
		return nil, "Radar's own identity cannot list persistentvolumeclaims"
	}
	if within := integration.NamespacesWithinCache(cache, "persistentvolumeclaims", []string{namespace}); within.Unavailable {
		return nil, "Radar's own identity cannot list persistentvolumeclaims in " + namespace
	}
	items, err := lister.PersistentVolumeClaims(namespace).List(selector)
	if err != nil {
		log.Printf("[cnpg] Failed to list claims in %s: %v", namespace, err)
		return nil, "listing persistentvolumeclaims failed"
	}
	return items, ""
}

// cnpgOwnedClaims keeps claims that name an instance and are owned by this
// Cluster's UID.
func cnpgOwnedClaims(candidates []*corev1.PersistentVolumeClaim, cluster *unstructured.Unstructured) ([]*corev1.PersistentVolumeClaim, []CNPGStorageExcludedPV) {
	var owned []*corev1.PersistentVolumeClaim
	var excluded []CNPGStorageExcludedPV
	for _, pvc := range candidates {
		if pvc == nil {
			continue
		}
		switch {
		case !cnpgOwnedBy(pvc, cluster) && cnpgHasClusterOwner(pvc):
			excluded = append(excluded, CNPGStorageExcludedPV{Claim: pvc.Name, Reason: "owned by a different Cluster object (UID mismatch)"})
		case !cnpgOwnedBy(pvc, cluster):
			excluded = append(excluded, CNPGStorageExcludedPV{Claim: pvc.Name, Reason: "carries this cluster's labels but no owner reference to it, as a claim kept after its instance was destroyed does"})
		case pvc.Labels[instanceNameLabel] == "":
			excluded = append(excluded, CNPGStorageExcludedPV{Claim: pvc.Name, Reason: "names no instance (" + instanceNameLabel + ")"})
		default:
			owned = append(owned, pvc)
		}
	}
	sort.Slice(owned, func(i, j int) bool { return owned[i].Name < owned[j].Name })
	sort.Slice(excluded, func(i, j int) bool { return excluded[i].Claim < excluded[j].Claim })
	return owned, excluded
}

func cnpgOwnedBy(pvc *corev1.PersistentVolumeClaim, cluster *unstructured.Unstructured) bool {
	for _, ref := range pvc.OwnerReferences {
		if ref.Kind == "Cluster" && ref.UID == cluster.GetUID() && strings.HasPrefix(ref.APIVersion, Group+"/") {
			return true
		}
	}
	return false
}

func cnpgHasClusterOwner(pvc *corev1.PersistentVolumeClaim) bool {
	for _, ref := range pvc.OwnerReferences {
		if ref.Kind == "Cluster" && strings.HasPrefix(ref.APIVersion, Group+"/") {
			return true
		}
	}
	return false
}

func claimNames(claims []*corev1.PersistentVolumeClaim) []string {
	out := make([]string, len(claims))
	for i, c := range claims {
		out[i] = c.Name
	}
	return out
}

// cnpgStorageClasses reads each named class once, when the caller may get
// StorageClasses.
func (s *Reader) storageClasses(ctx context.Context, cache *k8s.ResourceCache, claims []*corev1.PersistentVolumeClaim) map[string]CNPGStorageClassFact {
	out := map[string]CNPGStorageClassFact{}
	allowed := s.Access.CanRead(ctx, "storage.k8s.io", "storageclasses", "", "get")
	lister := cache.StorageClasses()
	for _, pvc := range claims {
		if pvc.Spec.StorageClassName == nil || *pvc.Spec.StorageClassName == "" {
			continue
		}
		name := *pvc.Spec.StorageClassName
		if _, done := out[name]; done {
			continue
		}
		fact := CNPGStorageClassFact{Name: name}
		switch {
		case !allowed:
			fact.Reason = "needs get storageclasses"
		case lister == nil:
			fact.Reason = "Radar's own credentials cannot list StorageClasses"
		default:
			sc, err := lister.Get(name)
			if err != nil || sc == nil {
				fact.Reason = "StorageClass " + name + " not found"
				break
			}
			allow := sc.AllowVolumeExpansion != nil && *sc.AllowVolumeExpansion
			fact.AllowVolumeExpansion = &allow
		}
		out[name] = fact
	}
	return out
}

// cnpgClusterPVCStates maps each claim to the Cluster status list naming it.
func cnpgClusterPVCStates(cluster *unstructured.Unstructured) map[string]string {
	out := map[string]string{}
	for _, f := range []struct{ field, state string }{
		{"healthyPVC", "healthy"},
		{"initializingPVC", "initializing"},
		{"resizingPVC", "resizing"},
		{"danglingPVC", "dangling"},
		{"unusablePVC", "unusable"},
	} {
		for _, n := range cnpgStatusStrings(cluster, f.field) {
			out[n] = f.state
		}
	}
	return out
}

func cnpgStatusStrings(cluster *unstructured.Unstructured, field string) []string {
	v, _, _ := unstructured.NestedStringSlice(cluster.Object, "status", field)
	return v
}

func cnpgStorageInstanceRole(cluster *unstructured.Unstructured, instance string) string {
	primary, _, _ := unstructured.NestedString(cluster.Object, "status", "currentPrimary")
	switch {
	case instance == "":
		return "unknown"
	case primary != "" && instance == primary:
		return "primary"
	case primary != "":
		for _, n := range cnpgStatusStrings(cluster, "instanceNames") {
			if n == instance {
				return "replica"
			}
		}
		return "noInstance"
	}
	return "unknown"
}

func cnpgPVCRoleRank(role string) int {
	switch role {
	case cnpgPVCRoleData:
		return 0
	case cnpgPVCRoleWAL:
		return 1
	case cnpgPVCRoleTablespace:
		return 2
	}
	return 3
}

var cnpgResizeConditions = map[corev1.PersistentVolumeClaimConditionType]bool{
	corev1.PersistentVolumeClaimResizing:                true,
	corev1.PersistentVolumeClaimFileSystemResizePending: true,
	"ControllerResizeError":                             true,
	"NodeResizeError":                                   true,
}

func cnpgStorageVolumeOf(pvc *corev1.PersistentVolumeClaim, classes map[string]CNPGStorageClassFact, states map[string]string) CNPGStorageVolume {
	v := CNPGStorageVolume{
		Claim:        pvc.Name,
		Role:         pvc.Labels[cnpgPVCRoleLabel],
		Tablespace:   pvc.Labels[cnpgTablespaceNameLabel],
		Phase:        string(pvc.Status.Phase),
		ClusterState: states[pvc.Name],
	}
	if q, ok := pvc.Spec.Resources.Requests[corev1.ResourceStorage]; ok {
		b := q.Value()
		v.Requested, v.RequestedBytes = q.String(), &b
	}
	if q, ok := pvc.Status.Capacity[corev1.ResourceStorage]; ok {
		b := q.Value()
		v.Capacity, v.CapacityBytes = q.String(), &b
	}
	if pvc.Spec.StorageClassName != nil && *pvc.Spec.StorageClassName != "" {
		v.StorageClass = classes[*pvc.Spec.StorageClassName]
	} else {
		v.StorageClass = CNPGStorageClassFact{Reason: "the claim names no StorageClass"}
	}
	for _, c := range pvc.Status.Conditions {
		if !cnpgResizeConditions[c.Type] || c.Status != corev1.ConditionTrue {
			continue
		}
		rc := CNPGStorageResizeCondition{Type: string(c.Type), Message: c.Message}
		if !c.LastTransitionTime.IsZero() {
			rc.Since = c.LastTransitionTime.UTC().Format(time.RFC3339)
		}
		v.Resize.Conditions = append(v.Resize.Conditions, rc)
	}
	if st, ok := pvc.Status.AllocatedResourceStatuses[corev1.ResourceStorage]; ok {
		v.Resize.AllocatedStatus = string(st)
	}
	if v.RequestedBytes != nil && v.CapacityBytes != nil && *v.RequestedBytes > *v.CapacityBytes {
		v.Resize.Pending = true
	}
	return v
}

// cnpgClaimUsage reads kubelet volume stats for the claims, behind the same
// gate as the single-claim PVC chart.
func (s *Reader) claimUsage(ctx context.Context, namespace string, claims []string, anchors []prom.WorkloadPodIdentity) (CNPGStorageCoverage, prometheuspkg.PVCUsageBatch) {
	grant := grantGetPVCs.In(namespace).Ref()
	if !s.Access.MetricsRead(ctx, "", "persistentvolumeclaims", namespace, "get") {
		return CNPGStorageCoverage{ReadSource: integration.ReadSource{State: cnpgUsageStateDenied, Grant: grant}}, prometheuspkg.PVCUsageBatch{}
	}
	batch := s.Metrics.PVCUsage(ctx, namespace, claims, anchors)
	switch batch.Status {
	case prometheuspkg.PVCUsageNoPrometheus:
		return CNPGStorageCoverage{ReadSource: integration.ReadSource{State: cnpgUsageStateNoPrometheus, Reason: cnpgNoPrometheusReason(batch.Error)}}, batch
	case prometheuspkg.PVCUsageQueryFailed:
		return CNPGStorageCoverage{ReadSource: integration.ReadSource{State: cnpgUsageStateError, Reason: "Prometheus query failed: " + truncateCNPGRuntimeError(batch.Error)}}, batch
	case prometheuspkg.PVCUsageAmbiguous:
		_, reason := usageScopeFailure(prometheuspkg.ErrScopeAmbiguous)
		return CNPGStorageCoverage{ReadSource: integration.ReadSource{State: cnpgHistoryStateAmbiguous, Reason: reason}}, batch
	case prometheuspkg.PVCUsageScopeMismatch:
		_, reason := usageScopeFailure(prometheuspkg.ErrScopeMismatch)
		return CNPGStorageCoverage{ReadSource: integration.ReadSource{State: cnpgHistoryStateScopeMismatch, Reason: reason}}, batch
	}
	iso := &batch.Isolation
	switch n := len(batch.Usage); {
	case n == 0:
		return CNPGStorageCoverage{ReadSource: integration.ReadSource{State: cnpgUsageStateNoSeries, Reason: "Prometheus has no kubelet volume stats for these claims"}}, batch
	case n < len(claims):
		return CNPGStorageCoverage{ReadSource: integration.ReadSource{State: cnpgStorageStatePartial, Reason: fmt.Sprintf("%d of %d claims have kubelet volume stats", n, len(claims))}, Isolation: iso}, batch
	}
	return CNPGStorageCoverage{ReadSource: integration.ReadSource{State: cnpgUsageStateOK}, Isolation: iso}, batch
}

// cnpgUnverifiedCaveat qualifies a measurement stated as this cluster's when
// its series were matched by name alone.
func cnpgUnverifiedCaveat(iso *prometheuspkg.SeriesIsolation) string {
	if iso == nil || iso.Mode != prometheuspkg.SeriesIsolationUnverified {
		return ""
	}
	return ". " + iso.Note
}

func cnpgVolumeUsage(cov CNPGStorageCoverage, batch prometheuspkg.PVCUsageBatch, claim string) CNPGStorageVolumeUsage {
	switch cov.State {
	case cnpgUsageStateOK, cnpgStorageStatePartial, cnpgUsageStateNoSeries:
	default:
		return CNPGStorageVolumeUsage{State: cov.State}
	}
	if u, ok := batch.Usage[claim]; ok {
		used, capacity, ratio := u.UsedBytes, u.CapacityBytes, u.Ratio
		return CNPGStorageVolumeUsage{State: cnpgUsageStateOK, UsedBytes: &used, CapacityBytes: &capacity, Ratio: &ratio}
	}
	if batch.Invalid[claim] {
		return CNPGStorageVolumeUsage{State: cnpgUsageStateInvalid}
	}
	return CNPGStorageVolumeUsage{State: cnpgUsageStateNoSeries}
}

func cnpgVolumeLabel(role, tablespace string) string {
	switch role {
	case cnpgPVCRoleData:
		return "data volume"
	case cnpgPVCRoleWAL:
		return "WAL volume"
	case cnpgPVCRoleTablespace:
		return "tablespace " + tablespace + " volume"
	}
	return "volume"
}

// cnpgDiskFindings reports volumes whose measured use crosses the thresholds.
// Only a measurement can raise one; an unmeasured volume says nothing.
func cnpgDiskFindings(instance string, volumes []CNPGStorageVolume, iso *prometheuspkg.SeriesIsolation) []CNPGStorageFinding {
	var out []CNPGStorageFinding
	for _, v := range volumes {
		if v.Usage.Ratio == nil || *v.Usage.Ratio < cnpgDiskWarningRatio {
			continue
		}
		sev := "warning"
		if *v.Usage.Ratio >= cnpgDiskCriticalRatio {
			sev = "critical"
		}
		out = append(out, CNPGStorageFinding{
			Severity: sev, Instance: instance, Claim: v.Claim, Role: v.Role, Tablespace: v.Tablespace, Ratio: *v.Usage.Ratio,
			Message: fmt.Sprintf("The %s of %s is %.0f%% full%s", cnpgVolumeLabel(v.Role, v.Tablespace), instance, *v.Usage.Ratio*100, cnpgUnverifiedCaveat(iso)),
		})
	}
	return out
}

// cnpgStorageExpansionOf names the spec field each volume's size comes from.
func cnpgStorageExpansionOf(cluster *unstructured.Unstructured) CNPGStorageExpansion {
	out := CNPGStorageExpansion{Targets: []CNPGStorageTarget{cnpgStorageTargetOf(cluster.Object, cnpgPVCRoleData, "", "spec.storage", "spec", "storage")}}
	if _, found, _ := unstructured.NestedMap(cluster.Object, "spec", "walStorage"); found {
		out.Targets = append(out.Targets, cnpgStorageTargetOf(cluster.Object, cnpgPVCRoleWAL, "", "spec.walStorage", "spec", "walStorage"))
	}
	tablespaces, _, _ := unstructured.NestedSlice(cluster.Object, "spec", "tablespaces")
	for _, raw := range tablespaces {
		ts, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		name, _ := ts["name"].(string)
		out.Targets = append(out.Targets, cnpgStorageTargetOf(ts, cnpgPVCRoleTablespace, name, "spec.tablespaces[name="+name+"].storage", "storage"))
	}
	if v, found, _ := unstructured.NestedBool(cluster.Object, "spec", "storage", "resizeInUseVolumes"); found {
		out.ResizeInUseVolumes = &v
	}
	return out
}

func cnpgStorageTargetOf(obj map[string]any, role, tablespace, base string, path ...string) CNPGStorageTarget {
	t := CNPGStorageTarget{Role: role, Tablespace: tablespace, Field: base + ".size"}
	if size, found, _ := unstructured.NestedString(obj, append(append([]string{}, path...), "size")...); found && size != "" {
		t.Declared = size
	} else if req, found, _ := unstructured.NestedString(obj, append(append([]string{}, path...), "pvcTemplate", "resources", "requests", "storage")...); found && req != "" {
		t.Field, t.Declared = base+".pvcTemplate.resources.requests.storage", req
	}
	if sc, found, _ := unstructured.NestedString(obj, append(append([]string{}, path...), "storageClass")...); found {
		t.StorageClass = sc
	}
	return t
}

// cnpgStorageWAL reads each instance's WAL facts through the same memoized
// pods/proxy reads the Replication and Performance tabs use. It writes the error and returns an
// empty state only when the caller's client cannot be built.
func (s *Reader) storageWAL(callerCtx context.Context, cache *k8s.ResourceCache, cluster *unstructured.Unstructured, byInstance map[string]*CNPGStorageInstance) (CNPGStorageCoverage, error) {
	namespace := cluster.GetNamespace()
	if !s.Access.CanRead(callerCtx, "", "pods", namespace, "list") {
		return CNPGStorageCoverage{ReadSource: integration.ReadSource{State: storageStateDenied, Grant: GrantListPods.In(namespace).Ref()}}, nil
	}
	grant := grantGetPodsProxy.In(namespace).Ref()
	if s.Access.Permission(callerCtx, grantGetPodsProxy.In(namespace)) == integration.PermissionDenied {
		return CNPGStorageCoverage{ReadSource: integration.ReadSource{State: storageStateDenied, Grant: grant}}, nil
	}
	pods, err := clusterInstancePods(cache, cluster)
	if err != nil {
		log.Printf("[cnpg] Failed to list instance Pods for %s/%s: %v", namespace, cluster.GetName(), err)
		return CNPGStorageCoverage{ReadSource: integration.ReadSource{State: cnpgStorageStateUnavailable, Grant: grant, Reason: "instance Pods unavailable"}}, nil
	}
	if len(pods) == 0 {
		return CNPGStorageCoverage{ReadSource: integration.ReadSource{State: cnpgStorageStateUnavailable, Grant: grant, Reason: "no instance Pods"}}, nil
	}
	client := s.Clients.Proxy
	if client == nil {
		return CNPGStorageCoverage{}, &ReadFailure{http.StatusServiceUnavailable, "cluster client unavailable"}
	}

	metricsTLS, _, _ := unstructured.NestedBool(cluster.Object, "spec", "monitoring", "tls", "enabled")
	fenced := parseCNPGFenced(cluster.GetAnnotations()[cnpgFencedAnnotation])
	identity := s.Identity
	walVolume := map[string]string{}
	for n, in := range byInstance {
		for _, v := range in.Volumes {
			if v.Role == cnpgPVCRoleWAL || (v.Role == cnpgPVCRoleData && walVolume[n] == "") {
				walVolume[n] = v.Claim
			}
		}
	}

	results := make([]CNPGStorageWAL, len(pods))
	run := newCNPGRuntimeRunner(callerCtx)
	for i, p := range pods {
		statusTarget, metricsTarget := cnpgInstanceProxyTargets(p, metricsTLS)
		out := &results[i]
		run.do(func(ctx context.Context) {
			st := memoized(ctx, identity, statusTarget, cnpgStatusMemoTTL, func(ctx context.Context) CNPGInstanceStatus {
				return cnpgInstanceStatusFrom(proxyGetWithFallback(ctx, client, statusTarget))
			})
			out.Status = st.CNPGRuntimeSource
			if st.CNPGInstanceStatusFacts != nil {
				out.SlotInventory = st.Slots
				out.SlotInventoryTruncated = st.SlotsTruncated
				if a := st.Archiving; a != nil {
					out.ReadyToArchive, out.LastArchivedAt, out.LastFailedAt, out.LastFailedWal = a.ReadyWalFiles, a.LastArchivedAt, a.LastFailedAt, a.LastFailedWal
					out.ArchivingFailed = cnpgArchivingFailedLast(*a)
				}
			}
		})
		run.do(func(ctx context.Context) {
			m := memoized(ctx, identity, metricsTarget, metricsMemoTTL, func(ctx context.Context) CNPGInstanceMetrics {
				return cnpgInstanceMetricsFrom(proxyGetWithFallback(ctx, client, metricsTarget))
			})
			out.Metrics = m.CNPGRuntimeSource
			if m.CNPGInstanceMetricFacts != nil {
				out.SizeBytes, out.Segments = m.WalBytes, m.WalSegments
				out.Slots = append([]CNPGSlotBytes(nil), m.ReplicationSlotsRetainedBytes...)
				// Without the WAL collector the exporter's queries are failing,
				// so an empty slot list is not "no slots".
				if out.Metrics.State == runtimeStateOK && m.WalBytes == nil {
					out.Metrics.State = cnpgRuntimeStatePartial
					out.Metrics.Reason = "the exporter reported no WAL figures in this sample, so WAL size and slots were not measured"
				}
			}
		})
	}
	run.wait()

	cov := CNPGStorageCoverage{ReadSource: integration.ReadSource{State: storageStateOK, Grant: grant}}
	failed, partial := 0, 0
	for i, p := range pods {
		wal := results[i]
		wal.SlotInventory = withCNPGSlotRetention(&CNPGInstanceStatusFacts{Slots: wal.SlotInventory}, wal.Slots).Slots
		if fenced.Fences(p.Name) {
			explainCNPGFenced(&wal.Status)
			explainCNPGFenced(&wal.Metrics)
		}
		if wal.Status.State == runtimeStateDenied || wal.Metrics.State == runtimeStateDenied {
			cov.State = storageStateDenied
		}
		switch statusOK, metricsOK := wal.Status.State == runtimeStateOK, wal.Metrics.State == runtimeStateOK; {
		case !statusOK && !metricsOK:
			failed++
		case !statusOK || !metricsOK:
			partial++
		}
		wal.Volume = walVolume[p.Name]
		in, ok := byInstance[p.Name]
		if !ok {
			in = &CNPGStorageInstance{Name: p.Name, Role: cnpgStorageInstanceRole(cluster, p.Name), Volumes: []CNPGStorageVolume{}}
			byInstance[p.Name] = in
		}
		in.WAL = &wal
	}
	if cov.State == storageStateOK && failed+partial > 0 {
		cov.State = cnpgStorageStatePartial
		cov.Reason = cnpgWALCoverageReason(failed, partial, len(pods))
	}
	return cov, nil
}

// cnpgInstanceProxyTargets builds the same targets as the live instance reads, so the
// memo serves both from one read.
func cnpgInstanceProxyTargets(p *corev1.Pod, clusterMetricsTLS bool) (status, metrics proxyTarget) {
	status = proxyTarget{
		namespace: p.Namespace, pod: p.Name, podUID: p.UID, port: cnpgStatusPort, path: cnpgStatusPath,
		scheme: schemeFor(containerHasFlag(p, defaultLogContainer, cnpgStatusPortTLSFlag)), limit: cnpgRuntimeStatusCap,
	}
	metrics = proxyTarget{
		namespace: p.Namespace, pod: p.Name, podUID: p.UID, port: cnpgMetricsPort, path: metricsPath,
		scheme: schemeFor(containerHasFlag(p, defaultLogContainer, metricsPortTLSFlag) || clusterMetricsTLS), limit: runtimeMetricsCap,
	}
	return status, metrics
}

func cnpgArchivingFailedLast(a CNPGArchivingStatus) bool {
	if a.LastFailedAt == "" {
		return false
	}
	failed, err := time.Parse(time.RFC3339, a.LastFailedAt)
	if err != nil {
		return false
	}
	if a.LastArchivedAt == "" {
		return true
	}
	archived, err := time.Parse(time.RFC3339, a.LastArchivedAt)
	return err == nil && failed.After(archived)
}

// CNPGFleetDiskResponse is GET /api/cnpg/disk: the fullest measured volume of
// each visible Cluster.
type CNPGFleetDiskResponse struct {
	SampledAt string            `json:"sampledAt"`
	Source    string            `json:"source"`
	Clusters  []CNPGClusterDisk `json:"clusters"`
}

// CNPGClusterDisk State: ok (every claim measured), partial, noSeries,
// noPrometheus, denied (Grant names what is missing), unavailable, error, or
// notRead (the request's namespace bound was reached).
type CNPGClusterDisk struct {
	Namespace string                         `json:"namespace"`
	Name      string                         `json:"name"`
	State     string                         `json:"state"`
	Grant     *auth.Grant                    `json:"grant,omitempty"`
	Reason    string                         `json:"reason,omitempty"`
	Claims    int                            `json:"claims"`
	Measured  int                            `json:"measured"`
	Max       *CNPGDiskUsage                 `json:"max,omitempty"`
	Isolation *prometheuspkg.SeriesIsolation `json:"isolation,omitempty"`
}

type CNPGDiskUsage struct {
	Claim         string  `json:"claim"`
	Instance      string  `json:"instance"`
	Role          string  `json:"role"`
	Tablespace    string  `json:"tablespace,omitempty"`
	UsedBytes     int64   `json:"usedBytes"`
	CapacityBytes int64   `json:"capacityBytes"`
	Ratio         float64 `json:"ratio"`
}

// cnpgNamespaceDisk answers for every Cluster of one namespace with one claim
// list and one usage batch.
func (s *Reader) namespaceDisk(ctx context.Context, cache *k8s.ResourceCache, namespace string, clusters []*unstructured.Unstructured) []CNPGClusterDisk {
	all := func(d CNPGClusterDisk) []CNPGClusterDisk {
		out := make([]CNPGClusterDisk, len(clusters))
		for i, c := range clusters {
			d.Namespace, d.Name = namespace, c.GetName()
			out[i] = d
		}
		return out
	}
	if !s.Access.CanRead(ctx, "", "persistentvolumeclaims", namespace, "list") {
		return all(CNPGClusterDisk{State: storageStateDenied, Grant: cnpgGrantListPVCs.In(namespace).Ref()})
	}
	req, err := labels.NewRequirement(clusterLabel, selection.Exists, nil)
	if err != nil {
		return all(CNPGClusterDisk{State: cnpgStorageStateError, Reason: err.Error()})
	}
	candidates, reason := cnpgCachedClaims(cache, namespace, labels.NewSelector().Add(*req))
	if reason != "" {
		return all(CNPGClusterDisk{State: cnpgStorageStateUnavailable, Reason: reason})
	}
	owned := make([][]*corev1.PersistentVolumeClaim, len(clusters))
	var names []string
	for i, c := range clusters {
		var mine []*corev1.PersistentVolumeClaim
		for _, pvc := range candidates {
			if pvc.Labels[clusterLabel] == c.GetName() {
				mine = append(mine, pvc)
			}
		}
		owned[i], _ = cnpgOwnedClaims(mine, c)
		names = append(names, claimNames(owned[i])...)
	}
	var cov CNPGStorageCoverage
	var batch prometheuspkg.PVCUsageBatch
	if len(names) > 0 {
		var anchors []prom.WorkloadPodIdentity
		for _, c := range clusters {
			if len(anchors) < cnpgHistoryAnchorCap {
				anchors = append(anchors, historyAnchors(cache, c)...)
			}
		}
		cov, batch = s.claimUsage(ctx, namespace, names, anchors)
	}

	out := make([]CNPGClusterDisk, len(clusters))
	for i, c := range clusters {
		d := CNPGClusterDisk{Namespace: namespace, Name: c.GetName(), Claims: len(owned[i]), Isolation: cov.Isolation}
		if d.Claims == 0 {
			d.State, d.Reason = usageStateNotRead, "no claims owned by this cluster"
			out[i] = d
			continue
		}
		for _, pvc := range owned[i] {
			u, ok := batch.Usage[pvc.Name]
			if !ok {
				continue
			}
			d.Measured++
			if d.Max == nil || u.Ratio > d.Max.Ratio {
				d.Max = &CNPGDiskUsage{
					Claim: pvc.Name, Instance: pvc.Labels[instanceNameLabel], Role: pvc.Labels[cnpgPVCRoleLabel],
					Tablespace: pvc.Labels[cnpgTablespaceNameLabel], UsedBytes: u.UsedBytes, CapacityBytes: u.CapacityBytes, Ratio: u.Ratio,
				}
			}
		}
		switch {
		case cov.State == cnpgUsageStateDenied || cov.State == cnpgUsageStateNoPrometheus || cov.State == cnpgUsageStateError ||
			cov.State == cnpgHistoryStateAmbiguous || cov.State == cnpgHistoryStateScopeMismatch:
			d.State, d.Grant, d.Reason = cov.State, cov.Grant, cov.Reason
		case d.Measured == 0:
			d.State, d.Reason = cnpgUsageStateNoSeries, "Prometheus has no kubelet volume stats for this cluster's claims"
		case d.Measured < d.Claims:
			d.State, d.Reason = cnpgStorageStatePartial, fmt.Sprintf("%d of %d claims measured", d.Measured, d.Claims)
		default:
			d.State = cnpgUsageStateOK
		}
		out[i] = d
	}
	return out
}

// cnpgWALCoverageReason counts instances whose WAL facts are missing
// entirely apart from those missing one of the two sources, so the count
// matches what each instance card shows.
func cnpgWALCoverageReason(failed, partial, total int) string {
	var parts []string
	if failed > 0 {
		parts = append(parts, fmt.Sprintf("%d of %d instances could not be read", failed, total))
	}
	if partial > 0 {
		verb := "were"
		if partial == 1 {
			verb = "was"
		}
		parts = append(parts, fmt.Sprintf("%d of %d %s read only in part", partial, total, verb))
	}
	return strings.Join(parts, "; ")
}

func (s *Reader) FleetDisk(ctx context.Context, cache *k8s.ResourceCache, namespaces []string) CNPGFleetDiskResponse {
	resp := CNPGFleetDiskResponse{SampledAt: time.Now().UTC().Format(time.RFC3339), Source: usageSource, Clusters: []CNPGClusterDisk{}}

	var clusterKind integration.WorkspaceKind
	for _, k := range workspaceKinds {
		if k.Key == workspaceClusterKey {
			clusterKind = k
		}
	}
	acc, clusters := s.workspaceReadKind(ctx, cache, clusterKind, namespaces)
	if acc.State != integration.KindCoverageFull && acc.State != integration.KindCoveragePartial {
		return resp
	}
	byNamespace := map[string][]*unstructured.Unstructured{}
	for _, c := range clusters {
		byNamespace[c.GetNamespace()] = append(byNamespace[c.GetNamespace()], c)
	}
	nsList := make([]string, 0, len(byNamespace))
	for ns := range byNamespace {
		nsList = append(nsList, ns)
	}
	sort.Strings(nsList)

	read := min(len(nsList), fleetDiskMaxNamespaces)
	results := integration.FanOut(ctx, read, fleetDiskConcurrency, func(i int) []CNPGClusterDisk {
		return s.namespaceDisk(ctx, cache, nsList[i], byNamespace[nsList[i]])
	})
	for _, rs := range results {
		resp.Clusters = append(resp.Clusters, rs...)
	}
	for _, ns := range nsList[read:] {
		for _, c := range byNamespace[ns] {
			resp.Clusters = append(resp.Clusters, CNPGClusterDisk{Namespace: ns, Name: c.GetName(), State: usageStateNotRead,
				Reason: fmt.Sprintf("disk use is read for at most %d namespaces at a time; narrow the namespace filter", fleetDiskMaxNamespaces)})
		}
	}
	sort.Slice(resp.Clusters, func(i, j int) bool {
		if resp.Clusters[i].Namespace != resp.Clusters[j].Namespace {
			return resp.Clusters[i].Namespace < resp.Clusters[j].Namespace
		}
		return resp.Clusters[i].Name < resp.Clusters[j].Name
	})
	return resp

}
