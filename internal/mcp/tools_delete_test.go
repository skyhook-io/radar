package mcp

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/topology"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
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
	if cascade["approximation"] != true || len(cascade["notEnumerated"].([]any)) != 1 || cascade["notEnumerated"].([]any)[0] != "controller-finalizer cleanup" {
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
	if _, _, err := handleDeleteResource(context.Background(), nil, direct); err == nil || !strings.Contains(err.Error(), "object replaced") {
		t.Fatalf("replacement refusal: %v", err)
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
	for _, tc := range []struct{ kind, version, want, category string }{{"Namespace", "v1", "ALL namespace contents", "namespace contents"}, {"CustomResourceDefinition", "apiextensions.k8s.io/v1", "ALL instances", "CRD instances"}} {
		obj := &unstructured.Unstructured{}
		obj.SetKind(tc.kind)
		obj.SetAPIVersion(tc.version)
		obj.SetName("target")
		result := deleteCascadePreview(context.Background(), obj, metav1.DeletePropagationOrphan)
		if !strings.Contains(result["scopeWarning"].(string), tc.want) || result["propagationEffect"] == nil || !reflect.DeepEqual(result["notEnumerated"], []string{tc.category}) {
			t.Fatalf("scope warning: %+v", result)
		}
	}
}

func TestDeleteCascadeWithholdsUnreadableDependents(t *testing.T) {
	previousMemo := summaryCtxTopoMemo
	summaryCtxTopoMemo = topology.NewMemoizer(5 * time.Second)
	t.Cleanup(func() { summaryCtxTopoMemo = previousMemo })
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
	if len(refs) != 1 || refs[0].Name != "web-pod" || result["withheld"] != nil || result["visibilityNote"] != "Dependents you can't read are not listed." {
		t.Fatalf("caller visibility: %+v", result)
	}
	counts := result["dependentCounts"].(*deleteDependentCounts)
	if counts.Total != 1 || !reflect.DeepEqual(counts.ByKind, map[string]int{"Pod": 1}) {
		t.Fatalf("counts disclosed unreadable ReplicaSet: %+v", counts)
	}
	summary := deletePreviewSummary(obj, metav1.DeletePropagationBackground, result)
	if strings.Contains(summary, "ReplicaSet") || !strings.Contains(summary, "Pod: 1") {
		t.Fatalf("summary disclosed unreadable ReplicaSet: %s", summary)
	}
	perms.SetCanI("get", "apps", "replicasets", "demo", true)
	readable := deleteCascadePreview(ctx, obj, metav1.DeletePropagationBackground)
	if len(readable["dependents"].([]topology.ResourceRef)) != 2 || readable["withheld"] != nil || readable["visibilityNote"] != result["visibilityNote"] {
		t.Fatalf("readable dependents changed coverage disclosure: %+v", readable)
	}
	if counts := readable["dependentCounts"].(*deleteDependentCounts); counts.Total != 2 || !reflect.DeepEqual(counts.ByKind, map[string]int{"ReplicaSet": 1, "Pod": 1}) {
		t.Fatalf("readable counts: %+v", counts)
	}
	obj.SetName("not-in-topology")
	missing := deleteCascadePreview(ctx, obj, metav1.DeletePropagationBackground)
	if missing["rootResolved"] != false || missing["coverage"] != "unknown" || missing["visibilityNote"] != result["visibilityNote"] {
		t.Fatalf("unresolved root was not unknown: %+v", missing)
	}
}

func TestDeleteResourceGenerationBinding(t *testing.T) {
	for _, change := range []string{"status", "spec", "finalizers", "finalizer order", "replacement"} {
		t.Run(change, func(t *testing.T) {
			gvr := schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}
			obj := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": map[string]any{"name": "web", "namespace": "demo", "uid": "u", "resourceVersion": "1", "generation": int64(5)}}}
			obj.SetFinalizers([]string{"example.com/a", "example.com/b"})
			dyn := setupMCPDynamicResource(t, gvr, "DeploymentList", k8s.APIResource{Group: "apps", Version: "v1", Kind: "Deployment", Name: "deployments", Namespaced: true}, obj).(*dynamicfake.FakeDynamicClient)
			deletes := 0
			dyn.PrependReactor("delete", "deployments", func(a clienttesting.Action) (bool, runtime.Object, error) {
				opts := a.(clienttesting.DeleteAction).GetDeleteOptions()
				if opts.Preconditions == nil || *opts.Preconditions.UID != "u" || opts.Preconditions.ResourceVersion != nil {
					t.Fatalf("wrong generation preconditions: %+v", opts)
				}
				deletes++
				return true, nil, nil
			})
			input := deleteResourceInput{Kind: "Deployment", Namespace: "demo", Name: "web"}
			res, _, err := handleDeleteResource(context.Background(), nil, input)
			if err != nil {
				t.Fatal(err)
			}
			input.Confirm = decodeToolResult(t, res)["confirm"].(string)
			falseValue := false
			input.DryRun = &falseValue
			live := obj.DeepCopy()
			live.SetResourceVersion("2")
			switch change {
			case "status":
				live.Object["status"] = map[string]any{"readyReplicas": int64(1)}
			case "spec":
				live.SetGeneration(6)
			case "finalizers":
				live.SetFinalizers([]string{"example.com/new"})
			case "finalizer order":
				live.SetFinalizers([]string{"example.com/b", "example.com/a"})
			case "replacement":
				live.SetUID("new-uid")
			}
			if err := dyn.Tracker().Update(gvr, live, "demo"); err != nil {
				t.Fatal(err)
			}
			_, _, err = handleDeleteResource(context.Background(), nil, input)
			switch change {
			case "status", "finalizer order":
				if err != nil || deletes != 2 {
					t.Fatalf("safe reconciliation refused: %v, calls %d", err, deletes)
				}
			default:
				want := "object changed since preview"
				if change == "replacement" {
					want = "object replaced"
				}
				if err == nil || !strings.Contains(err.Error(), want) || deletes != 1 {
					t.Fatalf("unsafe change: %v, calls %d", err, deletes)
				}
			}
		})
	}
}

func TestDeleteResourceAudit(t *testing.T) {
	var output bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&output)
	t.Cleanup(func() { log.SetOutput(previous) })
	gvr := schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}
	obj := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": map[string]any{"name": "web", "namespace": "demo", "uid": "u", "resourceVersion": "1", "generation": int64(1)}}}
	dyn := setupMCPDynamicResource(t, gvr, "DeploymentList", k8s.APIResource{Group: "apps", Version: "v1", Kind: "Deployment", Name: "deployments", Namespaced: true}, obj).(*dynamicfake.FakeDynamicClient)
	denied := true
	dyn.PrependReactor("delete", "deployments", func(a clienttesting.Action) (bool, runtime.Object, error) {
		if denied {
			return true, nil, apierrors.NewForbidden(gvr.GroupResource(), "web", fmt.Errorf("denied"))
		}
		return true, nil, nil
	})
	ctx := context.Background()
	input := deleteResourceInput{Kind: "Deployment", Namespace: "demo", Name: "web"}
	if _, _, err := handleDeleteResource(ctx, nil, input); err == nil {
		t.Fatal("denial accepted")
	}
	if !strings.Contains(output.String(), `outcome="preview_failed"`) {
		t.Fatalf("preview audit: %s", output.String())
	}
	denied = false
	res, _, err := handleDeleteResource(ctx, nil, input)
	if err != nil {
		t.Fatal(err)
	}
	falseValue := false
	input.DryRun = &falseValue
	input.Confirm = decodeToolResult(t, res)["confirm"].(string)
	denied = true
	if _, _, err := handleDeleteResource(ctx, nil, input); err == nil {
		t.Fatal("denial accepted")
	}
	for _, want := range []string{`kind="Deployment"`, `api_group="apps"`, `outcome="failed"`} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("missing %s in audit: %s", want, output.String())
		}
	}
}

func TestDeleteCascadeIncludesCallerReadableCertificateSecret(t *testing.T) {
	previousMemo := summaryCtxTopoMemo
	summaryCtxTopoMemo = topology.NewMemoizer(5 * time.Second)
	t.Cleanup(func() { summaryCtxTopoMemo = previousMemo })
	deployment := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "demo"}, Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Volumes: []corev1.Volume{{Name: "tls", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: "web-tls"}}}}}}}}
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "web-tls", Namespace: "demo", OwnerReferences: []metav1.OwnerReference{{APIVersion: "cert-manager.io/v1", Kind: "Certificate", Name: "web-cert", UID: "cert-uid"}}}}
	if err := k8s.InitTestResourceCache(kubefake.NewClientset(deployment, secret)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k8s.ResetTestState)
	certificate := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "cert-manager.io/v1", "kind": "Certificate", "metadata": map[string]any{"name": "web-cert", "namespace": "demo", "uid": "cert-uid"}, "spec": map[string]any{"secretName": "web-tls"}}}
	gvr := schema.GroupVersionResource{Group: "cert-manager.io", Version: "v1", Resource: "certificates"}
	setupMCPDynamicResource(t, gvr, "CertificateList", k8s.APIResource{Group: "cert-manager.io", Version: "v1", Kind: "Certificate", Name: "certificates", Namespaced: true, Verbs: []string{"get", "list", "watch"}}, certificate)
	cache := k8s.GetDynamicResourceCache()
	if err := cache.EnsureWatching(gvr); err != nil {
		t.Fatal(err)
	}
	if !cache.WaitForSync(gvr, 5*time.Second) {
		t.Fatal("certificate cache did not sync")
	}
	ctx := withClusterAdmin(t, "certificate-preview-reader")
	perms := getPermCache().Get("certificate-preview-reader", nil)
	perms.SetCanI("get", "", "secrets", "demo", false)
	hidden := deleteCascadePreview(ctx, certificate, metav1.DeletePropagationBackground)
	if len(hidden["dependents"].([]topology.ResourceRef)) != 0 || hidden["withheld"] != nil {
		t.Fatalf("unreadable Secret disclosed: %+v", hidden)
	}
	perms.SetCanI("get", "", "secrets", "demo", true)
	visible := deleteCascadePreview(ctx, certificate, metav1.DeletePropagationBackground)
	refs := visible["dependents"].([]topology.ResourceRef)
	if len(refs) != 1 || refs[0].Kind != "Secret" || refs[0].Name != "web-tls" || visible["withheld"] != nil {
		t.Fatalf("owned Secret missing: %+v", visible)
	}
	if hidden["visibilityNote"] != "Dependents you can't read are not listed." || visible["visibilityNote"] != hidden["visibilityNote"] {
		t.Fatalf("visibility note depends on withheld objects: hidden=%+v visible=%+v", hidden, visible)
	}
}

func TestDeleteCascadeLargeClusterSkipsGraphBuild(t *testing.T) {
	previousMemo := summaryCtxTopoMemo
	summaryCtxTopoMemo = topology.NewMemoizer(5 * time.Second)
	t.Cleanup(func() { summaryCtxTopoMemo = previousMemo })
	objects := make([]runtime.Object, 0, topology.LargeClusterThreshold)
	for i := 0; i < topology.LargeClusterThreshold; i++ {
		objects = append(objects, &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("web-%d", i), Namespace: "demo"}})
	}
	if err := k8s.InitTestResourceCache(kubefake.NewClientset(objects...)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k8s.ResetTestState)
	obj := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": map[string]any{"name": "web-0", "namespace": "demo"}}}
	result := deleteCascadePreview(context.Background(), obj, metav1.DeletePropagationBackground)
	if result["coverage"] != "unknown" || len(result["dependents"].([]topology.ResourceRef)) != 0 || result["visibilityNote"] != nil || !strings.Contains(result["reason"].(string), "too large") {
		t.Fatalf("large cluster cascade overclaimed: %+v", result)
	}
	opts := topology.DefaultBuildOptions()
	opts.IncludeReplicaSets = true
	opts.IncludeSecrets = true
	guarded, err := summaryCtxTopoMemo.Get(opts, func() (*topology.Topology, error) {
		t.Fatal("preview did not memoize the guarded topology")
		return nil, nil
	})
	if err != nil || !guarded.RequiresNamespaceFilter || len(guarded.Nodes) != 0 || len(guarded.Edges) != 0 {
		t.Fatalf("large cluster built a graph: %+v, %v", guarded, err)
	}
}

func TestDeleteResourcePreviewSummaryAndDependentCounts(t *testing.T) {
	for _, tc := range []struct {
		name              string
		replicaSets, pods int
		propagation       metav1.DeletionPropagation
		wantSummary       string
	}{
		{"deployment", 3, 12, metav1.DeletePropagationBackground, "Deletes Deployment demo/web; observed dependents: Pod: 12, ReplicaSet: 3 (15 total, not exhaustive); may wait on finalizers: example.com/cleanup"},
		{"truncated", 3, 105, metav1.DeletePropagationForeground, "Deletes Deployment demo/web; observed dependents: Pod: 105, ReplicaSet: 3 (108 total, not exhaustive); may wait on finalizers: example.com/cleanup"},
		{"orphan", 1, 1, metav1.DeletePropagationOrphan, "Deletes Deployment demo/web; orphans observed dependents: Pod: 1, ReplicaSet: 1 (2 total, not exhaustive); may wait on finalizers: example.com/cleanup"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			previousMemo := summaryCtxTopoMemo
			summaryCtxTopoMemo = topology.NewMemoizer(5 * time.Second)
			t.Cleanup(func() { summaryCtxTopoMemo = previousMemo })
			deployment := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "demo", UID: "dep"}}
			objects := []runtime.Object{deployment}
			for i := 0; i < tc.replicaSets; i++ {
				objects = append(objects, &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("web-rs-%d", i), Namespace: "demo", UID: types.UID(fmt.Sprintf("rs-%d", i)), OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "Deployment", Name: "web", UID: "dep"}}}})
			}
			for i := 0; i < tc.pods; i++ {
				owner := i % tc.replicaSets
				objects = append(objects, &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("web-pod-%d", i), Namespace: "demo", OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "ReplicaSet", Name: fmt.Sprintf("web-rs-%d", owner), UID: types.UID(fmt.Sprintf("rs-%d", owner))}}}})
			}
			if err := k8s.InitTestResourceCache(kubefake.NewClientset(objects...)); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(k8s.ResetTestState)
			obj := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": map[string]any{"name": "web", "namespace": "demo", "uid": "dep", "resourceVersion": "1", "generation": int64(1)}}}
			obj.SetFinalizers([]string{"example.com/cleanup"})
			dyn := setupMCPDynamicResource(t, schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}, "DeploymentList", k8s.APIResource{Group: "apps", Version: "v1", Kind: "Deployment", Name: "deployments", Namespaced: true}, obj).(*dynamicfake.FakeDynamicClient)
			dyn.PrependReactor("delete", "deployments", func(a clienttesting.Action) (bool, runtime.Object, error) {
				if opts := a.(clienttesting.DeleteAction).GetDeleteOptions(); !slices.Equal(opts.DryRun, []string{metav1.DryRunAll}) {
					t.Fatalf("preview persisted: %+v", opts)
				}
				return true, nil, nil
			})
			res, _, err := handleDeleteResource(context.Background(), nil, deleteResourceInput{Kind: "Deployment", Namespace: "demo", Name: "web", Propagation: string(tc.propagation)})
			if err != nil {
				t.Fatal(err)
			}
			got := decodeToolResult(t, res)
			if got["summary"] != tc.wantSummary {
				t.Fatalf("summary = %v, want %s", got["summary"], tc.wantSummary)
			}
			if !strings.HasPrefix(res.Content[0].(*mcpsdk.TextContent).Text, `{"summary":`) {
				t.Fatal("summary must lead the preview")
			}
			cascade := got["cascade"].(map[string]any)
			counts := cascade["dependentCounts"].(map[string]any)
			if counts["total"] != float64(tc.replicaSets+tc.pods) || counts["basis"] != "observed (not exhaustive)" || !reflect.DeepEqual(counts["byKind"], map[string]any{"ReplicaSet": float64(tc.replicaSets), "Pod": float64(tc.pods)}) {
				t.Fatalf("dependent counts: %+v", counts)
			}
			wantListed := min(100, tc.replicaSets+tc.pods)
			if len(cascade["dependents"].([]any)) != wantListed || (cascade["truncated"] == true) != (counts["total"].(float64) > 100) {
				t.Fatalf("dependent list cap: %+v", cascade)
			}
		})
	}
}

func TestDeleteCascadeNotEnumeratedOnlyWhenApplicable(t *testing.T) {
	for _, tc := range []struct {
		name, kind, version, namespace string
		finalizers, want               []string
	}{
		{"configmap", "ConfigMap", "v1", "demo", nil, nil},
		{"configmap with finalizer", "ConfigMap", "v1", "demo", []string{"example.com/cleanup"}, []string{"controller-finalizer cleanup"}},
		{"cluster-scoped", "ClusterRole", "rbac.authorization.k8s.io/v1", "", nil, []string{"cluster-scoped cascades outside cached topology"}},
		{"namespace with metadata finalizer", "Namespace", "v1", "", []string{"example.com/cleanup"}, []string{"controller-finalizer cleanup", "namespace contents"}},
		{"colliding namespace kind", "Namespace", "example.com/v1", "demo", nil, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			obj := &unstructured.Unstructured{}
			obj.SetKind(tc.kind)
			obj.SetAPIVersion(tc.version)
			obj.SetNamespace(tc.namespace)
			obj.SetFinalizers(tc.finalizers)
			result := deleteCascadePreview(context.Background(), obj, metav1.DeletePropagationBackground)
			if tc.want == nil {
				if _, exists := result["notEnumerated"]; exists {
					t.Fatalf("irrelevant warning: %+v", result)
				}
			} else if !reflect.DeepEqual(result["notEnumerated"], tc.want) {
				t.Fatalf("notEnumerated = %v, want %v", result["notEnumerated"], tc.want)
			}
		})
	}
}

func TestDeletePreviewSummaryUnknownAndNamespaceFinalizers(t *testing.T) {
	obj := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "Namespace", "metadata": map[string]any{"name": "demo"}, "spec": map[string]any{"finalizers": []any{"kubernetes"}}}}
	cascade := map[string]any{"coverage": "unknown", "dependentCounts": &deleteDependentCounts{ByKind: map[string]int{}, Basis: "observed (not exhaustive)"}}
	if summary := deletePreviewSummary(obj, metav1.DeletePropagationOrphan, cascade); summary != "Deletes Namespace demo and all namespace contents (count unknown); dependents unknown; may wait on finalizers: kubernetes" {
		t.Fatalf("namespace summary: %s", summary)
	}
	obj.SetKind("ConfigMap")
	obj.SetNamespace("demo")
	obj.SetName("settings")
	if summary := deletePreviewSummary(obj, metav1.DeletePropagationBackground, cascade); summary != "Deletes ConfigMap demo/settings; dependents unknown" {
		t.Fatalf("unknown dependents reported as absent: %s", summary)
	}
	cascade["coverage"] = "partial"
	if summary := deletePreviewSummary(obj, metav1.DeletePropagationBackground, cascade); summary != "Deletes ConfigMap demo/settings; no caller-readable dependents observed" {
		t.Fatalf("empty observed dependents: %s", summary)
	}
}

func TestDeleteResourcePreviewFinalizerFields(t *testing.T) {
	for _, kind := range []string{"ConfigMap", "Namespace"} {
		t.Run(kind, func(t *testing.T) {
			namespace, resource := "demo", "configmaps"
			if kind == "Namespace" {
				namespace, resource = "", "namespaces"
			}
			obj := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": kind, "metadata": map[string]any{"name": "target", "namespace": namespace, "uid": "uid", "resourceVersion": "1"}}}
			if kind == "Namespace" {
				obj.Object["spec"] = map[string]any{"finalizers": []any{"kubernetes"}}
			}
			gvr := schema.GroupVersionResource{Version: "v1", Resource: resource}
			dyn := setupMCPDynamicResource(t, gvr, kind+"List", k8s.APIResource{Version: "v1", Kind: kind, Name: resource, Namespaced: namespace != ""}, obj).(*dynamicfake.FakeDynamicClient)
			dyn.PrependReactor("delete", resource, func(clienttesting.Action) (bool, runtime.Object, error) { return true, nil, nil })
			res, _, err := handleDeleteResource(context.Background(), nil, deleteResourceInput{Kind: kind, Namespace: namespace, Name: "target"})
			if err != nil {
				t.Fatal(err)
			}
			got := decodeToolResult(t, res)
			if finalizers, exists := got["finalizers"]; !exists || finalizers != nil || got["finalizerGuidance"] != nil {
				t.Fatalf("metadata finalizer shape changed: %+v", got)
			}
			cascade := got["cascade"].(map[string]any)
			if kind == "Namespace" {
				if !reflect.DeepEqual(got["namespaceFinalizers"], []any{"kubernetes"}) || got["namespaceFinalizerGuidance"] == nil || !reflect.DeepEqual(cascade["notEnumerated"], []any{"namespace contents"}) || !strings.Contains(got["summary"].(string), "may wait on finalizers: kubernetes") {
					t.Fatalf("namespace lifecycle finalizers lost: %+v", got)
				}
			} else if cascade["notEnumerated"] != nil || got["namespaceFinalizers"] != nil || got["namespaceFinalizerGuidance"] != nil {
				t.Fatalf("unrelated cleanup fields: %+v", got)
			}
		})
	}
}
