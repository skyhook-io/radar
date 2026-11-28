// Package rook reads Rook's Ceph custom resources (ceph.rook.io/v1): the
// CephCluster's reconcile state and the Ceph health Rook copies from
// `ceph status`, and a CephBlockPool's reconcile state. Fields are those of
// the v1 types as of Rook v1.21; a field a version doesn't write reads as
// unknown, never as healthy or zero.
package rook

import (
	"sort"
	"strconv"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

const Group = "ceph.rook.io"

// DefaultHealthCheckInterval is how often Rook runs `ceph status` and copies
// the result to status.ceph, unless spec.healthCheck.daemonHealth.status.interval
// (or the operator's ROOK_CEPH_STATUS_CHECK_INTERVAL, which Radar can't see)
// overrides it.
const DefaultHealthCheckInterval = 60 * time.Second

// minStaleAfter keeps a few missed checks, an operator restart or a slow API
// server from reading as stale.
const minStaleAfter = 10 * time.Minute

// staleIntervals is how many health-check intervals may pass without a new
// report before the last one is stale.
const staleIntervals = 3

// HealthState says what the reported Ceph health establishes about now.
type HealthState string

const (
	// HealthFresh: Rook checked within StaleAfter.
	HealthFresh HealthState = "fresh"
	// HealthStale: a report exists, but Rook has not refreshed it within
	// StaleAfter, or it carries no check time. It says what Ceph reported
	// then, not now. When `ceph status` itself fails Rook writes a fresh
	// HEALTH_ERR instead, so a stale report points at the operator.
	HealthStale HealthState = "stale"
	// HealthDisabled: spec.healthCheck.daemonHealth.status.disabled stops
	// Rook's status checks, so any status.ceph left behind is not current.
	HealthDisabled HealthState = "disabled"
	// HealthMissing: no status.ceph.health yet, for example while the cluster
	// is created.
	HealthMissing HealthState = "missing"
)

// HealthCheck is one Ceph health check Rook copied to status.ceph.details,
// keyed by its Ceph code (MON_DOWN, OSD_NEARFULL, ...).
type HealthCheck struct {
	Code     string
	Severity string
	Message  string
}

// Health is the Ceph health in a CephCluster's status.ceph.
type Health struct {
	// Status is the health Ceph reported (HEALTH_OK, HEALTH_WARN, HEALTH_ERR),
	// empty when none was reported.
	Status      string
	State       HealthState
	LastChecked time.Time
	// LastChanged is when Rook saw Status change from Previous. Rook does not
	// record it for the first report.
	LastChanged time.Time
	Previous    string
	// Checks are ordered HEALTH_ERR first, then by code.
	Checks []HealthCheck
}

// Current reports whether Status describes Ceph as of the last check
// interval: reported, fresh and not disabled.
func (h Health) Current() bool { return h.State == HealthFresh }

// StaleAfter is how old status.ceph.lastChecked may be before the report is
// stale: three health-check intervals, and never less than ten minutes.
func StaleAfter(cluster *unstructured.Unstructured) time.Duration {
	interval := DefaultHealthCheckInterval
	if raw, _, _ := unstructured.NestedString(cluster.Object, "spec", "healthCheck", "daemonHealth", "status", "interval"); raw != "" {
		if d, err := time.ParseDuration(raw); err == nil && d > 0 {
			interval = d
		}
	}
	return max(minStaleAfter, staleIntervals*interval)
}

// ClusterHealth reads the Ceph health Rook last copied to a CephCluster's
// status.ceph, and whether that report is still current at now.
func ClusterHealth(cluster *unstructured.Unstructured, now time.Time) Health {
	var h Health
	if cluster == nil {
		h.State = HealthMissing
		return h
	}
	h.Status, _, _ = unstructured.NestedString(cluster.Object, "status", "ceph", "health")
	h.Previous, _, _ = unstructured.NestedString(cluster.Object, "status", "ceph", "previousHealth")
	h.LastChecked = parseTime(cluster, "status", "ceph", "lastChecked")
	h.LastChanged = parseTime(cluster, "status", "ceph", "lastChanged")
	if details, found, _ := unstructured.NestedMap(cluster.Object, "status", "ceph", "details"); found {
		for code, raw := range details {
			check := HealthCheck{Code: code}
			if detail, ok := raw.(map[string]any); ok {
				check.Severity, _ = detail["severity"].(string)
				check.Message, _ = detail["message"].(string)
				check.Message = strings.TrimSpace(check.Message)
			}
			h.Checks = append(h.Checks, check)
		}
		sort.Slice(h.Checks, func(i, j int) bool {
			ei, ej := h.Checks[i].Severity == "HEALTH_ERR", h.Checks[j].Severity == "HEALTH_ERR"
			if ei != ej {
				return ei
			}
			return h.Checks[i].Code < h.Checks[j].Code
		})
	}
	disabled, _, _ := unstructured.NestedBool(cluster.Object, "spec", "healthCheck", "daemonHealth", "status", "disabled")
	switch {
	case disabled:
		h.State = HealthDisabled
	case h.Status == "":
		h.State = HealthMissing
	case h.LastChecked.IsZero() || now.Sub(h.LastChecked) > StaleAfter(cluster):
		h.State = HealthStale
	default:
		h.State = HealthFresh
	}
	return h
}

// ReconcileFailure is Rook's last CephCluster reconcile failing. Rook then
// sets the condition named by status.phase (Progressing) to False, with the
// error as status.message, and keeps retrying. It rewrites that condition on
// every attempt, so its transition time is the latest attempt, not the onset.
type ReconcileFailure struct {
	Message string
}

// ClusterReconcileFailure reports a failed reconcile, or false when the last
// reconcile succeeded or is still running.
func ClusterReconcileFailure(cluster *unstructured.Unstructured) (ReconcileFailure, bool) {
	if cluster == nil {
		return ReconcileFailure{}, false
	}
	phase, _, _ := unstructured.NestedString(cluster.Object, "status", "phase")
	if phase == "" {
		return ReconcileFailure{}, false
	}
	conditions, _, _ := unstructured.NestedSlice(cluster.Object, "status", "conditions")
	for _, raw := range conditions {
		c, ok := raw.(map[string]any)
		if !ok || c["type"] != phase {
			continue
		}
		if c["status"] != "False" {
			return ReconcileFailure{}, false
		}
		message, _, _ := unstructured.NestedString(cluster.Object, "status", "message")
		if message == "" {
			message, _ = c["message"].(string)
		}
		return ReconcileFailure{Message: strings.TrimSpace(message)}, true
	}
	return ReconcileFailure{}, false
}

// ClusterStatus is a CephCluster's display status. Current Ceph health leads,
// except that a failed reconcile outranks HEALTH_WARN and HEALTH_OK: Ceph can
// be healthy while Rook can't apply the spec. A stale, disabled or missing
// report never reads as healthy. Mirrors getCephClusterStatus in
// packages/k8s-ui.
func ClusterStatus(cluster *unstructured.Unstructured, now time.Time) string {
	if cluster.GetDeletionTimestamp() != nil {
		return "Terminating"
	}
	h := ClusterHealth(cluster, now)
	if h.Current() && h.Status == "HEALTH_ERR" {
		return "HEALTH_ERR"
	}
	if _, failed := ClusterReconcileFailure(cluster); failed {
		return "Reconcile failed"
	}
	switch h.State {
	case HealthFresh:
		return h.Status
	case HealthDisabled:
		return "Health checks disabled"
	case HealthStale:
		return "Health stale"
	}
	if phase, _, _ := unstructured.NestedString(cluster.Object, "status", "phase"); phase != "" {
		return phase
	}
	return "Not reported"
}

// BlockPoolDeletionBlocked returns the messages of a CephBlockPool's True
// DeletionIsBlocked and PoolDeletionIsBlocked conditions: Rook won't delete
// a pool that has dependents or still holds images.
func BlockPoolDeletionBlocked(pool *unstructured.Unstructured) []string {
	var out []string
	conditions, _, _ := unstructured.NestedSlice(pool.Object, "status", "conditions")
	for _, raw := range conditions {
		c, ok := raw.(map[string]any)
		if !ok || c["status"] != "True" {
			continue
		}
		if t, _ := c["type"].(string); strings.HasSuffix(t, "DeletionIsBlocked") {
			message, _ := c["message"].(string)
			out = append(out, strings.TrimSpace(message))
		}
	}
	return out
}

// BlockPoolStatus is a CephBlockPool's display status: Rook's phase (Ready,
// Progressing, Failure), which says whether Rook configured the pool, not
// whether its placement groups are healthy. Mirrors getCephBlockPoolStatus in
// packages/k8s-ui.
func BlockPoolStatus(pool *unstructured.Unstructured) string {
	if pool.GetDeletionTimestamp() != nil {
		if len(BlockPoolDeletionBlocked(pool)) > 0 {
			return "Deletion blocked"
		}
		return "Terminating"
	}
	if phase, _, _ := unstructured.NestedString(pool.Object, "status", "phase"); phase != "" {
		return phase
	}
	return "Not reported"
}

// BlockPoolProtection describes a pool's configured data protection:
// "Replicated, size 3" or "Erasure coded, 2+1". It is configuration, not
// proof the data currently has that many copies.
func BlockPoolProtection(pool *unstructured.Unstructured) string {
	data, _, _ := unstructured.NestedInt64(pool.Object, "spec", "erasureCoded", "dataChunks")
	coding, _, _ := unstructured.NestedInt64(pool.Object, "spec", "erasureCoded", "codingChunks")
	if data > 0 || coding > 0 {
		return "Erasure coded, " + strconv.FormatInt(data, 10) + "+" + strconv.FormatInt(coding, 10)
	}
	if size, found, _ := unstructured.NestedInt64(pool.Object, "spec", "replicated", "size"); found && size > 0 {
		return "Replicated, size " + strconv.FormatInt(size, 10)
	}
	return ""
}

func parseTime(obj *unstructured.Unstructured, fields ...string) time.Time {
	raw, _, _ := unstructured.NestedString(obj.Object, fields...)
	if raw == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return time.Time{}
	}
	return t
}
