package server

import (
	"context"
	"net/http"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// Node maintenance mode, as `kubectl cnpg maintenance set|unset`: a merge patch
// of spec.nodeMaintenanceWindow {inProgress, reusePVC}. While it is in
// progress the operator stops treating a drained node's instance as failed: it
// waits for the node (reusePVC true) or recreates the instance elsewhere on a
// new volume (false, needs more than one instance).

// CNPGMaintenanceFacts is spec.nodeMaintenanceWindow as the confirmation binds
// it. ReusePVC is the effective value: the CRD defaults it to true.
type CNPGMaintenanceFacts struct {
	Declared   bool `json:"declared"`
	InProgress bool `json:"inProgress"`
	ReusePVC   bool `json:"reusePVC"`
}

type CNPGMaintenanceActions struct {
	SetMaintenance   CNPGActionCapability `json:"setMaintenance"`
	UnsetMaintenance CNPGActionCapability `json:"unsetMaintenance"`
}

func init() {
	for _, a := range []struct {
		action     string
		inProgress bool
	}{{"setMaintenance", true}, {"unsetMaintenance", false}} {
		cnpgClusterActionRunners[a.action] = cnpgClusterRunner{binds: []string{"maintenance"}, run: cnpgRunMaintenance(a.inProgress)}
		cnpgClusterActionGrants[a.action] = cnpgGrantPatchClusters
		cnpgClusterActionsOrdered = append(cnpgClusterActionsOrdered, a.action)
	}
}

func cnpgMaintenanceFactsOf(cluster *unstructured.Unstructured) CNPGMaintenanceFacts {
	win, declared, _ := unstructured.NestedMap(cluster.Object, "spec", "nodeMaintenanceWindow")
	m := CNPGMaintenanceFacts{Declared: declared, ReusePVC: true}
	if !declared {
		return m
	}
	if v, ok := win["inProgress"].(bool); ok {
		m.InProgress = v
	}
	if v, ok := win["reusePVC"].(bool); ok {
		m.ReusePVC = v
	}
	return m
}

func cnpgGuardSetMaintenance(f CNPGClusterFacts) string {
	if r := cnpgGuardCommon(f); r != "" {
		return r
	}
	if f.Maintenance.InProgress {
		return "Maintenance is already in progress"
	}
	return ""
}

func cnpgGuardUnsetMaintenance(f CNPGClusterFacts) string {
	if r := cnpgGuardCommon(f); r != "" {
		return r
	}
	if !f.Maintenance.InProgress {
		return "Maintenance is not in progress"
	}
	return ""
}

// cnpgMaintenanceParams.ReusePVC is required for both directions: `kubectl
// cnpg maintenance unset` writes its --reusePVC flag (default false) too, and
// the dialog sends the value it showed rather than a default nobody reviewed.
type cnpgMaintenanceParams struct {
	ReusePVC *bool `json:"reusePVC"`
}

func cnpgRunMaintenance(inProgress bool) func(context.Context, *cnpgClusterRun) (*CNPGActionResult, error) {
	return func(ctx context.Context, x *cnpgClusterRun) (*CNPGActionResult, error) {
		var p cnpgMaintenanceParams
		if err := decodeCNPGParams(x.params, &p); err != nil {
			return nil, err
		}
		if p.ReusePVC == nil {
			return nil, cnpgRefuse(http.StatusBadRequest, "", "params.reusePVC is required")
		}
		guard, msg := cnpgGuardSetMaintenance, "Maintenance mode set"
		if !inProgress {
			guard, msg = cnpgGuardUnsetMaintenance, "Maintenance mode lifted"
		}
		if r := guard(x.facts); r != "" {
			return nil, cnpgBlocked(r)
		}
		err := cnpgMergePatch(ctx, x.c.dyn, cnpgClusterGVR, x.cluster, map[string]any{
			"spec": map[string]any{"nodeMaintenanceWindow": map[string]any{
				"inProgress": inProgress,
				"reusePVC":   *p.ReusePVC,
			}},
		})
		if err != nil {
			return nil, err
		}
		return &CNPGActionResult{Message: msg}, nil
	}
}
