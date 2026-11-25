package k8score

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	clienttesting "k8s.io/client-go/testing"
)

// Discovery can still be loading after a switch while the typed caches serve.
// Actions on built-in workloads must not wait for it, including the kinds an
// action reads besides the workload (ReplicaSets, ControllerRevisions, Jobs).
func TestBuiltinWorkloadActionsWithoutDiscovery(t *testing.T) {
	deployment := func() *dynamicfake.FakeDynamicClient {
		return newAppsClient(
			deploymentObject(podTemplateV2(), map[string]any{"deployment.kubernetes.io/revision": "2"}, nil),
			deploymentReplicaSet("web-h1", "1", "h1", podTemplateV1(), nil),
			deploymentReplicaSet("web-h2", "2", "h2", podTemplateV2(), nil),
		)
	}
	statefulSet := func() *dynamicfake.FakeDynamicClient {
		client := newAppsClient(
			controllerWorkload("StatefulSet", "db", podTemplateV2()),
			controllerRevisionObject("db", "StatefulSet", "db-1", 1, podTemplateV1()),
			controllerRevisionObject("db", "StatefulSet", "db-2", 2, podTemplateV2()),
		)
		// The fake client can't apply a strategic merge patch to unstructured objects.
		client.PrependReactor("patch", "statefulsets", func(clienttesting.Action) (bool, runtime.Object, error) {
			return true, nil, nil
		})
		return client
	}
	for name, tc := range map[string]struct {
		client  func() *dynamicfake.FakeDynamicClient
		act     func(*WorkloadManager) error
		patches int
	}{
		"restart": {deployment, func(m *WorkloadManager) error {
			return m.RestartWorkload(context.Background(), "deployments", "prod", "web")
		}, 1},
		"scale": {deployment, func(m *WorkloadManager) error {
			return m.ScaleWorkload(context.Background(), "deployments", "prod", "web", 3)
		}, 1},
		"pause": {deployment, func(m *WorkloadManager) error {
			return m.SetDeploymentPaused(context.Background(), "prod", "web", true)
		}, 1},
		"deployment revisions": {deployment, func(m *WorkloadManager) error {
			revs, err := m.ListWorkloadRevisions(context.Background(), "Deployment", "prod", "web")
			if err == nil && len(revs) != 2 {
				t.Errorf("listed %d revisions, want 2", len(revs))
			}
			return err
		}, 0},
		"deployment rollback": {deployment, func(m *WorkloadManager) error {
			return m.RollbackWorkload(context.Background(), "Deployment", "prod", "web", 1)
		}, 1},
		"statefulset revisions": {statefulSet, func(m *WorkloadManager) error {
			revs, err := m.ListWorkloadRevisions(context.Background(), "StatefulSet", "prod", "db")
			if err == nil && len(revs) != 2 {
				t.Errorf("listed %d revisions, want 2", len(revs))
			}
			return err
		}, 0},
		"statefulset rollback": {statefulSet, func(m *WorkloadManager) error {
			return m.RollbackWorkload(context.Background(), "StatefulSet", "prod", "db", 1)
		}, 1},
	} {
		t.Run(name, func(t *testing.T) {
			client := tc.client()
			if err := tc.act(NewWorkloadManager(client, nil)); err != nil {
				t.Fatalf("%s without discovery: %v", name, err)
			}
			if got := len(patchActions(client)); got != tc.patches {
				t.Fatalf("%s issued %d patches, want %d", name, got, tc.patches)
			}
		})
	}
	if _, _, err := NewWorkloadManager(nil, nil).workloadGVR("rollouts"); err == nil {
		t.Fatal("a custom kind resolved without discovery")
	}
}

func TestTriggerCronJobWithoutDiscovery(t *testing.T) {
	cronJobs := schema.GroupVersionResource{Group: "batch", Version: "v1", Resource: "cronjobs"}
	jobs := schema.GroupVersionResource{Group: "batch", Version: "v1", Resource: "jobs"}
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{cronJobs: "CronJobList", jobs: "JobList"},
		&unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "batch/v1", "kind": "CronJob",
			"metadata": map[string]any{"namespace": "prod", "name": "nightly", "uid": "cj-uid"},
			"spec": map[string]any{"schedule": "0 0 * * *", "jobTemplate": map[string]any{
				"spec": map[string]any{"template": podTemplateV1()},
			}},
		}})
	if _, err := NewWorkloadManager(client, nil).TriggerCronJob(context.Background(), "prod", "nightly"); err != nil {
		t.Fatalf("Run Now without discovery: %v", err)
	}
	list, err := client.Resource(jobs).Namespace("prod").List(context.Background(), metav1.ListOptions{})
	if err != nil || len(list.Items) != 1 {
		t.Fatalf("created %v jobs (err %v), want 1", len(list.Items), err)
	}
}

// A discovery whose first refresh failed holds no kinds. Built-in actions
// keep working from the built-in table instead of calling the kind unknown.
func TestBuiltinWorkloadActionsAfterFailedDiscovery(t *testing.T) {
	failed := &ResourceDiscovery{resourceMap: map[string]APIResource{}, gvrMap: map[string]schema.GroupVersionResource{}, refreshFailed: true}
	client := newAppsClient(deploymentObject(podTemplateV1(), nil, nil))
	if err := NewWorkloadManager(client, failed).ScaleWorkload(context.Background(), "deployments", "prod", "web", 3); err != nil {
		t.Fatalf("scale after a failed discovery: %v", err)
	}
	if _, ok, err := NewWorkloadManager(client, failed).workloadGVR("widgets"); ok || err != nil {
		t.Fatalf("an unknown custom kind resolved (ok=%v, err=%v)", ok, err)
	}
}
