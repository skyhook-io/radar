package k8score

import (
	"fmt"
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

func TestBuildDeploymentRevisionsReportReadiness(t *testing.T) {
	replicaSet := func(revision string, ready int64) unstructured.Unstructured {
		rs := unstructured.Unstructured{Object: map[string]any{
			"metadata": map[string]any{
				"annotations": map[string]any{"deployment.kubernetes.io/revision": revision},
			},
			"status": map[string]any{"readyReplicas": ready},
		}}
		return rs
	}

	got := BuildDeploymentRevisions([]unstructured.Unstructured{replicaSet("1", 3), replicaSet("2", 0)}, "")
	if len(got) != 2 || got[0].Number != 2 {
		t.Fatalf("revisions = %#v", got)
	}
	if *got[0].ReadyReplicas != 0 || *got[0].Serving {
		t.Fatalf("newest revision ready=%d serving=%v, want 0/false", *got[0].ReadyReplicas, *got[0].Serving)
	}
	if *got[1].ReadyReplicas != 3 || !*got[1].Serving {
		t.Fatalf("older revision ready=%d serving=%v, want 3/true", *got[1].ReadyReplicas, *got[1].Serving)
	}
}

func TestApplyControllerRevisionServing(t *testing.T) {
	revisions := func() []WorkloadRevision {
		return []WorkloadRevision{
			{Number: 2, PodHash: "db-new", IsCurrent: true},
			{Number: 1, PodHash: "db-old"},
		}
	}
	workload := func(generation int64, status map[string]any) *unstructured.Unstructured {
		status["observedGeneration"] = generation
		obj := &unstructured.Unstructured{Object: map[string]any{"status": status}}
		obj.SetGeneration(generation)
		return obj
	}

	t.Run("settled StatefulSet", func(t *testing.T) {
		got := revisions()
		applyControllerRevisionServing(got, workload(4, map[string]any{
			"currentRevision": "db-new", "updateRevision": "db-new", "readyReplicas": int64(3), "replicas": int64(3), "currentReplicas": int64(3),
		}), "statefulsets")
		if !*got[0].Serving || *got[0].ReadyReplicas != 3 || *got[1].Serving || *got[1].ReadyReplicas != 0 {
			t.Fatalf("revisions = %+v / %+v", got[0], got[1])
		}
	})

	t.Run("OnDelete StatefulSet with pods left on a reverted revision stays unknown", func(t *testing.T) {
		got := revisions()
		applyControllerRevisionServing(got, workload(4, map[string]any{
			"currentRevision": "db-new", "updateRevision": "db-new", "readyReplicas": int64(3), "replicas": int64(3), "currentReplicas": int64(1),
		}), "statefulsets")
		if got[0].Serving != nil || got[1].Serving != nil || got[0].ReadyReplicas != nil {
			t.Fatalf("revisions = %+v / %+v", got[0], got[1])
		}
	})

	t.Run("DaemonSet with a misscheduled pod stays unknown", func(t *testing.T) {
		got := revisions()
		applyControllerRevisionServing(got, workload(4, map[string]any{
			"desiredNumberScheduled": int64(3), "updatedNumberScheduled": int64(3), "numberReady": int64(3), "numberMisscheduled": int64(1),
		}), "daemonsets")
		if got[0].Serving != nil || got[1].Serving != nil || got[0].ReadyReplicas != nil {
			t.Fatalf("revisions = %+v / %+v", got[0], got[1])
		}
	})

	t.Run("mid-update StatefulSet stays unknown", func(t *testing.T) {
		got := revisions()
		applyControllerRevisionServing(got, workload(4, map[string]any{
			"currentRevision": "db-old", "updateRevision": "db-new", "readyReplicas": int64(3),
		}), "statefulsets")
		if got[0].Serving != nil || got[1].Serving != nil || got[0].ReadyReplicas != nil {
			t.Fatalf("revisions = %+v / %+v", got[0], got[1])
		}
	})

	t.Run("StatefulSet status behind spec stays unknown", func(t *testing.T) {
		got := revisions()
		stale := workload(4, map[string]any{"currentRevision": "db-new", "updateRevision": "db-new"})
		stale.SetGeneration(5)
		applyControllerRevisionServing(got, stale, "statefulsets")
		if got[0].Serving != nil {
			t.Fatalf("revision = %+v", got[0])
		}
	})

	t.Run("settled DaemonSet", func(t *testing.T) {
		got := revisions()
		applyControllerRevisionServing(got, workload(1, map[string]any{
			"desiredNumberScheduled": int64(5), "updatedNumberScheduled": int64(5), "numberReady": int64(4),
		}), "daemonsets")
		if !*got[0].Serving || *got[0].ReadyReplicas != 4 || *got[1].Serving {
			t.Fatalf("revisions = %+v / %+v", got[0], got[1])
		}
	})

	t.Run("mid-update DaemonSet stays unknown", func(t *testing.T) {
		got := revisions()
		applyControllerRevisionServing(got, workload(1, map[string]any{
			"desiredNumberScheduled": int64(5), "updatedNumberScheduled": int64(2), "numberReady": int64(5),
		}), "daemonsets")
		if got[0].Serving != nil {
			t.Fatalf("revision = %+v", got[0])
		}
	})
}

func TestBuildControllerRevisionsReadsChangeCause(t *testing.T) {
	revision := func(number int64, cause string) unstructured.Unstructured {
		cr := unstructured.Unstructured{Object: map[string]any{
			"revision": number,
			"metadata": map[string]any{
				"name":            fmt.Sprintf("api-%d", number),
				"ownerReferences": []any{map[string]any{"uid": "owner", "kind": "StatefulSet"}},
			},
		}}
		if cause != "" {
			cr.SetAnnotations(map[string]string{"kubernetes.io/change-cause": cause})
		}
		return cr
	}

	revisions := BuildControllerRevisions([]unstructured.Unstructured{revision(1, ""), revision(2, "release 42")}, "owner")
	if len(revisions) != 2 {
		t.Fatalf("got %d revisions, want 2", len(revisions))
	}
	if revisions[0].ChangeCause != "release 42" || revisions[1].ChangeCause != "" {
		t.Fatalf("change causes = %q, %q", revisions[0].ChangeCause, revisions[1].ChangeCause)
	}
}

func TestApplyDeploymentDesiredTemplate(t *testing.T) {
	template := func(image string, hash string) map[string]any {
		labels := map[string]any{"app": "api"}
		if hash != "" {
			labels["pod-template-hash"] = hash
		}
		return map[string]any{
			"metadata": map[string]any{"labels": labels},
			"spec":     map[string]any{"containers": []any{map[string]any{"name": "web", "image": image}}},
		}
	}
	replicaSet := func(number, image, hash string) unstructured.Unstructured {
		rs := unstructured.Unstructured{Object: map[string]any{
			"metadata": map[string]any{
				"name":        "api-" + hash,
				"annotations": map[string]any{"deployment.kubernetes.io/revision": number},
				"labels":      map[string]any{"pod-template-hash": hash},
			},
			"spec": map[string]any{"template": template(image, hash)},
		}}
		rs.SetOwnerReferences([]metav1.OwnerReference{{UID: types.UID("deployment-uid")}})
		return rs
	}
	deployment := func(image string) *unstructured.Unstructured {
		return &unstructured.Unstructured{Object: map[string]any{"spec": map[string]any{"template": template(image, "")}}}
	}
	rsList := []unstructured.Unstructured{replicaSet("1", "nginx:1.26", "aaa"), replicaSet("2", "nginx:1.27", "bbb")}

	tests := []struct {
		name  string
		image string
		want  map[int64]bool
	}{
		{name: "the highest revision matches", image: "nginx:1.27", want: map[int64]bool{1: false, 2: true}},
		{name: "a change with no revision yet", image: "nginx:1.28", want: map[int64]bool{1: false, 2: false}},
		{name: "changed back to an older revision", image: "nginx:1.26", want: map[int64]bool{1: true, 2: false}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			revisions := BuildDeploymentRevisions(rsList, "deployment-uid")
			applyDeploymentDesiredTemplate(revisions, rsList, deployment(tc.image), "deployment-uid")
			for _, revision := range revisions {
				if revision.MatchesDesired == nil || *revision.MatchesDesired != tc.want[revision.Number] {
					t.Fatalf("revision %d MatchesDesired = %v, want %v", revision.Number, revision.MatchesDesired, tc.want[revision.Number])
				}
			}
		})
	}
}
