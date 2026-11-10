package cnpg

import (
	"context"
	"encoding/json"
	"net/http"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"

	"github.com/skyhook-io/radar/internal/integration"
	declarations "github.com/skyhook-io/radar/pkg/cnpg"
)

type ScheduleMethodFacts struct {
	ClusterUID     string `json:"clusterUID"`
	ClusterConfig  string `json:"clusterConfig"`
	ScheduleConfig string `json:"scheduleConfig"`
}

type ScheduleMethodPreview struct {
	Context        string              `json:"context"`
	UID            string              `json:"uid"`
	Cluster        string              `json:"cluster"`
	PreviousMethod string              `json:"previousMethod"`
	Facts          ScheduleMethodFacts `json:"facts"`
	Unchanged      bool                `json:"unchanged"`
}

func prepareScheduleMethod(ctx context.Context, c ActionClients, namespace, name string) (*unstructured.Unstructured, map[string]any, ScheduleMethodPreview, error) {
	var out ScheduleMethodPreview
	schedule, err := c.Dynamic.Resource(ScheduleGVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, nil, out, err
	}
	if !schedule.GetDeletionTimestamp().IsZero() {
		return nil, nil, out, integration.BlockedAction("The schedule is being deleted")
	}
	spec, _, _ := unstructured.NestedMap(schedule.Object, "spec")
	clusterName, _, _ := unstructured.NestedString(schedule.Object, "spec", "cluster", "name")
	cluster, err := c.Dynamic.Resource(ClusterGVR).Namespace(namespace).Get(ctx, clusterName, metav1.GetOptions{})
	if err != nil {
		return nil, nil, out, err
	}
	if !cluster.GetDeletionTimestamp().IsZero() {
		return nil, nil, out, integration.BlockedAction("The Cluster is being deleted")
	}
	plugin, ok := declarations.ParseBackupDeclaration(cluster).BarmanPlugin()
	if !ok || !plugin.WALArchiver || plugin.ObjectStore == "" {
		return nil, nil, out, integration.BlockedAction("The Cluster needs an enabled Barman WAL archiver and ObjectStore before matching this schedule")
	}
	store, _, err := readStore(ctx, c.Dynamic, namespace, plugin.ObjectStore)
	if err != nil {
		return nil, nil, out, err
	}
	if !store.GetDeletionTimestamp().IsZero() {
		return nil, nil, out, integration.BlockedAction("The ObjectStore is being deleted")
	}
	method, _ := spec["method"].(string)
	if method == "volumeSnapshot" {
		return nil, nil, out, integration.BlockedAction("This is a snapshot schedule. Review a backup-method migration in its YAML")
	}
	configuration, _ := spec["pluginConfiguration"].(map[string]any)
	if configuration == nil {
		configuration = map[string]any{}
	}
	if declared, _ := configuration["name"].(string); declared != "" && declared != declarations.BarmanPluginName {
		return nil, nil, out, integration.BlockedAction("This schedule names another plugin. Review a backup-method migration in its YAML")
	}
	clusterHash, err := configDigest(protectionConfig(cluster))
	if err != nil {
		return nil, nil, out, err
	}
	scheduleHash, err := configDigest(map[string]any{"cluster": spec["cluster"], "method": spec["method"], "pluginConfiguration": spec["pluginConfiguration"]})
	if err != nil {
		return nil, nil, out, err
	}
	unchanged := method == "plugin" && configuration["name"] == declarations.BarmanPluginName
	configuration["name"] = declarations.BarmanPluginName
	patch := map[string]any{"spec": map[string]any{"method": "plugin", "pluginConfiguration": configuration}}
	if method == "" {
		method = "barmanObjectStore"
	}
	out = ScheduleMethodPreview{UID: string(schedule.GetUID()), Cluster: clusterName, PreviousMethod: method, Unchanged: unchanged, Facts: ScheduleMethodFacts{ClusterUID: string(cluster.GetUID()), ClusterConfig: clusterHash, ScheduleConfig: scheduleHash}}
	return schedule, patch, out, nil
}

func (s *Reader) PreviewScheduleMethod(ctx context.Context, c ActionClients, contextName, namespace, name string) (*ScheduleMethodPreview, error) {
	schedule, patch, out, err := prepareScheduleMethod(ctx, c, namespace, name)
	if err != nil {
		return nil, err
	}
	if !out.Unchanged {
		patch["metadata"] = map[string]any{"resourceVersion": schedule.GetResourceVersion()}
		data, err := json.Marshal(patch)
		if err != nil {
			return nil, err
		}
		_, err = c.Dynamic.Resource(ScheduleGVR).Namespace(namespace).Patch(ctx, name, types.MergePatchType, data, metav1.PatchOptions{DryRun: []string{metav1.DryRunAll}})
		if err != nil {
			return nil, err
		}
	}
	out.Context = contextName
	return &out, nil
}

func runRepairScheduleMethod(ctx context.Context, c ActionClients, namespace, name string, req integration.ActionRequest) (*CNPGActionResult, error) {
	if err := integration.DecodeActionParams(req.Params, &struct{}{}); err != nil {
		return nil, err
	}
	var reviewed ScheduleMethodFacts
	if err := integration.DecodeActionParams(req.Facts, &reviewed); err != nil {
		return nil, err
	}
	if reviewed.ClusterUID == "" || reviewed.ClusterConfig == "" || reviewed.ScheduleConfig == "" {
		return nil, integration.RefuseAction(http.StatusBadRequest, "", "Reviewed schedule and Cluster configuration are required")
	}
	schedule, patch, current, err := prepareScheduleMethod(ctx, c, namespace, name)
	if err != nil {
		return nil, err
	}
	if string(schedule.GetUID()) != req.UID || reviewed != current.Facts {
		return nil, integration.ChangedAction(current, "The schedule or Cluster backup configuration changed since review; review the repair again")
	}
	if current.Unchanged {
		return nil, integration.BlockedAction("The schedule already uses the Cluster's Barman plugin")
	}
	if err := integration.MergePatchAtVersion(ctx, c.Dynamic, ScheduleGVR, schedule, patch); err != nil {
		if apierrors.IsConflict(err) {
			return nil, integration.ChangedAction(current, "The schedule changed while the patch was sent; review it again")
		}
		return nil, err
	}
	return &CNPGActionResult{Action: "repairMethod", Message: "Schedule now uses the Cluster's Barman plugin; verify its next backup"}, nil
}
