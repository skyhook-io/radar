package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/timeline"
	"github.com/skyhook-io/radar/pkg/issuesapi"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// A namespace-filtered change query keeps its own rows namespace-scoped and
// carries the admission configuration changes that reach the namespace beside
// them.
func TestGetChangesNamespaceQueryCarriesClusterScopedAdmissionChanges(t *testing.T) {
	store := initCorrelationStore(t)
	for _, event := range []timeline.TimelineEvent{
		{ID: "webhook", Kind: "MutatingWebhookConfiguration", APIVersion: "admissionregistration.k8s.io/v1", Name: "gatekeeper-mutating-webhook-configuration", EventType: timeline.EventTypeAdd},
		{ID: "deploy", Kind: "Deployment", APIVersion: "apps/v1", Namespace: "shop", Name: "web", EventType: timeline.EventTypeUpdate,
			Diff: &timeline.DiffInfo{Fields: []timeline.FieldChange{{Path: "spec.template.spec.containers[web].image"}}}},
	} {
		event.Timestamp, event.Source, event.ClusterContext = time.Now(), timeline.SourceInformer, k8s.ActiveClusterContext()
		if err := store.Append(context.Background(), event); err != nil {
			t.Fatal(err)
		}
	}
	decode := func(input getChangesInput) getChangesResponseMCP {
		t.Helper()
		result, _, err := handleGetChanges(context.Background(), nil, input)
		if err != nil {
			t.Fatal(err)
		}
		var response getChangesResponseMCP
		if err := json.Unmarshal([]byte(extractText(t, result)), &response); err != nil {
			t.Fatal(err)
		}
		return response
	}
	got := decode(getChangesInput{Namespace: "shop"})
	if len(got.Changes) != 1 || got.Changes[0].Name != "web" {
		t.Fatalf("namespace feed must stay namespace-scoped: %+v", got.Changes)
	}
	section := got.ClusterScopedChanges
	if section == nil || len(section.Changes) != 1 || section.Changes[0].Name != "gatekeeper-mutating-webhook-configuration" {
		t.Fatalf("cluster-scoped section=%+v", section)
	}
	// No cached webhook configurations in this test: scope is unknown, and
	// the section must say so rather than claim the webhook applies.
	if section.Changes[0].Applicability != issuesapi.ClusterScopedMayApply || section.Incomplete == "" {
		t.Fatalf("unknown scope overclaimed: %+v", section)
	}
	if got := decode(getChangesInput{Namespace: "shop", Kind: "Deployment"}); got.ClusterScopedChanges != nil {
		t.Fatalf("kind-filtered query gained a section: %+v", got.ClusterScopedChanges)
	}
	if got := decode(getChangesInput{}); got.ClusterScopedChanges != nil || len(got.Changes) != 2 {
		t.Fatalf("cluster-wide query: section=%+v changes=%+v", got.ClusterScopedChanges, got.Changes)
	}
}

func TestGetResourcePodTemplateDivergenceInContextAndAsBareNote(t *testing.T) {
	defer k8s.ResetTestState()
	controller := true
	template := corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "web", Image: "nginx", Resources: corev1.ResourceRequirements{Limits: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("256Mi")}}}}}}
	rs := &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Name: "web-1", Namespace: "shop", UID: "rs"}, Spec: appsv1.ReplicaSetSpec{Template: template}}
	spec := template.Spec.DeepCopy()
	spec.Containers[0].Resources.Limits[corev1.ResourceMemory] = resource.MustParse("16Mi")
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "web-1-abc", Namespace: "shop", OwnerReferences: []metav1.OwnerReference{{Kind: "ReplicaSet", APIVersion: "apps/v1", Name: "web-1", UID: "rs", Controller: &controller}}}, Spec: *spec}
	if err := k8s.InitTestResourceCache(fake.NewClientset(fixtureNamespaces(rs, pod)...)); err != nil {
		t.Fatal(err)
	}
	call := func(mode string) map[string]any {
		t.Helper()
		result, _, err := handleGetResource(t.Context(), nil, getResourceInput{Kind: "pod", Namespace: "shop", Name: "web-1-abc", Context: mode})
		if err != nil {
			t.Fatal(err)
		}
		var body map[string]any
		if err := json.Unmarshal([]byte(extractText(t, result)), &body); err != nil {
			t.Fatal(err)
		}
		return body
	}
	body := call("")
	divergence, _ := body["resourceContext"].(map[string]any)["templateDivergence"].(map[string]any)
	if divergence == nil || divergence["baseline"] != "ReplicaSet web-1" || !strings.Contains(divergence["differences"].([]any)[0].(string), "limits.memory is 16Mi; template specifies 256Mi") {
		t.Fatalf("templateDivergence=%+v", body["resourceContext"])
	}
	bare := call("none")
	if _, present := bare["resourceContext"]; present {
		t.Fatal("context=none emitted resource context")
	}
	warnings, _ := bare["warnings"].([]any)
	if len(warnings) != 1 || !strings.HasPrefix(warnings[0].(string), "Live Pod spec differs from ReplicaSet web-1's template: containers[web].resources.limits.memory is 16Mi") {
		t.Fatalf("bare note=%+v", bare)
	}
	if bare["resource"].(map[string]any)["metadata"].(map[string]any)["name"] != "web-1-abc" {
		t.Fatalf("note leaked into the object: %+v", bare)
	}
}
