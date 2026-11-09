package k8score

import (
	"context"
	"fmt"
	"runtime"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
)

var fieldManagerT0 = time.Date(2026, 11, 2, 8, 54, 0, 0, time.UTC)

func mfEntry(manager string, op metav1.ManagedFieldsOperationType, subresource string, at time.Time) metav1.ManagedFieldsEntry {
	entry := metav1.ManagedFieldsEntry{Manager: manager, Operation: op, Subresource: subresource, APIVersion: "apps/v1", FieldsType: "FieldsV1", FieldsV1: &metav1.FieldsV1{Raw: []byte(`{"f:spec":{}}`)}}
	if !at.IsZero() {
		entry.Time = &metav1.Time{Time: at}
	}
	return entry
}

func mfDeployment(rv string, entries ...metav1.ManagedFieldsEntry) *appsv1.Deployment {
	return mfScaledDeployment(rv, 1, entries...)
}

func mfScaledDeployment(rv string, replicas int32, entries ...metav1.ManagedFieldsEntry) *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "ns", UID: types.UID("uid-1"), ResourceVersion: rv, ManagedFields: entries},
		Spec:       appsv1.DeploymentSpec{Replicas: &replicas},
	}
}

func TestFieldManagerIndexAttributesUpdates(t *testing.T) {
	at := func(s int) time.Time { return fieldManagerT0.Add(time.Duration(s) * time.Second) }
	status := func(s int) metav1.ManagedFieldsEntry {
		return mfEntry("kube-controller-manager", metav1.ManagedFieldsOperationUpdate, "status", at(s))
	}
	csa := mfEntry("kubectl-client-side-apply", metav1.ManagedFieldsOperationUpdate, "", at(0))
	scale := mfEntry("kubectl", metav1.ManagedFieldsOperationUpdate, "scale", time.Time{})
	hpaScale := mfEntry("kube-controller-manager", metav1.ManagedFieldsOperationUpdate, "scale", time.Time{})

	lastApplied := mfEntry("kubectl-last-applied", metav1.ManagedFieldsOperationApply, "", time.Time{})

	cases := []struct {
		name                  string
		old, new              []metav1.ManagedFieldsEntry
		oldReplicas, replicas int32
		want                  []FieldManagerWrite
	}{
		{
			name: "new entry (kubectl edit)",
			old:  []metav1.ManagedFieldsEntry{csa, status(2)},
			new:  []metav1.ManagedFieldsEntry{csa, mfEntry("kubectl-edit", metav1.ManagedFieldsOperationUpdate, "", at(9)), status(2)},
			want: []FieldManagerWrite{{Manager: "kubectl-edit", Operation: "Update"}},
		},
		{
			name: "time advanced (kubectl apply again)",
			old:  []metav1.ManagedFieldsEntry{csa, status(2)},
			new:  []metav1.ManagedFieldsEntry{mfEntry("kubectl-client-side-apply", metav1.ManagedFieldsOperationUpdate, "", at(11)), status(2)},
			want: []FieldManagerWrite{{Manager: "kubectl-client-side-apply", Operation: "Update"}},
		},
		{
			name: "status write",
			old:  []metav1.ManagedFieldsEntry{csa, status(2)},
			new:  []metav1.ManagedFieldsEntry{csa, status(13)},
			want: []FieldManagerWrite{{Manager: "kube-controller-manager", Operation: "Update", Subresource: "status"}},
		},
		{
			name: "server-side apply takes over; removed entries did not write",
			old:  []metav1.ManagedFieldsEntry{csa, mfEntry("kubectl-edit", metav1.ManagedFieldsOperationUpdate, "", at(9)), status(2)},
			new:  []metav1.ManagedFieldsEntry{lastApplied, mfEntry("kubectl", metav1.ManagedFieldsOperationApply, "", at(20)), status(2)},
			want: []FieldManagerWrite{{Manager: "kubectl", Operation: "Apply"}},
		},
		{
			name:        "first scale adds an untimed scale entry",
			old:         []metav1.ManagedFieldsEntry{csa, status(2)},
			new:         []metav1.ManagedFieldsEntry{csa, scale, status(2)},
			oldReplicas: 1, replicas: 3,
			want: []FieldManagerWrite{{Manager: "kubectl", Operation: "Update", Subresource: "scale"}},
		},
		{
			name:        "repeat scale changes no entry; the scale entry still owns replicas",
			old:         []metav1.ManagedFieldsEntry{csa, hpaScale, status(2)},
			new:         []metav1.ManagedFieldsEntry{csa, hpaScale, status(2)},
			oldReplicas: 2, replicas: 3,
			want: []FieldManagerWrite{{Manager: "kube-controller-manager", Operation: "Update", Subresource: "scale"}},
		},
		{
			name:        "HPA takes over scale",
			old:         []metav1.ManagedFieldsEntry{csa, scale, status(2)},
			new:         []metav1.ManagedFieldsEntry{csa, hpaScale, status(2)},
			oldReplicas: 3, replicas: 2,
			want: []FieldManagerWrite{{Manager: "kube-controller-manager", Operation: "Update", Subresource: "scale"}},
		},
		{
			name: "status write with a scale entry present does not name the scaler",
			old:  []metav1.ManagedFieldsEntry{csa, hpaScale, status(2)},
			new:  []metav1.ManagedFieldsEntry{csa, hpaScale, status(13)},
			want: []FieldManagerWrite{{Manager: "kube-controller-manager", Operation: "Update", Subresource: "status"}},
		},
		{
			name: "nothing changed and no scale entry",
			old:  []metav1.ManagedFieldsEntry{csa, status(2)},
			new:  []metav1.ManagedFieldsEntry{csa, status(2)},
		},
		{
			name: "two writers between observed versions are both named",
			old:  []metav1.ManagedFieldsEntry{csa, status(2)},
			new:  []metav1.ManagedFieldsEntry{csa, mfEntry("helm", metav1.ManagedFieldsOperationApply, "", at(30)), status(31)},
			want: []FieldManagerWrite{{Manager: "helm", Operation: "Apply"}, {Manager: "kube-controller-manager", Operation: "Update", Subresource: "status"}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			index := newFieldManagerIndex()
			if tc.oldReplicas == 0 {
				tc.oldReplicas, tc.replicas = 1, 1
			}
			oldObj, newObj := mfScaledDeployment("1", tc.oldReplicas, tc.old...), mfScaledDeployment("2", tc.replicas, tc.new...)
			index.capture(oldObj)
			index.capture(newObj)
			got := index.update(oldObj, newObj)
			if fmt.Sprint(got) != fmt.Sprint(tc.want) {
				t.Fatalf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestFieldManagerIndexVersionOrdering(t *testing.T) {
	at := func(s int) time.Time { return fieldManagerT0.Add(time.Duration(s) * time.Second) }
	v1 := mfDeployment("1", mfEntry("helm", metav1.ManagedFieldsOperationApply, "", at(0)))
	v2 := mfDeployment("2", mfEntry("helm", metav1.ManagedFieldsOperationApply, "", at(0)), mfEntry("kubectl-edit", metav1.ManagedFieldsOperationUpdate, "", at(5)))
	v3 := mfDeployment("3", mfEntry("helm", metav1.ManagedFieldsOperationApply, "", at(8)), mfEntry("kubectl-edit", metav1.ManagedFieldsOperationUpdate, "", at(5)))

	index := newFieldManagerIndex()
	// Transforms run ahead of handlers: all three versions are captured
	// before the first update is handled.
	index.capture(v1)
	index.capture(v2)
	index.capture(v3)
	// The watch-list second pass sees stripped managedFields.
	index.capture(mfDeployment("3"))

	if got := index.update(v1, v2); len(got) != 1 || got[0].Manager != "kubectl-edit" {
		t.Fatalf("v1->v2: got %+v", got)
	}
	if got := index.update(v2, v3); len(got) != 1 || got[0].Manager != "helm" {
		t.Fatalf("v2->v3: got %+v", got)
	}
	if n := len(index.objects[fieldManagerKeyOf("uid-1")]); n != 1 {
		t.Fatalf("held versions after handling = %d, want 1", n)
	}
	// An update whose old version was never captured is not attributed.
	if got := index.update(mfDeployment("9"), v3); got != nil {
		t.Fatalf("uncaptured old version: got %+v", got)
	}
	// A recreate missed in a watch gap arrives as an update across UIDs.
	recreated := mfDeployment("4", mfEntry("helm", metav1.ManagedFieldsOperationApply, "", at(9)))
	recreated.UID = "uid-2"
	index.capture(recreated)
	if got := index.update(v3, recreated); got != nil {
		t.Fatalf("update across UIDs: got %+v", got)
	}
	if _, kept := index.objects[fieldManagerKeyOf("uid-1")]; kept {
		t.Fatal("old UID kept after an update across UIDs")
	}
	index.forget("uid-2")
	if len(index.objects) != 0 {
		t.Fatalf("forget left %d objects", len(index.objects))
	}
	// A stopped watch's index captures nothing more.
	index.close()
	index.capture(v1)
	if len(index.objects) != 0 {
		t.Fatalf("closed index captured %d objects", len(index.objects))
	}
}

func TestFieldManagerIndexUnstructured(t *testing.T) {
	obj := func(rv string, entries ...map[string]any) *unstructured.Unstructured {
		list := make([]any, len(entries))
		for i, e := range entries {
			list[i] = e
		}
		return &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "argoproj.io/v1alpha1", "kind": "Rollout",
			"metadata": map[string]any{"name": "r", "namespace": "ns", "uid": "uid-u", "resourceVersion": rv, "managedFields": list},
		}}
	}
	entry := func(manager, op, at string) map[string]any {
		return map[string]any{"manager": manager, "operation": op, "apiVersion": "argoproj.io/v1alpha1", "time": at, "fieldsType": "FieldsV1", "fieldsV1": map[string]any{"f:spec": map[string]any{}}}
	}
	oldObj := obj("1", entry("argocd-controller", "Apply", "2026-11-02T08:54:00Z"))
	newObj := obj("2", entry("argocd-controller", "Apply", "2026-11-02T08:54:00Z"), entry("kubectl-patch", "Update", "2026-11-02T08:55:00Z"))
	index := newFieldManagerIndex()
	index.capture(oldObj)
	index.capture(newObj)
	got := index.update(oldObj, newObj)
	if len(got) != 1 || got[0] != (FieldManagerWrite{Manager: "kubectl-patch", Operation: "Update"}) {
		t.Fatalf("got %+v", got)
	}
}

// BenchmarkFieldManagerIndexMemory reports the index's retained heap per
// object at 100k objects after each was updated, with entry counts drawn from a real cluster (pods
// and ReplicaSets carry about 2 entries, Deployments 2.5, ConfigMaps and
// Services 1, Nodes 7).
func BenchmarkFieldManagerIndexMemory(b *testing.B) {
	const objects = 100_000
	managers := []string{"kube-controller-manager", "kubelet", "kube-scheduler", "kubectl-client-side-apply", "helm", "argocd-controller", "kubectl", "kubectl-edit"}
	entryCounts := []int{2, 2, 2, 2, 2, 2, 3, 1, 1, 7}
	for b.Loop() {
		runtime.GC()
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		index := newFieldManagerIndex()
		version := func(i, rv int) *appsv1.Deployment {
			n := entryCounts[i%len(entryCounts)]
			fields := make([]metav1.ManagedFieldsEntry, n)
			for j := range n {
				fields[j] = mfEntry(managers[(i+j)%len(managers)], metav1.ManagedFieldsOperationUpdate, "", fieldManagerT0.Add(time.Duration(i+rv)*time.Second))
			}
			return &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{UID: types.UID(fmt.Sprintf("%08d-0000-4000-8000-000000000000", i)), ResourceVersion: fmt.Sprint(rv), ManagedFields: fields}}
		}
		for i := range objects {
			index.capture(version(i, 1))
		}
		// Every object is then updated twice, with the second transform
		// running before the first update is handled.
		for i := range objects {
			v1, v2, v3 := version(i, 1), version(i, 2), version(i, 3)
			index.capture(v2)
			index.capture(v3)
			index.update(v1, v2)
			index.update(v2, v3)
		}
		runtime.GC()
		runtime.ReadMemStats(&after)
		b.ReportMetric(float64(after.HeapAlloc-before.HeapAlloc)/objects, "B/object")
		runtime.KeepAlive(index)
	}
}

func TestResourceCacheReportsFieldManagersOnUpdate(t *testing.T) {
	at := func(s int) time.Time { return fieldManagerT0.Add(time.Duration(s) * time.Second) }
	dep := mfDeployment("1", mfEntry("helm", metav1.ManagedFieldsOperationApply, "", at(0)))
	client := fake.NewSimpleClientset(dep)

	updates := make(chan ResourceChange, 4)
	rc, err := NewResourceCache(CacheConfig{
		Client:             client,
		ResourceTypes:      map[string]bool{Deployments: true},
		TrackFieldManagers: true,
		OnChange: func(change ResourceChange, _, _ any) {
			if change.Operation == OpUpdate {
				updates <- change
			}
		},
	})
	if err != nil {
		t.Fatalf("NewResourceCache: %v", err)
	}
	defer rc.Stop()

	edited := mfDeployment("2", mfEntry("helm", metav1.ManagedFieldsOperationApply, "", at(0)), mfEntry("kubectl-edit", metav1.ManagedFieldsOperationUpdate, "", at(7)))
	if _, err := client.AppsV1().Deployments("ns").Update(context.Background(), edited, metav1.UpdateOptions{}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	select {
	case change := <-updates:
		want := []FieldManagerWrite{{Manager: "kubectl-edit", Operation: "Update"}}
		if fmt.Sprint(change.FieldManagers) != fmt.Sprint(want) {
			t.Fatalf("FieldManagers = %+v, want %+v", change.FieldManagers, want)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no update observed")
	}
	cached, err := rc.Deployments().Deployments("ns").Get("web")
	if err != nil {
		t.Fatalf("cached Get: %v", err)
	}
	if len(cached.ManagedFields) != 0 {
		t.Fatalf("cached object kept %d managedFields entries", len(cached.ManagedFields))
	}
}
