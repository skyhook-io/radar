package k8score

import (
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
)

func TestRevisionBuildersExposePodIdentity(t *testing.T) {
	deploymentRevision := unstructured.Unstructured{Object: map[string]any{
		"metadata": map[string]any{
			"name":        "api-rev-2",
			"annotations": map[string]any{"deployment.kubernetes.io/revision": "2"},
			"labels":      map[string]any{"pod-template-hash": "hash-2"},
		},
		"spec": map[string]any{"replicas": int64(1)},
	}}
	deploymentRevision.SetOwnerReferences([]metav1.OwnerReference{{UID: types.UID("deployment-uid")}})
	gotDeployment := BuildDeploymentRevisions([]unstructured.Unstructured{deploymentRevision}, "deployment-uid")
	if len(gotDeployment) != 1 || gotDeployment[0].PodHash != "hash-2" {
		t.Fatalf("deployment revision identity = %#v", gotDeployment)
	}

	controllerRevision := unstructured.Unstructured{Object: map[string]any{
		"metadata": map[string]any{
			"name":   "daemon-rev-3",
			"labels": map[string]any{"controller-revision-hash": "hash-3"},
		},
		"revision": int64(3),
	}}
	controllerRevision.SetOwnerReferences([]metav1.OwnerReference{{Kind: "DaemonSet", UID: types.UID("daemon-uid")}})
	gotController := BuildControllerRevisions([]unstructured.Unstructured{controllerRevision}, "daemon-uid")
	if len(gotController) != 1 || gotController[0].PodHash != "hash-3" {
		t.Fatalf("controller revision identity = %#v", gotController)
	}

	statefulRevision := unstructured.Unstructured{Object: map[string]any{
		"metadata": map[string]any{
			"name":   "database-hash-4",
			"labels": map[string]any{"controller.kubernetes.io/hash": "hash-4"},
		},
		"revision": int64(4),
	}}
	statefulRevision.SetOwnerReferences([]metav1.OwnerReference{{Kind: "StatefulSet", UID: types.UID("stateful-uid")}})
	gotStateful := BuildControllerRevisions([]unstructured.Unstructured{statefulRevision}, "stateful-uid")
	if len(gotStateful) != 1 || gotStateful[0].PodHash != "database-hash-4" {
		t.Fatalf("stateful revision identity = %#v", gotStateful)
	}
}

func TestCurrentDeploymentRevisionPodHashes(t *testing.T) {
	replicaSets := []*appsv1.ReplicaSet{
		{
			ObjectMeta: metav1.ObjectMeta{
				Annotations:     map[string]string{"deployment.kubernetes.io/revision": "1"},
				Labels:          map[string]string{appsv1.DefaultDeploymentUniqueLabelKey: "old"},
				OwnerReferences: []metav1.OwnerReference{{Kind: "Deployment", UID: types.UID("api")}},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Annotations:     map[string]string{"deployment.kubernetes.io/revision": "2"},
				Labels:          map[string]string{appsv1.DefaultDeploymentUniqueLabelKey: "new"},
				OwnerReferences: []metav1.OwnerReference{{Kind: "Deployment", UID: types.UID("api")}},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Annotations:     map[string]string{"deployment.kubernetes.io/revision": "not-a-number"},
				Labels:          map[string]string{appsv1.DefaultDeploymentUniqueLabelKey: "ignored"},
				OwnerReferences: []metav1.OwnerReference{{Kind: "Deployment", UID: types.UID("api")}},
			},
		},
	}

	got := CurrentDeploymentRevisionPodHashes(replicaSets)
	if got[types.UID("api")] != "new" {
		t.Fatalf("current Deployment pod hash = %q, want new", got[types.UID("api")])
	}
}

func TestCurrentControllerRevisionPodHashes(t *testing.T) {
	revisions := []unstructured.Unstructured{
		{Object: map[string]any{
			"metadata": map[string]any{"labels": map[string]any{appsv1.ControllerRevisionHashLabelKey: "old"}},
			"revision": int64(1),
		}},
		{Object: map[string]any{
			"metadata": map[string]any{"labels": map[string]any{appsv1.ControllerRevisionHashLabelKey: "new"}},
			"revision": int64(2),
		}},
	}
	for i := range revisions {
		revisions[i].SetOwnerReferences([]metav1.OwnerReference{{Kind: "DaemonSet", UID: types.UID("agent")}})
	}

	got := CurrentControllerRevisionPodHashes(revisions)
	if got[types.UID("agent")] != "new" {
		t.Fatalf("current ControllerRevision pod hash = %q, want new", got[types.UID("agent")])
	}
}
