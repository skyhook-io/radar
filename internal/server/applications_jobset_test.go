package server

import (
	"context"
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/jobset"
	"github.com/skyhook-io/radar/pkg/packages"
	"github.com/skyhook-io/radar/pkg/subject"
	"github.com/skyhook-io/radar/pkg/topology"
	batchv1 "k8s.io/api/batch/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
)

func TestApplicationsListJobSetInsteadOfExactMembers(t *testing.T) {
	root := testJobSet("ml", "train", "root")
	root.Object["status"] = map[string]any{"replicatedJobsStatus": []any{map[string]any{"name": "workers", "active": int64(2), "failed": int64(1)}}}
	yes := true
	member := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: "train-workers-0", Namespace: "ml", UID: "member", OwnerReferences: []metav1.OwnerReference{{APIVersion: jobset.APIVersion, Kind: jobset.Kind, Name: "train", UID: "root", Controller: &yes}}}}
	stale := member.DeepCopy()
	stale.Name = "stale"
	stale.OwnerReferences[0].UID = "old-root"
	foreign := member.DeepCopy()
	foreign.Name = "foreign"
	foreign.OwnerReferences[0].APIVersion = "other.io/v1"
	stray := member.DeepCopy()
	stray.Name = "label-only"
	stray.OwnerReferences = nil
	stray.Labels = map[string]string{"jobset.sigs.k8s.io/jobset-name": "train"}
	useTestResourceCache(t, fake.NewClientset(member, stale, foreign, stray))
	gvr := schema.GroupVersionResource{Group: jobset.Group, Version: "v1alpha2", Resource: "jobsets"}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{gvr: "JobSetList"}, root)
	if err := k8s.InitTestDynamicResourceCache(dyn, []k8s.APIResource{{Group: gvr.Group, Version: gvr.Version, Name: gvr.Resource, Kind: jobset.Kind, Namespaced: true, IsCRD: true, Verbs: []string{"get", "list", "watch"}}}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k8s.ResetTestDynamicState)
	dc := k8s.GetDynamicResourceCache()
	if err := dc.EnsureWatching(gvr); err != nil || !dc.WaitForSync(gvr, 2*time.Second) {
		t.Fatalf("cache not ready: %v", err)
	}
	cache := k8s.GetResourceCache()
	g := buildAppGraph(cache, []string{"ml"})
	inputs := collectAppWorkloads(context.Background(), cache, []string{"ml"}, g, false)
	if len(inputs) != 5 {
		t.Fatalf("workloads: %+v", inputs)
	}
	rows := groupApplications(inputs)
	foldJobSetApplicationWorkloads(rows)
	row := rowByName(rows, "train")
	if row == nil || row.Key != "ml/JobSet/train" || len(row.Workloads) != 1 {
		t.Fatalf("JobSet app = %+v", row)
	}
	workload := row.Workloads[0]
	if workload.Kind != jobset.Kind || workload.Group != jobset.Group || workload.WorkloadClass != "job" || workload.Health != string(packages.HealthNeutral) || workload.Batch == nil || workload.Batch.ActiveRuns != 0 || workload.Batch.FailedRuns != 0 {
		t.Fatalf("JobSet workload = %+v", workload)
	}
}

func TestJobSetApplicationLifecycle(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status map[string]any
		health packages.Health
		phase  string
	}{
		{"unknown", nil, packages.HealthUnknown, "Unknown"},
		{"pending", map[string]any{"replicatedJobsStatus": []any{}}, packages.HealthNeutral, "Pending"},
		{"completed", map[string]any{"terminalState": "Completed"}, packages.HealthNeutral, "Succeeded"},
		{"failed", map[string]any{"terminalState": "Failed"}, packages.HealthUnhealthy, "Failed"},
		{"suspended", map[string]any{"conditions": []any{map[string]any{"type": "Suspended", "status": "True"}}}, packages.HealthNeutral, "Suspended"},
		{"restarting", map[string]any{"conditions": []any{map[string]any{"type": "RestartingJobSet", "status": "True"}}}, packages.HealthDegraded, "Restarting"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := testJobSet("ml", "train", "root")
			if tc.status != nil {
				root.Object["status"] = tc.status
			}
			batch, health := jobSetApplicationBatch(root)
			if health != tc.health || batch.LatestRunPhase != tc.phase {
				t.Fatalf("%+v %s", batch, health)
			}
		})
	}
}

func TestJobSetApplicationPrimaryImage(t *testing.T) {
	root := testJobSet("ml", "train", "root")
	root.Object["spec"] = map[string]any{"replicatedJobs": []any{map[string]any{"template": map[string]any{"spec": map[string]any{"template": map[string]any{"spec": map[string]any{"containers": []any{map[string]any{"image": "trainer:v2"}}}}}}}}}
	if got := jobSetPrimaryImage(root); got != "trainer:v2" {
		t.Fatal(got)
	}
}

func TestJobSetWorkloadFoldPreservesMemberEvidence(t *testing.T) {
	root := rawInput("JobSet", "ml", "train", "", "neutral")
	member := rawInput("Job", "ml", "worker", "v2", "unhealthy")
	member.rootKey, member.rootKind, member.rootGroup, member.rootIdentity = root.rootKey, root.rootKind, root.rootGroup, root.rootIdentity
	member.wl.jobSetParent = root.rootIdentity
	member.wl.envLabel = "staging"
	member.wl.appAnnotation = "training"
	member.events = []appEvent{{Object: "Job/worker", Reason: "FailedCreate", Message: "quota"}}
	member.rels = &appRelationships{configRefs: map[string]topology.ResourceRef{"config": {Kind: "ConfigMap", Namespace: "ml", Name: "config"}}}
	member.overlay = subject.ResolveOverlay(&metav1.ObjectMeta{Namespace: "ml", Labels: map[string]string{"app.kubernetes.io/instance": "training"}}, false)
	rows := groupApplications([]appWorkloadInput{member, root})
	key := rows[0].Key
	// Identity resolution reads member annotations before the presentation fold.
	resolveAppIdentities(rows, nil, nil, nil, nil)
	identity := rows[0].Identity
	rows[0].SourceStatus = &appSourceStatus{Sync: "Synced", Health: "Degraded"}
	foldJobSetApplicationWorkloads(rows)
	if len(rows) != 1 || rows[0].Key != key || len(rows[0].Workloads) != 1 || rows[0].Workloads[0].Kind != "JobSet" || rows[0].RuntimeHealth != "neutral" || rows[0].Health != "degraded" || len(rows[0].Events) != 1 || rows[0].Relationships.Configs != 1 || identity == nil || identity.Key != "training" || identity.Source != "explicit" || rows[0].Identity != identity {
		t.Fatalf("fold dropped member evidence: %+v", rows)
	}
}
