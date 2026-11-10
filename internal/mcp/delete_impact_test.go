package mcp

import (
	"context"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	kubefake "k8s.io/client-go/kubernetes/fake"
	clienttesting "k8s.io/client-go/testing"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/k8score"
)

func TestDeleteResourceConfirmationKeepsDependentsAdvisory(t *testing.T) {
	for _, addDependent := range []bool{false, true} {
		t.Run(map[bool]string{false: "status-only lag", true: "new dependent"}[addDependent], func(t *testing.T) {
			setConnectedForTest(t)
			deployment := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "demo", UID: "dep", ResourceVersion: "1", Generation: 1}}
			core := kubefake.NewClientset(deployment)
			if err := k8s.InitTestResourceCache(core); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(k8s.ResetTestState)
			obj := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": map[string]any{"name": "web", "namespace": "demo", "uid": "dep", "resourceVersion": "1", "generation": int64(1)}}}
			gvr := schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}
			dyn := setupMCPDynamicResource(t, gvr, "DeploymentList", k8s.APIResource{Group: "apps", Version: "v1", Kind: "Deployment", Name: "deployments", Namespaced: true}, obj).(*dynamicfake.FakeDynamicClient)
			addDeletePreviewKinds(t)
			deletes := 0
			dyn.PrependReactor("delete", "deployments", func(a clienttesting.Action) (bool, runtime.Object, error) {
				deletes++
				return true, nil, nil
			})
			input := deleteResourceInput{Kind: "Deployment", Namespace: "demo", Name: "web"}
			res, _, err := handleDeleteResource(context.Background(), nil, input)
			if err != nil {
				t.Fatal(err)
			}
			preview := decodeToolResult(t, res)
			if preview["cascade"].(map[string]any)["rootResolved"] != true {
				t.Fatalf("unresolved fixture: %+v", preview)
			}
			input.Confirm = preview["confirm"].(string)
			if addDependent {
				_, err := core.CoreV1().Pods("demo").Create(context.Background(), &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "new", Namespace: "demo", UID: "new", OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "Deployment", Name: "web", UID: "dep"}}}}, metav1.CreateOptions{})
				if err != nil {
					t.Fatal(err)
				}
				if err := wait.PollUntilContextTimeout(context.Background(), time.Millisecond, 5*time.Second, true, func(context.Context) (bool, error) {
					_, found, _ := k8s.GetResourceCache().ReadableObject(k8score.Pods, "demo", "new")
					return found, nil
				}); err != nil {
					t.Fatal(err)
				}
			} else {
				obj.SetResourceVersion("2")
				if _, err := dyn.Resource(gvr).Namespace("demo").Update(context.Background(), obj, metav1.UpdateOptions{}); err != nil {
					t.Fatal(err)
				}
			}
			no := false
			input.DryRun = &no
			_, _, err = handleDeleteResource(context.Background(), nil, input)
			if err != nil || deletes != 2 {
				t.Fatalf("root-bound confirmation rejected unchanged target: %v deletes=%d", err, deletes)
			}
		})
	}
}
