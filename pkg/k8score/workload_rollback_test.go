package k8score

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/strategicpatch"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	clienttesting "k8s.io/client-go/testing"

	"github.com/skyhook-io/radar/pkg/rollouts"
)

var (
	deploymentsGVR         = schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}
	replicaSetsGVR         = schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "replicasets"}
	statefulSetsGVR        = schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "statefulsets"}
	daemonSetsGVR          = schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "daemonsets"}
	controllerRevisionsGVR = schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "controllerrevisions"}
)

func appsDiscovery() *ResourceDiscovery {
	rd := &ResourceDiscovery{resourceMap: map[string]APIResource{}, gvrMap: map[string]schema.GroupVersionResource{}}
	for kind, gvr := range map[string]schema.GroupVersionResource{
		"Deployment": deploymentsGVR, "ReplicaSet": replicaSetsGVR, "StatefulSet": statefulSetsGVR,
		"DaemonSet": daemonSetsGVR, "ControllerRevision": controllerRevisionsGVR,
	} {
		res := APIResource{Kind: kind, Name: gvr.Resource, Group: gvr.Group, Version: gvr.Version, Namespaced: true}
		rd.resourceMap[strings.ToLower(kind)] = res
		rd.resourceMap[gvr.Resource] = res
		rd.gvrMap[strings.ToLower(kind)] = gvr
		rd.gvrMap[gvr.Resource] = gvr
	}
	return rd
}

func newAppsClient(objs ...runtime.Object) *dynamicfake.FakeDynamicClient {
	return dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		deploymentsGVR: "DeploymentList", replicaSetsGVR: "ReplicaSetList", statefulSetsGVR: "StatefulSetList",
		daemonSetsGVR: "DaemonSetList", controllerRevisionsGVR: "ControllerRevisionList",
	}, objs...)
}

// Revision 1 is a bare container; revision 2 adds what a later deploy
// typically adds: a volume and its mount, an env var, a sidecar, and the
// restartedAt annotation kubectl rollout restart writes.
func podTemplateV1() map[string]any {
	return map[string]any{
		"metadata": map[string]any{"labels": map[string]any{"app": "web"}},
		"spec": map[string]any{
			"containers": []any{map[string]any{"name": "app", "image": "web:v1"}},
		},
	}
}

func podTemplateV2() map[string]any {
	return map[string]any{
		"metadata": map[string]any{
			"labels":      map[string]any{"app": "web"},
			"annotations": map[string]any{"kubectl.kubernetes.io/restartedAt": "2026-10-27T10:00:00Z"},
		},
		"spec": map[string]any{
			"containers": []any{
				map[string]any{
					"name":         "app",
					"image":        "web:v2",
					"env":          []any{map[string]any{"name": "FEATURE", "value": "on"}},
					"volumeMounts": []any{map[string]any{"name": "cache", "mountPath": "/cache"}},
				},
				map[string]any{"name": "proxy", "image": "envoy:v1"},
			},
			"volumes": []any{map[string]any{"name": "cache", "emptyDir": map[string]any{}}},
		},
	}
}

func withHash(template map[string]any, hash string) map[string]any {
	t := runtime.DeepCopyJSON(template)
	labels := t["metadata"].(map[string]any)["labels"].(map[string]any)
	labels["pod-template-hash"] = hash
	return t
}

func deploymentObject(template map[string]any, annotations map[string]any, mutate func(map[string]any)) *unstructured.Unstructured {
	d := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apps/v1",
		"kind":       "Deployment",
		"metadata": map[string]any{
			"namespace": "prod", "name": "web", "uid": "dep-uid", "resourceVersion": "42",
			"annotations": annotations,
		},
		"spec": map[string]any{
			"replicas": int64(2),
			"selector": map[string]any{"matchLabels": map[string]any{"app": "web"}},
			"template": runtime.DeepCopyJSON(template),
		},
	}}
	if mutate != nil {
		mutate(d.Object)
	}
	return d
}

func deploymentReplicaSet(name, revision, hash string, template map[string]any, annotations map[string]any) *unstructured.Unstructured {
	ann := map[string]any{"deployment.kubernetes.io/revision": revision}
	for k, v := range annotations {
		ann[k] = v
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apps/v1",
		"kind":       "ReplicaSet",
		"metadata": map[string]any{
			"namespace": "prod", "name": name, "annotations": ann,
			"labels": map[string]any{"app": "web", "pod-template-hash": hash},
			"ownerReferences": []any{map[string]any{
				"apiVersion": "apps/v1", "kind": "Deployment", "name": "web", "uid": "dep-uid", "controller": true,
			}},
		},
		"spec": map[string]any{"replicas": int64(0), "template": withHash(template, hash)},
	}}
}

func getDeployment(t *testing.T, client *dynamicfake.FakeDynamicClient) *unstructured.Unstructured {
	t.Helper()
	d, err := client.Resource(deploymentsGVR).Namespace("prod").Get(context.Background(), "web", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get deployment: %v", err)
	}
	return d
}

func patchActions(client *dynamicfake.FakeDynamicClient) []clienttesting.PatchAction {
	var out []clienttesting.PatchAction
	for _, a := range client.Actions() {
		if pa, ok := a.(clienttesting.PatchAction); ok {
			out = append(out, pa)
		}
	}
	return out
}

// Rolling back across a revision that added a volume+mount, env var, sidecar and
// restartedAt must leave exactly the target template: a merge patch would keep
// every one of them, and copying the ReplicaSet template would leak its hash.
func TestRollbackDeploymentRestoresTargetTemplateExactly(t *testing.T) {
	for _, tc := range []struct {
		name             string
		from, to         func() map[string]any
		toRev, fromRev   string
		toHash, fromHash string
	}{
		{name: "back to the revision without the additions", from: podTemplateV2, to: podTemplateV1, fromRev: "2", toRev: "1", fromHash: "h2", toHash: "h1"},
		{name: "forward to the revision with the additions", from: podTemplateV1, to: podTemplateV2, fromRev: "3", toRev: "2", fromHash: "h1", toHash: "h2"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := newAppsClient(
				deploymentObject(tc.from(), map[string]any{"deployment.kubernetes.io/revision": tc.fromRev}, nil),
				deploymentReplicaSet("web-"+tc.toHash, tc.toRev, tc.toHash, tc.to(), nil),
				deploymentReplicaSet("web-"+tc.fromHash, tc.fromRev, tc.fromHash, tc.from(), nil),
			)
			m := NewWorkloadManager(client, appsDiscovery())
			toRev := map[string]int64{"1": 1, "2": 2}[tc.toRev]
			if err := m.RollbackWorkload(context.Background(), "Deployment", "prod", "web", toRev); err != nil {
				t.Fatalf("RollbackWorkload: %v", err)
			}
			got, _, _ := unstructured.NestedMap(getDeployment(t, client).Object, "spec", "template")
			if !reflect.DeepEqual(got, tc.to()) {
				gotJSON, _ := json.Marshal(got)
				wantJSON, _ := json.Marshal(tc.to())
				t.Fatalf("template after rollback\n got  %s\n want %s", gotJSON, wantJSON)
			}
			pa := patchActions(client)
			if len(pa) != 1 || pa[0].GetPatchType() != "application/json-patch+json" {
				t.Fatalf("want one JSON patch, got %d (%v)", len(pa), pa)
			}
			if strings.Contains(string(pa[0].GetPatch()), "pod-template-hash") {
				t.Errorf("patch leaks pod-template-hash: %s", pa[0].GetPatch())
			}
		})
	}
}

// The patch pins the resourceVersion that was compared, so a write that lands
// between the read and the patch is rejected instead of overwritten.
func TestRollbackDeploymentPatchPinsResourceVersion(t *testing.T) {
	client := newAppsClient(
		deploymentObject(podTemplateV2(), nil, nil),
		deploymentReplicaSet("web-h1", "1", "h1", podTemplateV1(), nil),
	)
	m := NewWorkloadManager(client, appsDiscovery())
	if err := m.RollbackWorkload(context.Background(), "Deployment", "prod", "web", 1); err != nil {
		t.Fatalf("RollbackWorkload: %v", err)
	}
	var ops []map[string]any
	if err := json.Unmarshal(patchActions(client)[0].GetPatch(), &ops); err != nil {
		t.Fatal(err)
	}
	if ops[0]["op"] != "replace" || ops[0]["path"] != "/metadata/resourceVersion" || ops[0]["value"] != "42" {
		t.Fatalf("first op does not pin resourceVersion 42: %v", ops[0])
	}
}

func TestRollbackDeploymentConflictSaysTheWorkloadChanged(t *testing.T) {
	client := newAppsClient(
		deploymentObject(podTemplateV2(), nil, nil),
		deploymentReplicaSet("web-h1", "1", "h1", podTemplateV1(), nil),
	)
	client.PrependReactor("patch", "deployments", func(clienttesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewConflict(schema.GroupResource{Group: "apps", Resource: "deployments"}, "web", errors.New("the object has been modified"))
	})
	err := NewWorkloadManager(client, appsDiscovery()).RollbackWorkload(context.Background(), "Deployment", "prod", "web", 1)
	if !apierrors.IsConflict(err) || !strings.Contains(err.Error(), "changed while rolling back") {
		t.Fatalf("want a wrapped conflict naming the change, got %v", err)
	}
}

// A controller status write between the read and the patch bumps the
// resourceVersion; the rollback re-reads and succeeds instead of failing.
func TestRollbackRetriesAfterTransientConflict(t *testing.T) {
	cases := []struct {
		kind, name string
		objects    []runtime.Object
	}{
		{"Deployment", "web", []runtime.Object{
			deploymentObject(podTemplateV2(), nil, nil),
			deploymentReplicaSet("web-h1", "1", "h1", podTemplateV1(), nil),
		}},
		{"StatefulSet", "db", []runtime.Object{
			controllerWorkload("StatefulSet", "db", podTemplateV2()),
			controllerRevisionObject("db", "StatefulSet", "db-1", 1, podTemplateV1()),
		}},
	}
	for _, tc := range cases {
		t.Run(tc.kind, func(t *testing.T) {
			client := newAppsClient(tc.objects...)
			attempts := 0
			client.PrependReactor("patch", "*", func(clienttesting.Action) (bool, runtime.Object, error) {
				attempts++
				if attempts == 1 {
					return true, nil, apierrors.NewConflict(schema.GroupResource{Group: "apps", Resource: "x"}, tc.name, errors.New("the object has been modified"))
				}
				return true, nil, nil
			})
			if err := NewWorkloadManager(client, appsDiscovery()).RollbackWorkload(context.Background(), tc.kind, "prod", tc.name, 1); err != nil {
				t.Fatalf("RollbackWorkload: %v", err)
			}
			if attempts != 2 {
				t.Fatalf("patch attempts = %d, want 2", attempts)
			}
		})
	}
}

// Annotations follow kubectl rollout undo: the controller's bookkeeping stays
// from the live Deployment; everything else (change-cause included) comes back
// from the target revision, and annotations added since are dropped.
func TestRollbackDeploymentRestoresAnnotationsLikeKubectl(t *testing.T) {
	client := newAppsClient(
		deploymentObject(podTemplateV2(), map[string]any{
			"deployment.kubernetes.io/revision":                "2",
			"kubectl.kubernetes.io/last-applied-configuration": "{}",
			"kubernetes.io/change-cause":                       "deploy v2",
			"added-after-v1":                                   "x",
		}, nil),
		deploymentReplicaSet("web-h1", "1", "h1", podTemplateV1(), map[string]any{
			"kubernetes.io/change-cause":            "deploy v1",
			"deployment.kubernetes.io/max-replicas": "3",
		}),
	)
	if err := NewWorkloadManager(client, appsDiscovery()).RollbackWorkload(context.Background(), "Deployment", "prod", "web", 1); err != nil {
		t.Fatalf("RollbackWorkload: %v", err)
	}
	got := getDeployment(t, client).GetAnnotations()
	want := map[string]string{
		"deployment.kubernetes.io/revision":                "2",
		"kubectl.kubernetes.io/last-applied-configuration": "{}",
		"kubernetes.io/change-cause":                       "deploy v1",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("annotations\n got  %v\n want %v", got, want)
	}
}

func TestRollbackDeploymentToCurrentTemplateIsRefused(t *testing.T) {
	// A pod-template-hash label in the live template does not make it a
	// different template.
	client := newAppsClient(
		deploymentObject(withHash(podTemplateV1(), "h1"), nil, nil),
		deploymentReplicaSet("web-h1", "1", "h1", podTemplateV1(), nil),
	)
	err := NewWorkloadManager(client, appsDiscovery()).RollbackWorkload(context.Background(), "Deployment", "prod", "web", 1)
	if !errors.Is(err, rollouts.ErrTemplateUnchanged) {
		t.Fatalf("want ErrTemplateUnchanged, got %v", err)
	}
	if n := len(patchActions(client)); n != 0 {
		t.Fatalf("issued %d patches for a no-op rollback", n)
	}
}

func TestRollbackPausedDeploymentIsRefused(t *testing.T) {
	client := newAppsClient(
		deploymentObject(podTemplateV2(), nil, func(o map[string]any) { o["spec"].(map[string]any)["paused"] = true }),
		deploymentReplicaSet("web-h1", "1", "h1", podTemplateV1(), nil),
	)
	err := NewWorkloadManager(client, appsDiscovery()).RollbackWorkload(context.Background(), "Deployment", "prod", "web", 1)
	if !errors.Is(err, ErrWorkloadPaused) {
		t.Fatalf("want ErrWorkloadPaused, got %v", err)
	}
	if n := len(patchActions(client)); n != 0 {
		t.Fatalf("issued %d patches for a paused Deployment", n)
	}
}

func controllerRevisionObject(owner, ownerKind, name string, revision int64, template map[string]any) *unstructured.Unstructured {
	data := runtime.DeepCopyJSON(template)
	data["$patch"] = "replace"
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apps/v1",
		"kind":       "ControllerRevision",
		"metadata": map[string]any{
			"namespace": "prod", "name": name,
			"ownerReferences": []any{map[string]any{"apiVersion": "apps/v1", "kind": ownerKind, "name": owner, "uid": owner + "-uid", "controller": true}},
		},
		"revision": revision,
		"data":     map[string]any{"spec": map[string]any{"template": data}},
	}}
}

func controllerWorkload(kind, name string, template map[string]any) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apps/v1",
		"kind":       kind,
		"metadata":   map[string]any{"namespace": "prod", "name": name, "uid": name + "-uid", "resourceVersion": "7"},
		"spec":       map[string]any{"template": runtime.DeepCopyJSON(template)},
	}}
}

// StatefulSet and DaemonSet revisions replay the controller's own patch, whose
// template carries $patch: replace, so the template is replaced, not merged.
func TestRollbackControllerRevisionReplacesTemplate(t *testing.T) {
	for _, kind := range []string{"StatefulSet", "DaemonSet"} {
		t.Run(kind, func(t *testing.T) {
			client := newAppsClient(
				controllerWorkload(kind, "db", podTemplateV2()),
				controllerRevisionObject("db", kind, "db-1", 1, podTemplateV1()),
				controllerRevisionObject("db", kind, "db-2", 2, podTemplateV2()),
			)
			var patch []byte
			client.PrependReactor("patch", "*", func(a clienttesting.Action) (bool, runtime.Object, error) {
				pa := a.(clienttesting.PatchAction)
				if pa.GetPatchType() != types.StrategicMergePatchType {
					t.Errorf("patch type %s, want strategic merge", pa.GetPatchType())
				}
				patch = pa.GetPatch()
				return true, nil, nil
			})
			if err := NewWorkloadManager(client, appsDiscovery()).RollbackWorkload(context.Background(), kind, "prod", "db", 1); err != nil {
				t.Fatalf("RollbackWorkload: %v", err)
			}
			var body map[string]any
			if err := json.Unmarshal(patch, &body); err != nil {
				t.Fatalf("patch not JSON: %v", err)
			}
			if rv, _, _ := unstructured.NestedString(body, "metadata", "resourceVersion"); rv != "7" {
				t.Errorf("patch does not pin resourceVersion 7: %s", patch)
			}

			// Apply it the way the API server does, to the typed object, and
			// check every revision-2 addition is gone.
			live, _ := json.Marshal(controllerWorkload(kind, "db", podTemplateV2()).Object)
			var typed any = &appsv1.StatefulSet{}
			if kind == "DaemonSet" {
				typed = &appsv1.DaemonSet{}
			}
			merged, err := strategicpatch.StrategicMergePatch(live, patch, typed)
			if err != nil {
				t.Fatalf("apply strategic merge patch: %v", err)
			}
			var result map[string]any
			if err := json.Unmarshal(merged, &result); err != nil {
				t.Fatal(err)
			}
			got, _, _ := unstructured.NestedMap(result, "spec", "template")
			if !reflect.DeepEqual(got, podTemplateV1()) {
				gotJSON, _ := json.Marshal(got)
				t.Errorf("template after rollback is not revision 1: %s", gotJSON)
			}
		})
	}
}

func TestRollbackControllerRevisionToCurrentTemplateIsRefused(t *testing.T) {
	client := newAppsClient(
		controllerWorkload("StatefulSet", "db", podTemplateV1()),
		controllerRevisionObject("db", "StatefulSet", "db-1", 1, podTemplateV1()),
	)
	err := NewWorkloadManager(client, appsDiscovery()).RollbackWorkload(context.Background(), "StatefulSet", "prod", "db", 1)
	if !errors.Is(err, rollouts.ErrTemplateUnchanged) {
		t.Fatalf("want ErrTemplateUnchanged, got %v", err)
	}
	if n := len(patchActions(client)); n != 0 {
		t.Fatalf("issued %d patches for a no-op rollback", n)
	}
}
