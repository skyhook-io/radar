package server

import (
	"context"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	listersbatchv1 "k8s.io/client-go/listers/batch/v1"
	"k8s.io/client-go/tools/cache"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/k8s"
)

func cnpgJob(ns, name, uid string, owner metav1.OwnerReference) *batchv1.Job {
	return &batchv1.Job{ObjectMeta: metav1.ObjectMeta{
		Namespace: ns, Name: name, UID: types.UID(uid),
		Labels:          map[string]string{cnpgClusterLabel: owner.Name, cnpgJobRoleLabel: "initdb"},
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
			Labels:          map[string]string{cnpgClusterLabel: cluster, cnpgJobRoleLabel: "initdb", "cnpg.io/instanceName": cluster + "-1"},
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
	delete(unlabelled.Labels, cnpgJobRoleLabel)
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

func seedCNPGJobs(t *testing.T, jobs ...*batchv1.Job) {
	t.Helper()
	for _, j := range jobs {
		if _, err := testFakeClient.BatchV1().Jobs(j.Namespace).Create(context.Background(), j, metav1.CreateOptions{}); err != nil {
			t.Fatalf("create job %s: %v", j.Name, err)
		}
		t.Cleanup(func() {
			_ = testFakeClient.BatchV1().Jobs(j.Namespace).Delete(context.Background(), j.Name, metav1.DeleteOptions{})
		})
	}
	lister := k8s.GetResourceCache().Jobs()
	deadline := time.Now().Add(5 * time.Second)
	for _, j := range jobs {
		for {
			if _, err := lister.Jobs(j.Namespace).Get(j.Name); err == nil {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("job %s did not reach the cache", j.Name)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
}

// The scheduler's verdict on a Cluster's own Job Pod (here initdb) reaches the
// Cluster only for a caller who may read both the Pods and the Jobs; a Pod that
// names an earlier Job of the same name is never adopted.
func TestCNPGWorkspace_JobPodsFollowJobAccess(t *testing.T) {
	seedCNPGWorkspace(t, cnpgWorkspaceTestKinds,
		withUID(cnpgObj("postgresql.cnpg.io/v1", "Cluster", "pgjob", "pg-new", map[string]any{"instances": int64(1)}, nil), "new-uid"),
	)
	seedCNPGJobs(t, cnpgJob("pgjob", "pg-new-1-initdb", "job-uid", clusterRef("pg-new", "new-uid")))
	stuck := cnpgJobPod("pgjob", "pg-new-1-initdb-abcde", "pg-new", jobRef("pg-new-1-initdb", "job-uid"))
	stale := cnpgJobPod("pgjob", "pg-new-1-initdb-old", "pg-new", jobRef("pg-new-1-initdb", "old-job-uid"))
	seedCNPGPods(t, stuck, stale)

	env := newAuthTestServer(t)
	for _, u := range []struct {
		name string
		jobs bool
	}{{"with-jobs", true}, {"pods-only", false}} {
		perms := &auth.UserPermissions{AllowedNamespaces: []string{"pgjob"}}
		allow(perms, cnpgGroup, "clusters", "", true)
		allow(perms, "", "pods", "", true)
		allow(perms, "", "pods", "pgjob", true)
		allow(perms, "batch", "jobs", "", u.jobs)
		allow(perms, "batch", "jobs", "pgjob", u.jobs)
		env.srv.permCache.Set(u.name, nil, perms)
	}

	withJobs := decodeWorkspace(t, env.authGet(t, "/api/cnpg/workspace", "with-jobs", ""))
	if names := objectNames(withJobs.JobPods); len(names) != 1 || names[0] != stuck.Name {
		t.Errorf("with Job access: jobPods = %v, want only %s", names, stuck.Name)
	}
	if withJobs.JobCoverage == nil || withJobs.JobCoverage.State != kindCoverageFull {
		t.Errorf("with Job access: jobCoverage = %+v, want full", withJobs.JobCoverage)
	}
	if containsName(withJobs.Objects["pods"], stuck.Name) {
		t.Error("a Job Pod was returned as an instance Pod")
	}
	found := false
	for _, iss := range withJobs.Issues {
		if iss.Kind == "Pod" && iss.Name == stale.Name {
			t.Errorf("the stale Pod's issue reached the Cluster: %+v", iss)
		}
		found = found || (iss.Kind == "Pod" && iss.Name == stuck.Name)
	}
	if !found {
		t.Errorf("with Job access: no issue for the unschedulable initdb Pod, got %+v", withJobs.Issues)
	}

	podsOnly := decodeWorkspace(t, env.authGet(t, "/api/cnpg/workspace", "pods-only", ""))
	if len(podsOnly.JobPods) != 0 {
		t.Errorf("without Job access: jobPods = %v, want none", objectNames(podsOnly.JobPods))
	}
	if podsOnly.JobCoverage == nil || podsOnly.JobCoverage.State != kindCoverageDenied {
		t.Errorf("without Job access: jobCoverage = %+v, want denied", podsOnly.JobCoverage)
	}
	for _, iss := range podsOnly.Issues {
		if iss.Kind == "Pod" && (iss.Name == stuck.Name || iss.Name == stale.Name) {
			t.Errorf("without Job access: a Job Pod's issue reached the Cluster: %+v", iss)
		}
	}
}
