package velero

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"

	"github.com/skyhook-io/radar/internal/integration"
)

func TestVolumeOperationsKindsAndUID(t *testing.T) {
	for _, restore := range []bool{false, true} {
		run := obj("Backup", "velero", "nightly", nil, nil, map[string]any{"phase": "InProgress"})
		run.SetUID(types.UID("current"))
		label := "velero.io/backup-uid"
		if restore {
			label = "velero.io/restore-uid"
		}
		kinds := VolumeOperationKinds(restore)
		f := fakeRead{objects: map[string][]*unstructured.Unstructured{}, access: map[string]integration.KindAccess{}}
		for _, k := range kinds {
			matching := obj(k.Kind, "velero", "matches", map[string]any{label: "current"}, map[string]any{}, map[string]any{"phase": "InProgress", "progress": map[string]any{"bytesDone": int64(12), "totalBytes": int64(24)}})
			old := matching.DeepCopy()
			old.SetName("old")
			old.SetLabels(map[string]string{label: "old"})
			otherNS := matching.DeepCopy()
			otherNS.SetNamespace("other")
			unlabeled := matching.DeepCopy()
			unlabeled.SetLabels(nil)
			f.objects[k.Resource] = []*unstructured.Unstructured{old, matching, otherNS, unlabeled}
		}
		got := f.reader().VolumeOperations(context.Background(), run, restore)
		if len(got.Operations) != 2 || got.RunPhase != "InProgress" {
			t.Fatalf("matching: %+v", got)
		}
		for _, row := range got.Operations {
			if row.Name != "matches" || *row.BytesDone != 12 || *row.BytesTotal != 24 || !row.LongRunning || row.NodeAgent != "unknown" {
				t.Fatalf("row: %+v", row)
			}
		}
		f.access[kinds[0].Resource] = integration.KindAccess{State: integration.KindCoverageDenied}
		f.access[kinds[1].Resource] = integration.KindAccess{State: integration.KindCoverageNotInstalled}
		got = f.reader().VolumeOperations(context.Background(), run, restore)
		if len(got.Operations) != 0 || got.Coverage[kinds[0].Key].State != "denied" || got.Coverage[kinds[1].Key].State != "notInstalled" {
			t.Fatalf("coverage: %+v", got)
		}
		for _, k := range WorkspaceKinds() {
			for _, operation := range kinds {
				if k.Kind == operation.Kind {
					t.Fatalf("operations added to workspace: %+v", k)
				}
			}
		}
	}
}

func TestVolumeOperationProjection(t *testing.T) {
	for _, kind := range []string{"PodVolumeBackup", "PodVolumeRestore", "DataUpload", "DataDownload"} {
		u := obj(kind, "velero", "op", nil, map[string]any{
			"node": "backup-node", "pod": map[string]any{"namespace": "app", "name": "pod"}, "volume": "data",
			"sourcePVC": "source", "sourceNamespace": "app", "targetVolume": map[string]any{"pvc": "target", "namespace": "recovery"},
		}, map[string]any{"phase": "Prepared", "node": "worker-node", "message": "waiting", "startTimestamp": testNow.Add(-11 * time.Minute).Format(time.RFC3339)})
		got := volumeOperation(u, kind, testNow)
		if !got.LongRunning || got.BytesDone != nil || got.BytesTotal != nil || got.Message != "waiting" {
			t.Fatalf("%s: %+v", kind, got)
		}
		switch kind {
		case "PodVolumeBackup":
			if got.Node != "backup-node" || got.Pod != "pod" || got.Volume != "data" {
				t.Fatal(got)
			}
		case "PodVolumeRestore":
			if got.Node != "worker-node" || got.PodNamespace != "app" {
				t.Fatal(got)
			}
		case "DataUpload":
			if got.PVC != "source" || got.PVCNamespace != "app" {
				t.Fatal(got)
			}
		case "DataDownload":
			if got.PVC != "target" || got.PVCNamespace != "recovery" {
				t.Fatal(got)
			}
		}
	}
	// An object no node agent has picked up has no phase; Velero treats it as New.
	for _, phase := range []string{"", "New", "Accepted", "Prepared", "InProgress", "Completed", "Failed", "Canceled"} {
		status := map[string]any{"phase": phase}
		if phase == "" {
			status = map[string]any{}
		}
		u := obj("DataUpload", "velero", "op", nil, nil, status)
		row := volumeOperation(u, "DataUpload", testNow)
		want := phase == "" || phase == "New" || phase == "Accepted" || phase == "Prepared" || phase == "InProgress"
		if row.LongRunning != want {
			t.Fatalf("%s: %+v", phase, row)
		}
		_ = unstructured.SetNestedField(u.Object, testNow.Add(-time.Minute).Format(time.RFC3339), "status", "startTimestamp")
		if volumeOperation(u, "DataUpload", testNow).LongRunning {
			t.Fatalf("recent %s longRunning", phase)
		}
	}
}

func TestAnnotateNodeAgents(t *testing.T) {
	controller := true
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "velero", Name: "agent", Labels: map[string]string{"role": "node-agent"}, OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "DaemonSet", Name: "node-agent", Controller: &controller}}}, Spec: corev1.PodSpec{NodeName: "linux"}, Status: corev1.PodStatus{Phase: corev1.PodRunning}}
	windows := pod.DeepCopy()
	windows.Spec.NodeName = "windows"
	windows.OwnerReferences[0].Name = "node-agent-windows"
	unrelated := pod.DeepCopy()
	unrelated.Spec.NodeName = "unrelated"
	unrelated.OwnerReferences[0].Name = "other"
	pending := pod.DeepCopy()
	pending.Spec.NodeName = "pending"
	pending.Status.Phase = corev1.PodPending
	terminating := pod.DeepCopy()
	terminating.Spec.NodeName = "terminating"
	stamp := metav1.NewTime(testNow)
	terminating.DeletionTimestamp = &stamp
	for _, readable := range []bool{false, true} {
		rows := []VolumeOperation{{Node: "linux", LongRunning: true}, {Node: "windows", LongRunning: true}, {Node: "unrelated", LongRunning: true}, {Node: "pending", LongRunning: true}, {Node: "terminating", LongRunning: true}, {LongRunning: true}, {Node: "linux"}}
		AnnotateNodeAgents(rows, []*corev1.Pod{pod, windows, unrelated, pending, terminating}, readable)
		for i, row := range rows {
			want := "unknown"
			if i == 6 {
				want = ""
			} else if readable && i < 2 {
				want = "running"
			} else if readable && i < 5 {
				want = "notRunning"
			}
			if row.NodeAgent != want {
				t.Fatalf("readable %v, row %d: %+v, want %s", readable, i, row, want)
			}
		}
	}
}
