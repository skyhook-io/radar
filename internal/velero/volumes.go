package velero

import (
	"context"
	"sort"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/skyhook-io/radar/internal/integration"
)

// VolumeOperationKinds are read only by the per-run operations route.
func VolumeOperationKinds(restore bool) []integration.WorkspaceKind {
	if restore {
		return []integration.WorkspaceKind{
			{Key: "podVolumeRestores", Group: "velero.io", Kind: "PodVolumeRestore", Resource: "podvolumerestores"},
			{Key: "dataDownloads", Group: "velero.io", Kind: "DataDownload", Resource: "datadownloads"},
		}
	}
	return []integration.WorkspaceKind{
		{Key: "podVolumeBackups", Group: "velero.io", Kind: "PodVolumeBackup", Resource: "podvolumebackups"},
		{Key: "dataUploads", Group: "velero.io", Kind: "DataUpload", Resource: "datauploads"},
	}
}

type VolumeOperation struct {
	Kind         string `json:"kind"`
	Name         string `json:"name"`
	Pod          string `json:"pod,omitempty"`
	PodNamespace string `json:"podNamespace,omitempty"`
	Volume       string `json:"volume,omitempty"`
	PVC          string `json:"pvc,omitempty"`
	PVCNamespace string `json:"pvcNamespace,omitempty"`
	Node         string `json:"node,omitempty"`
	Phase        string `json:"phase,omitempty"`
	BytesDone    *int64 `json:"bytesDone,omitempty"`
	BytesTotal   *int64 `json:"bytesTotal,omitempty"`
	Message      string `json:"message,omitempty"`
	Started      string `json:"started,omitempty"`
	Completed    string `json:"completed,omitempty"`
	LongRunning  bool   `json:"longRunning"`
	// NodeAgent: running, notRunning, or unknown (including an unassigned node).
	NodeAgent string `json:"nodeAgent,omitempty"`
}

type VolumeOperationsResponse struct {
	ObservedAt time.Time                           `json:"observedAt"`
	RunPhase   string                              `json:"runPhase"`
	Coverage   map[string]integration.KindCoverage `json:"coverage"`
	Operations []VolumeOperation                   `json:"operations"`
}

func (r *Reader) VolumeOperations(ctx context.Context, run *unstructured.Unstructured, restore bool) VolumeOperationsResponse {
	out := VolumeOperationsResponse{ObservedAt: r.now(), Coverage: map[string]integration.KindCoverage{}, Operations: []VolumeOperation{}}
	out.RunPhase, _, _ = unstructured.NestedString(run.Object, "status", "phase")
	label := "velero.io/backup-uid"
	if restore {
		label = "velero.io/restore-uid"
	}
	for _, k := range VolumeOperationKinds(restore) {
		access, items := r.Observations.WorkspaceRead(ctx, k, []string{run.GetNamespace()})
		out.Coverage[k.Key] = access.Coverage()
		for _, item := range integration.KeepGroups(items, groups) {
			if !access.Covers(item.GetNamespace()) || item.GetNamespace() != run.GetNamespace() || run.GetUID() == "" || item.GetLabels()[label] != string(run.GetUID()) {
				continue
			}
			out.Operations = append(out.Operations, volumeOperation(item, k.Kind, out.ObservedAt))
		}
	}
	sort.Slice(out.Operations, func(i, j int) bool {
		a, b := out.Operations[i], out.Operations[j]
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		return a.Name < b.Name
	})
	return out
}

func volumeOperation(u *unstructured.Unstructured, kind string, now time.Time) VolumeOperation {
	str := func(fields ...string) string { v, _, _ := unstructured.NestedString(u.Object, fields...); return v }
	opt := func(fields ...string) *int64 {
		v, found, _ := unstructured.NestedInt64(u.Object, fields...)
		if found {
			return &v
		}
		return nil
	}
	row := VolumeOperation{
		Kind: kind, Name: u.GetName(), Pod: str("spec", "pod", "name"), PodNamespace: str("spec", "pod", "namespace"), Volume: str("spec", "volume"),
		Node: str("status", "node"), Phase: str("status", "phase"), Message: str("status", "message"),
		BytesDone: opt("status", "progress", "bytesDone"), BytesTotal: opt("status", "progress", "totalBytes"),
		Started: str("status", "startTimestamp"), Completed: str("status", "completionTimestamp"),
	}
	// Velero omits zero bytesDone; a recorded total establishes progress exists.
	if row.BytesDone == nil && row.BytesTotal != nil {
		zero := int64(0)
		row.BytesDone = &zero
	}
	switch kind {
	case "PodVolumeBackup":
		row.Node = str("spec", "node")
	case "DataUpload":
		row.PVC, row.PVCNamespace = str("spec", "sourcePVC"), str("spec", "sourceNamespace")
	case "DataDownload":
		row.PVC, row.PVCNamespace = str("spec", "targetVolume", "pvc"), str("spec", "targetVolume", "namespace")
	}
	if (kind == "DataUpload" || kind == "DataDownload") && row.Node == "" {
		row.Node = str("status", "acceptedByNode")
	}
	anchor, err := time.Parse(time.RFC3339, row.Started)
	if err != nil {
		anchor = u.GetCreationTimestamp().Time
	}
	switch row.Phase {
	case "New", "Accepted", "Prepared", "InProgress":
		row.LongRunning = !anchor.IsZero() && now.Sub(anchor) > 10*time.Minute && row.Completed == ""
	}
	if row.LongRunning {
		row.NodeAgent = "unknown"
	}
	return row
}

// AnnotateNodeAgents uses only the installation's caller-readable pods. An
// unread list never proves absence, and a terminating pod is no longer an agent.
func AnnotateNodeAgents(rows []VolumeOperation, pods []*corev1.Pod, readable bool) {
	running := map[string]bool{}
	if readable {
		for _, p := range pods {
			if p.Labels["role"] != "node-agent" || p.Status.Phase != corev1.PodRunning || p.DeletionTimestamp != nil {
				continue
			}
			for _, owner := range p.OwnerReferences {
				if owner.APIVersion == "apps/v1" && owner.Kind == "DaemonSet" && (owner.Name == "node-agent" || owner.Name == "node-agent-windows") && owner.Controller != nil && *owner.Controller {
					running[p.Spec.NodeName] = true
				}
			}
		}
	}
	for i := range rows {
		row := &rows[i]
		if !row.LongRunning {
			continue
		}
		row.NodeAgent = "unknown"
		if readable && row.Node != "" {
			row.NodeAgent = "notRunning"
			if running[row.Node] {
				row.NodeAgent = "running"
			}
		}
	}
}
