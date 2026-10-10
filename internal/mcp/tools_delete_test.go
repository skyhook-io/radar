package mcp

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/topology"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	kubefake "k8s.io/client-go/kubernetes/fake"
	clienttesting "k8s.io/client-go/testing"
)

func TestDeleteResourceMandatoryPreviewAndPreconditions(t *testing.T) {
	gvr := schema.GroupVersionResource{Version: "v1", Resource: "configmaps"}
	obj := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "ConfigMap", "metadata": map[string]any{"name": "held", "namespace": "demo", "uid": "uid-1", "resourceVersion": "42"}}}
	obj.SetFinalizers([]string{"example.com/cleanup"})
	dyn := setupMCPDynamicResource(t, gvr, "ConfigMapList", k8s.APIResource{Version: "v1", Kind: "ConfigMap", Name: "configmaps", Namespaced: true}, obj).(*dynamicfake.FakeDynamicClient)
	calls := 0
	dyn.PrependReactor("delete", "configmaps", func(a clienttesting.Action) (bool, runtime.Object, error) {
		opts := a.(clienttesting.DeleteAction).GetDeleteOptions()
		if opts.Preconditions == nil || *opts.Preconditions.UID != "uid-1" || *opts.Preconditions.ResourceVersion != "42" {
			t.Fatalf("missing preconditions: %+v", opts)
		}
		if opts.PropagationPolicy == nil || *opts.PropagationPolicy != metav1.DeletePropagationBackground {
			t.Fatalf("wrong propagation: %+v", opts)
		}
		if calls == 0 && (len(opts.DryRun) != 1 || opts.DryRun[0] != metav1.DryRunAll) {
			t.Fatalf("preview persisted: %+v", opts)
		}
		if calls == 1 && len(opts.DryRun) != 0 {
			t.Fatalf("real delete was dry-run: %+v", opts)
		}
		calls++
		return true, nil, nil
	})
	input := deleteResourceInput{Kind: "ConfigMap", Namespace: "demo", Name: "held"}
	falseValue := false
	direct := input
	direct.DryRun = &falseValue
	if _, _, err := handleDeleteResource(context.Background(), nil, direct); err == nil || !strings.Contains(err.Error(), "confirm is required") {
		t.Fatalf("unpreviewed deletion: %v", err)
	}
	res, _, err := handleDeleteResource(context.Background(), nil, input)
	if err != nil {
		t.Fatal(err)
	}
	got := decodeToolResult(t, res)
	if got["dry_run"] != true || got["finalizerGuidance"] == nil || got["uid"] != "uid-1" {
		t.Fatalf("preview: %+v", got)
	}
	cascade := got["cascade"].(map[string]any)
	if cascade["approximation"] != true || len(cascade["notEnumerated"].([]any)) != 3 {
		t.Fatalf("cascade overclaimed: %+v", cascade)
	}
	token := got["confirm"].(string)
	direct.Confirm = token
	changedPolicy := direct
	changedPolicy.Propagation = "orphan"
	if _, _, err := handleDeleteResource(context.Background(), nil, changedPolicy); err == nil {
		t.Fatal("changed propagation accepted")
	}
	live, _ := dyn.Resource(gvr).Namespace("demo").Get(context.Background(), "held", metav1.GetOptions{})
	live.SetResourceVersion("43")
	if _, err := dyn.Resource(gvr).Namespace("demo").Update(context.Background(), live, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := handleDeleteResource(context.Background(), nil, direct); err == nil || !strings.Contains(err.Error(), "changed") {
		t.Fatalf("stale token accepted: %v", err)
	}
	live.SetResourceVersion("42")
	if _, err := dyn.Resource(gvr).Namespace("demo").Update(context.Background(), live, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	live.SetUID("replacement")
	dyn.Tracker().Update(gvr, live, "demo")
	if _, _, err := handleDeleteResource(context.Background(), nil, direct); err == nil {
		t.Fatal("replacement accepted")
	}
	live.SetUID("uid-1")
	now := metav1.Now()
	live.SetDeletionTimestamp(&now)
	dyn.Tracker().Update(gvr, live, "demo")
	res, _, err = handleDeleteResource(context.Background(), nil, direct)
	if err != nil {
		t.Fatal(err)
	}
	got = decodeToolResult(t, res)
	obs := got["observation"].(map[string]any)
	if obs["deletionTimestamp"] == nil || len(obs["pendingFinalizers"].([]any)) != 1 {
		t.Fatalf("missing pending observation: %+v", got)
	}
	if calls != 2 {
		t.Fatalf("delete calls %d, want preview + apply", calls)
	}
	for _, a := range dyn.Actions() {
		if a.GetVerb() == "patch" {
			t.Fatal("delete stripped finalizers")
		}
	}
}

func TestDeleteResourcePreviewDenialAndRace(t *testing.T) {
	for _, reason := range []string{"denial", "race"} {
		t.Run(reason, func(t *testing.T) {
			gvr := schema.GroupVersionResource{Version: "v1", Resource: "configmaps"}
			obj := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "ConfigMap", "metadata": map[string]any{"name": "cfg", "namespace": "demo", "uid": "u", "resourceVersion": "1"}}}
			dyn := setupMCPDynamicResource(t, gvr, "ConfigMapList", k8s.APIResource{Version: "v1", Kind: "ConfigMap", Name: "configmaps", Namespaced: true}, obj).(*dynamicfake.FakeDynamicClient)
			dyn.PrependReactor("delete", "configmaps", func(a clienttesting.Action) (bool, runtime.Object, error) {
				opts := a.(clienttesting.DeleteAction).GetDeleteOptions()
				if reason == "denial" {
					return true, nil, apierrors.NewForbidden(gvr.GroupResource(), "cfg", fmt.Errorf("no delete permission"))
				}
				if len(opts.DryRun) > 0 {
					return true, nil, nil
				}
				if opts.Preconditions == nil {
					t.Fatal("race was unguarded")
				}
				return true, nil, apierrors.NewConflict(gvr.GroupResource(), "cfg", fmt.Errorf("resourceVersion changed"))
			})
			input := deleteResourceInput{Kind: "ConfigMap", Namespace: "demo", Name: "cfg"}
			res, _, err := handleDeleteResource(context.Background(), nil, input)
			if reason == "denial" {
				if err == nil || !apierrors.IsForbidden(err) || res != nil || strings.Contains(err.Error(), "object changed") {
					t.Fatalf("denial lost: %v %+v", err, res)
				}
				return
			}
			input.Confirm = decodeToolResult(t, res)["confirm"].(string)
			falseValue := false
			input.DryRun = &falseValue
			if _, _, err := handleDeleteResource(context.Background(), nil, input); err == nil || !apierrors.IsConflict(err) || !strings.Contains(err.Error(), "dry_run=true again") {
				t.Fatalf("race conflict lost: %v", err)
			}
		})
	}
}

func TestDeleteCascadeScopeWarnings(t *testing.T) {
	for _, tc := range []struct{ kind, version, want string }{{"Namespace", "v1", "ALL namespace contents"}, {"CustomResourceDefinition", "apiextensions.k8s.io/v1", "ALL instances"}} {
		obj := &unstructured.Unstructured{}
		obj.SetKind(tc.kind)
		obj.SetAPIVersion(tc.version)
		result := deleteCascadePreview(context.Background(), obj, metav1.DeletePropagationOrphan)
		if !strings.Contains(result["scopeWarning"].(string), tc.want) || result["propagationEffect"] == nil {
			t.Fatalf("scope warning: %+v", result)
		}
	}
}

func TestDeleteCascadeWithholdsUnreadableDependents(t *testing.T) {
	deployment := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "demo", UID: "dep"}}
	rs := &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Name: "web-rs", Namespace: "demo", UID: "rs", OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "Deployment", Name: "web", UID: "dep"}}}}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "web-pod", Namespace: "demo", OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "ReplicaSet", Name: "web-rs", UID: "rs"}}}}
	if err := k8s.InitTestResourceCache(kubefake.NewClientset(deployment, rs, pod)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k8s.ResetTestState)
	ctx := withClusterAdmin(t, "delete-preview-reader")
	perms := getPermCache().Get("delete-preview-reader", nil)
	perms.SetCanI("get", "apps", "replicasets", "demo", false)
	perms.SetCanI("get", "", "pods", "demo", true)
	obj := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": map[string]any{"name": "web", "namespace": "demo"}}}
	setupMCPDynamicResource(t, schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}, "DeploymentList", k8s.APIResource{Group: "apps", Version: "v1", Kind: "Deployment", Name: "deployments", Namespaced: true}, obj)
	result := deleteCascadePreview(ctx, obj, metav1.DeletePropagationBackground)
	refs := result["dependents"].([]topology.ResourceRef)
	if len(refs) != 1 || refs[0].Name != "web-pod" || result["withheld"] != 1 {
		t.Fatalf("caller visibility: %+v", result)
	}
	obj.SetName("not-in-topology")
	missing := deleteCascadePreview(ctx, obj, metav1.DeletePropagationBackground)
	if missing["rootResolved"] != false || missing["coverage"] != "unknown" {
		t.Fatalf("unresolved root was not unknown: %+v", missing)
	}
}
