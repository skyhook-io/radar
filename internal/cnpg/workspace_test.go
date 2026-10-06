package cnpg

import (
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
)

func cnpgObj(apiVersion, kind, ns, name string, spec, status map[string]any) *unstructured.Unstructured {
	meta := map[string]any{"name": name, "creationTimestamp": time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)}
	if ns != "" {
		meta["namespace"] = ns
	}
	obj := map[string]any{"apiVersion": apiVersion, "kind": kind, "metadata": meta}
	if spec != nil {
		obj["spec"] = spec
	}
	if status != nil {
		obj["status"] = status
	}
	return &unstructured.Unstructured{Object: obj}
}

func cnpgBackup(ns, name, cluster, phase string, stoppedAt time.Time) *unstructured.Unstructured {
	status := map[string]any{"phase": phase}
	if !stoppedAt.IsZero() {
		status["stoppedAt"] = stoppedAt.UTC().Format(time.RFC3339)
	}
	return cnpgObj("postgresql.cnpg.io/v1", "Backup", ns, name, map[string]any{"cluster": map[string]any{"name": cluster}}, status)
}

func cnpgPod(ns, name, clusterLabel string, owners ...metav1.OwnerReference) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: ns,
			Labels:          map[string]string{"cnpg.io/cluster": clusterLabel, "cnpg.io/instanceRole": "primary"},
			OwnerReferences: owners,
		},
		Spec: corev1.PodSpec{NodeName: "node-1", Containers: []corev1.Container{{Name: "postgres", Image: "pg:17"}}},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			PodIP: "10.0.0.5",
			ContainerStatuses: []corev1.ContainerStatus{{
				Name: "postgres", Ready: true, RestartCount: 2, Image: "pg:17",
				State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
			}},
		},
	}
}

func TestWindowCNPGBackups(t *testing.T) {
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	day := 24 * time.Hour
	items := []*unstructured.Unstructured{
		cnpgBackup("pg", "running-old", "orders", "running", time.Time{}),
		cnpgBackup("pg", "orders-recent", "orders", "completed", now.Add(-2*day)),
		cnpgBackup("pg", "orders-old", "orders", "completed", now.Add(-20*day)),
		cnpgBackup("pg", "orders-failed-old", "orders", "failed", now.Add(-30*day)),
		cnpgBackup("pg", "orders-failed-new", "orders", "failed", now.Add(-1*day)),
		cnpgBackup("pg", "billing-only-old", "billing", "completed", now.Add(-40*day)),
		cnpgBackup("pg", "billing-older", "billing", "completed", now.Add(-50*day)),
		cnpgBackup("aa", "other-ns", "orders", "completed", now.Add(-60*day)),
	}
	// A running Backup older than the window stays: in flight is never settled.
	items[0].Object["metadata"].(map[string]any)["creationTimestamp"] = now.Add(-90 * day).Format(time.RFC3339)

	kept, omitted := windowCNPGBackups(items, now)
	var names []string
	for _, u := range kept {
		names = append(names, u.GetName())
	}
	want := []string{"other-ns", "orders-failed-new", "orders-recent", "billing-only-old", "running-old"}
	if len(names) != len(want) {
		t.Fatalf("kept = %v, want %v", names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("kept = %v, want %v (namespace, then newest first)", names, want)
		}
	}
	if omitted != 3 {
		t.Errorf("omitted = %d, want 3 (orders-old, orders-failed-old, billing-older)", omitted)
	}
}

func TestIsCNPGInstancePod(t *testing.T) {
	uids := map[string]types.UID{"pg/x": "x-uid"}
	ref := func(uid string, controller bool) metav1.OwnerReference {
		return metav1.OwnerReference{APIVersion: "postgresql.cnpg.io/v1", Kind: "Cluster", Name: "x", UID: types.UID(uid), Controller: boolPtr(controller)}
	}
	for _, c := range []struct {
		name string
		pod  *corev1.Pod
		uids map[string]types.UID
		want bool
	}{
		{"controller ref to the visible Cluster", cnpgPod("pg", "x-1", "x", ref("x-uid", true)), uids, true},
		{"left behind by a deleted Cluster of the same name", cnpgPod("pg", "x-1", "x", ref("old-uid", true)), uids, false},
		{"non-controller owner", cnpgPod("pg", "x-1", "x", ref("x-uid", false)), uids, false},
		{"Cluster not visible", cnpgPod("pg", "x-1", "x", ref("x-uid", true)), map[string]types.UID{}, false},
		{"Cluster of that name in another namespace", cnpgPod("other", "x-1", "x", ref("x-uid", true)), uids, false},
		{"no cluster label", func() *corev1.Pod { p := cnpgPod("pg", "x-1", "x", ref("x-uid", true)); p.Labels = nil; return p }(), uids, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := isCNPGInstancePod(c.pod, c.uids); got != c.want {
				t.Errorf("isCNPGInstancePod = %v, want %v", got, c.want)
			}
		})
	}
}
