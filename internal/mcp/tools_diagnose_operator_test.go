package mcp

import (
	"context"
	"encoding/json"
	corev1 "k8s.io/api/core/v1"
	"strings"
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/ownership"
	"github.com/skyhook-io/radar/pkg/resourceid"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
)

func TestOperatorDiagnoseUsesObservedOwnership(t *testing.T) {
	yes, zero := true, int32(0)
	root := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "opentelemetry.io/v1beta1", "kind": "OpenTelemetryCollector", "metadata": map[string]any{"name": "collector", "namespace": "observe", "uid": "root"}, "spec": map[string]any{"mode": "deployment", "config": map[string]any{}}}}
	child := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "actual-name", Namespace: "observe", UID: "child", OwnerReferences: []metav1.OwnerReference{{APIVersion: "opentelemetry.io/v1beta1", Kind: "OpenTelemetryCollector", Name: "collector", UID: "root", Controller: &yes}}}, Spec: appsv1.DeploymentSpec{Replicas: &zero, Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "collector"}}}}
	decoy := child.DeepCopy()
	decoy.Name = "collector-collector"
	decoy.UID = "decoy"
	decoy.OwnerReferences[0].UID = "previous-root"
	initOperatorDiagnosisCache(t, root, child, decoy)
	result, _, err := handleDiagnose(context.Background(), nil, testDiagnoseInput("opentelemetrycollector", "observe", "collector"))
	if err != nil {
		t.Fatal(err)
	}
	var got operatorDiagnoseResponse
	if err := json.Unmarshal([]byte(extractText(t, result)), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.OwnedWorkloads) != 1 || got.OwnedWorkloads[0].Ref.Name != "actual-name" || got.OwnedWorkloads[0].Diagnosis == nil || got.OwnedWorkloads[0].Error != "" {
		t.Fatalf("wrong runtime bundle: %s", extractText(t, result))
	}
	if got.OwnedWorkloads[0].Diagnosis.Pods != 0 {
		t.Fatal("scaled-to-zero workload gained Pods")
	}
	if got.ResourceContext == nil || got.ResourceContext.Children == nil {
		t.Fatal("root lost followable owned refs")
	}
	if _, _, err := handleDiagnose(context.Background(), nil, diagnoseInput{diagnoseCommonInput: diagnoseCommonInput{Kind: "OpenTelemetryCollector", Group: "foreign.example", Namespace: "observe", Name: "collector"}}); err == nil {
		t.Fatal("foreign root was accepted")
	}
}

func TestOwnedWorkloadBundleRequiresCurrentControllerUID(t *testing.T) {
	yes := true
	root := ownership.Observation{Ref: resourceid.Ref{Group: "opentelemetry.io", Kind: "OpenTelemetryCollector", Namespace: "observe", Name: "collector"}, UID: "root"}
	u := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Namespace: "observe", OwnerReferences: []metav1.OwnerReference{{APIVersion: "opentelemetry.io/v1beta1", Kind: "OpenTelemetryCollector", Name: "collector", UID: "root", Controller: &yes}}}}
	if !ownedByOperator(u, root) {
		t.Fatal("current owned workload rejected")
	}
	u.OwnerReferences[0].UID = "old-root"
	if ownedByOperator(u, root) {
		t.Fatal("prior incarnation accepted")
	}
	u.OwnerReferences[0].UID = "root"
	u.OwnerReferences[0].APIVersion = "other.example/v1"
	if ownedByOperator(u, root) {
		t.Fatal("foreign-group owner accepted")
	}
	u.OwnerReferences[0].APIVersion = "opentelemetry.io/v1beta1"
	u.OwnerReferences[0].Controller = nil
	if ownedByOperator(u, root) {
		t.Fatal("non-controller ownership attributed as runtime")
	}
}

func initOperatorDiagnosisCache(t *testing.T, root *unstructured.Unstructured, objects ...runtime.Object) {
	t.Helper()
	t.Cleanup(k8s.ResetTestState)
	t.Cleanup(k8s.ResetTestDynamicState)
	_, group, resource, ok := operatorDiagnoseTarget(root.GetKind())
	if !ok {
		t.Fatal("unsupported test root")
	}
	if err := k8s.InitTestResourceCache(fake.NewClientset(objects...)); err != nil {
		t.Fatal(err)
	}
	gvr := schema.GroupVersionResource{Group: group, Version: root.GroupVersionKind().Version, Resource: resource}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{gvr: root.GetKind() + "List"}, root)
	if err := k8s.InitTestDynamicResourceCache(dyn, []k8s.APIResource{{Group: gvr.Group, Version: gvr.Version, Name: gvr.Resource, Kind: root.GetKind(), Namespaced: true, Verbs: []string{"get", "list", "watch", "delete"}}, {Group: "apps", Version: "v1", Name: "deployments", Kind: "Deployment", Namespaced: true, Verbs: []string{"get", "list", "watch", "delete"}}, {Group: "apps", Version: "v1", Name: "statefulsets", Kind: "StatefulSet", Namespaced: true, Verbs: []string{"get", "list", "watch", "delete"}}, {Group: "apps", Version: "v1", Name: "daemonsets", Kind: "DaemonSet", Namespaced: true, Verbs: []string{"get", "list", "watch", "delete"}}, {Group: "example.io", Version: "v1", Name: "widgets", Kind: "Widget", Namespaced: true, Verbs: []string{"get", "list", "watch", "delete"}}, {Group: "apps", Version: "v1", Name: "replicasets", Kind: "ReplicaSet", Namespaced: true, Verbs: []string{"get", "list", "watch", "delete"}}, {Version: "v1", Name: "pods", Kind: "Pod", Namespaced: true, Verbs: []string{"get", "list", "watch", "delete"}}}); err != nil {
		t.Fatal(err)
	}
	dc := k8s.GetDynamicResourceCache()
	if err := dc.EnsureWatching(gvr); err != nil || !dc.WaitForSync(gvr, 2*time.Second) {
		t.Fatalf("root not synced: %v", err)
	}
}

func TestOperatorDiagnoseCachedEvidenceUsesNamespaceVisibility(t *testing.T) {
	for _, tc := range []struct {
		name         string
		pods, events bool
	}{
		{"pods-and-events-denied", false, false},
		{"events-denied", true, false},
		{"readable-positive-control", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			yes := true
			root := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "opentelemetry.io/v1beta1", "kind": "OpenTelemetryCollector", "metadata": map[string]any{"name": "collector", "namespace": "observe", "uid": "root"}}}
			child := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "runtime", Namespace: "observe", UID: "runtime", OwnerReferences: []metav1.OwnerReference{{APIVersion: "opentelemetry.io/v1beta1", Kind: "OpenTelemetryCollector", Name: "collector", UID: "root", Controller: &yes}}}, Spec: appsv1.DeploymentSpec{Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "runtime"}}}}
			rs := &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Name: "runtime-rs", Namespace: "observe", UID: "rs", OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "Deployment", Name: "runtime", UID: "runtime", Controller: &yes}}}, Spec: appsv1.ReplicaSetSpec{Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "runtime"}}}}
			pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "restricted-pod-marker", Namespace: "observe", UID: "pod", Labels: map[string]string{"app": "runtime"}, OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "ReplicaSet", Name: "runtime-rs", UID: "rs", Controller: &yes}}}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app"}}}}
			event := &corev1.Event{ObjectMeta: metav1.ObjectMeta{Name: "private-event", Namespace: "observe"}, Type: "Warning", Reason: "FailedCreate", Message: `admission webhook "private-hook" denied the request: restricted-event-marker`, InvolvedObject: corev1.ObjectReference{APIVersion: "apps/v1", Kind: "ReplicaSet", Namespace: "observe", Name: "runtime-rs", UID: "rs"}}
			initOperatorDiagnosisCache(t, root, child, rs, pod, event)
			ctx := withTestUserPerms(t, "operator-reader", nil, []string{"observe"})
			perms := getPermCache().Get("operator-reader", nil)
			perms.SetCanI("get", "opentelemetry.io", "opentelemetrycollectors", "observe", true)
			for _, verb := range []string{"get", "list"} {
				for _, resource := range []string{"deployments", "replicasets"} {
					perms.SetCanI(verb, "apps", resource, "observe", true)
				}
				perms.SetCanI(verb, "", "pods", "observe", tc.pods)
				perms.SetCanI(verb, "", "events", "observe", tc.events)
			}
			result, _, err := handleDiagnose(ctx, nil, testDiagnoseInput("opentelemetrycollector", "observe", "collector"))
			if err != nil {
				t.Fatal(err)
			}
			text := extractText(t, result)
			if !strings.Contains(text, "restricted-event-marker") {
				t.Fatalf("namespace-visible Event evidence was withheld: %s", text)
			}
			if !strings.Contains(text, "restricted-pod-marker") {
				t.Fatalf("namespace-visible Pod membership was withheld: %s", text)
			}
			var got operatorDiagnoseResponse
			if err := json.Unmarshal([]byte(text), &got); err != nil {
				t.Fatal(err)
			}
			if len(got.OwnedWorkloads) != 1 || got.OwnedWorkloads[0].Diagnosis == nil {
				t.Fatalf("readable workload lost its partial bundle: %s", text)
			}
			bundle := got.OwnedWorkloads[0].Diagnosis
			if len(bundle.RelatedIssues) == 0 {
				t.Fatalf("positive control did not exercise Event-derived Issues: %+v", bundle)
			}
			if bundle.EventsError != "" {
				t.Fatalf("cached Events should use namespace visibility: %+v", bundle)
			}
			if bundle.Pods != 1 || len(bundle.PodNames) != 1 {
				t.Fatalf("namespace-visible membership changed with per-kind grants: %+v", bundle)
			}
		})
	}
}

func TestWorkloadDiagnoseNamespacePolicyMatchesResourceRead(t *testing.T) {
	yes := true
	root := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "opentelemetry.io/v1beta1", "kind": "OpenTelemetryCollector", "metadata": map[string]any{"name": "collector", "namespace": "observe", "uid": "root"}}}
	deployment := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "runtime", Namespace: "observe", UID: "deployment"}, Spec: appsv1.DeploymentSpec{Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "runtime"}}}}
	rs := &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Name: "runtime-rs", Namespace: "observe", UID: "rs", OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "Deployment", Name: "runtime", UID: "deployment", Controller: &yes}}}, Spec: appsv1.ReplicaSetSpec{Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "runtime"}}}}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "runtime-pod", Namespace: "observe", UID: "pod", Labels: map[string]string{"app": "runtime"}, OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "ReplicaSet", Name: "runtime-rs", UID: "rs", Controller: &yes}}}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app"}}}}
	event := &corev1.Event{ObjectMeta: metav1.ObjectMeta{Name: "blocked", Namespace: "observe"}, Type: "Warning", Reason: "FailedCreate", Message: `admission webhook "fixture" denied the request: namespace-evidence-marker`, InvolvedObject: corev1.ObjectReference{APIVersion: "apps/v1", Kind: "ReplicaSet", Namespace: "observe", Name: "runtime-rs", UID: "rs"}}
	initOperatorDiagnosisCache(t, root, deployment, rs, pod, event)
	ctx := withTestUserPerms(t, "namespace-reader", nil, []string{"observe"})
	perms := getPermCache().Get("namespace-reader", nil)
	perms.SetCanI("get", "apps", "deployments", "observe", false)
	perms.SetCanI("list", "", "pods", "observe", false)
	perms.SetCanI("list", "", "events", "observe", false)
	gotResource, _, err := handleGetResource(ctx, nil, getResourceInput{Kind: "Deployment", Group: "apps", Namespace: "observe", Name: "runtime", Include: "issues"})
	if err != nil {
		t.Fatal(err)
	}
	gotDiagnosis, _, err := handleDiagnose(ctx, nil, testDiagnoseInput("deployment", "observe", "runtime"))
	if err != nil {
		t.Fatal(err)
	}
	for surface, result := range map[string]string{"get_resource": extractText(t, gotResource), "diagnose": extractText(t, gotDiagnosis)} {
		if !strings.Contains(result, "namespace-evidence-marker") {
			t.Fatalf("%s withheld namespace-visible evidence: %s", surface, result)
		}
	}
	var diagnosis diagnoseResponse
	if err := json.Unmarshal([]byte(extractText(t, gotDiagnosis)), &diagnosis); err != nil {
		t.Fatal(err)
	}
	if diagnosis.Pods != 1 || len(diagnosis.RelatedIssues) == 0 || len(diagnosis.StartupBlockers) == 0 {
		t.Fatalf("incomplete cached workload evidence: %+v", diagnosis)
	}
	if diagnosis.LogsError == "" {
		t.Fatal("unavailable live log client was not qualified")
	}
	if _, _, err := handleDiagnose(ctx, nil, testDiagnoseInput("deployment", "hidden", "runtime")); err == nil || !strings.Contains(err.Error(), "forbidden") {
		t.Fatalf("namespace boundary was not enforced: %v", err)
	}
}
