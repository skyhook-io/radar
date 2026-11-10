package cnpg

import (
	"testing"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	listersbatchv1 "k8s.io/client-go/listers/batch/v1"
	"k8s.io/client-go/tools/cache"
)

func cnpgJob(ns, name, uid string, owner metav1.OwnerReference) *batchv1.Job {
	return &batchv1.Job{ObjectMeta: metav1.ObjectMeta{
		Namespace: ns, Name: name, UID: types.UID(uid),
		Labels:          map[string]string{clusterLabel: owner.Name, jobRoleLabel: "initdb"},
		OwnerReferences: []metav1.OwnerReference{owner},
	}}
}

func clusterRef(name, uid string) metav1.OwnerReference {
	return metav1.OwnerReference{APIVersion: "postgresql.cnpg.io/v1", Kind: "Cluster", Name: name, UID: types.UID(uid), Controller: boolPtr(true)}
}

func jobRef(name, uid string) metav1.OwnerReference {
	return metav1.OwnerReference{APIVersion: "batch/v1", Kind: "Job", Name: name, UID: types.UID(uid), Controller: boolPtr(true)}
}

// An initdb Pod the scheduler could not place, as CloudNativePG labels it.
func cnpgJobPod(ns, name, cluster string, owner metav1.OwnerReference) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: ns, Name: name,
			Labels:          map[string]string{clusterLabel: cluster, jobRoleLabel: "initdb", "cnpg.io/instanceName": cluster + "-1"},
			OwnerReferences: []metav1.OwnerReference{owner},
		},
		Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "initdb", Image: "pg:17"}}},
		Status: corev1.PodStatus{
			Phase: corev1.PodPending,
			Conditions: []corev1.PodCondition{{
				Type: corev1.PodScheduled, Status: corev1.ConditionFalse, Reason: corev1.PodReasonUnschedulable,
				Message: "0/2 nodes are available: 2 Too many pods.",
			}},
		},
	}
}

func TestIsCNPGClusterJobPod(t *testing.T) {
	indexer := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{cache.NamespaceIndex: cache.MetaNamespaceIndexFunc})
	for _, j := range []*batchv1.Job{
		cnpgJob("pg", "x-1-initdb", "job-uid", clusterRef("x", "x-uid")),
		cnpgJob("pg", "y-1-initdb", "y-job-uid", clusterRef("y", "y-uid")),
		cnpgJob("pg", "x-2-join", "join-uid", metav1.OwnerReference{APIVersion: "postgresql.cnpg.io/v1", Kind: "Cluster", Name: "x", UID: "x-uid"}),
	} {
		if err := indexer.Add(j); err != nil {
			t.Fatal(err)
		}
	}
	jobs := listersbatchv1.NewJobLister(indexer)
	uids := map[string]types.UID{"pg/x": "x-uid", "pg/y": "y-uid"}
	unlabelled := cnpgJobPod("pg", "x-1-initdb-a", "x", jobRef("x-1-initdb", "job-uid"))
	delete(unlabelled.Labels, jobRoleLabel)
	for _, c := range []struct {
		name string
		pod  *corev1.Pod
		want bool
	}{
		{"Pod of a Job the Cluster controls", cnpgJobPod("pg", "x-1-initdb-a", "x", jobRef("x-1-initdb", "job-uid")), true},
		{"left by an earlier Job of the same name", cnpgJobPod("pg", "x-1-initdb-a", "x", jobRef("x-1-initdb", "old-job-uid")), false},
		{"Job controlled by another Cluster", cnpgJobPod("pg", "y-1-initdb-a", "x", jobRef("y-1-initdb", "y-job-uid")), false},
		{"Job the Cluster owns but does not control", cnpgJobPod("pg", "x-2-join-a", "x", jobRef("x-2-join", "join-uid")), false},
		{"controller is not a batch Job", cnpgJobPod("pg", "x-1-initdb-a", "x", metav1.OwnerReference{APIVersion: "example.com/v1", Kind: "Job", Name: "x-1-initdb", UID: "job-uid", Controller: boolPtr(true)}), false},
		{"Job not cached", cnpgJobPod("pg", "x-9-initdb-a", "x", jobRef("x-9-initdb", "job-uid")), false},
		{"no job role label", unlabelled, false},
		{"Cluster not visible", cnpgJobPod("other", "x-1-initdb-a", "x", jobRef("x-1-initdb", "job-uid")), false},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := isCNPGClusterJobPod(c.pod, uids, jobs); got != c.want {
				t.Errorf("isCNPGClusterJobPod = %v, want %v", got, c.want)
			}
		})
	}
}
