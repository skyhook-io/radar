package server

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/investigation"
	"github.com/skyhook-io/radar/pkg/resourcecontext"
)

func TestDetectDiagnoseHealthPreservesCrossplanePause(t *testing.T) {
	for _, ack := range []bool{false, true} {
		name := "requested"
		want := resourcecontext.CrossplanePauseRequested
		if ack {
			name = "acknowledged"
			want = resourcecontext.CrossplanePauseAcknowledged
		}
		t.Run(name, func(t *testing.T) {
			gvr := schema.GroupVersionResource{Group: "pause.example.org", Version: "v1", Resource: "buckets"}
			conditions := []any{map[string]any{"type": "Ready", "status": "True"}}
			if ack {
				conditions = append(conditions, map[string]any{"type": "Synced", "status": "False", "reason": "ReconcilePaused"})
			}
			obj := &unstructured.Unstructured{Object: map[string]any{
				"apiVersion": "pause.example.org/v1", "kind": "Bucket",
				"metadata": map[string]any{"name": "archive", "namespace": "default", "annotations": map[string]any{"crossplane.io/paused": "true"}},
				"status":   map[string]any{"conditions": conditions},
			}}
			dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{gvr: "BucketList"}, obj)
			if err := k8s.InitTestDynamicResourceCache(dyn, []k8s.APIResource{{Group: gvr.Group, Version: gvr.Version, Kind: "Bucket", Name: gvr.Resource, Namespaced: true, IsCRD: true, Verbs: []string{"get", "list", "watch"}}}); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(k8s.ResetTestDynamicState)
			cache := k8s.GetDynamicResourceCache()
			if err := cache.EnsureWatching(gvr); err != nil {
				t.Fatal(err)
			}
			if !cache.WaitForSync(gvr, 5*time.Second) {
				t.Fatal("dynamic fixture cache did not sync")
			}
			signal := (&Server{}).detectDiagnoseHealth(httptest.NewRequest("GET", "/", nil), "Bucket", gvr.Group, "default", "archive")
			if signal == nil || signal.CrossplanePause != want || signal.Health != "healthy" || signal.IssueCount != 0 {
				t.Fatalf("kickoff signal=%+v", signal)
			}
			prompt := investigation.TaskPrompt(investigation.Target{Kind: "Bucket", Group: gvr.Group, Namespace: "default", Name: "archive"}, signal, investigation.Options{})
			if !strings.Contains(prompt, "availability as healthy") || !strings.Contains(prompt, "Verify quickly") {
				t.Fatalf("kickoff prompt lost independent context: %s", prompt)
			}
		})
	}
}

func TestDetectDiagnoseHealthExcludesConvertedNativePauseAnnotation(t *testing.T) {
	replicas := int32(1)
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "native-copy", Namespace: "default", Annotations: map[string]string{"crossplane.io/paused": "true"}},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "native-copy"}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "native-copy"}},
				Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "example.invalid/test:latest"}}},
			},
		},
	}
	useTestResourceCache(t, fake.NewClientset(dep))
	obj, err := k8s.GetResourceCache().GetDynamicWithGroup(t.Context(), "Deployment", "default", "native-copy", "apps")
	if err != nil {
		t.Fatal(err)
	}
	if obj.GetAnnotations()["crossplane.io/paused"] != "true" {
		t.Fatal("fixture lost annotation during typed conversion")
	}
	signal := (&Server{}).detectDiagnoseHealth(httptest.NewRequest("GET", "/", nil), "Deployment", "apps", "default", "native-copy")
	if signal == nil || signal.CrossplanePause != "" {
		t.Fatalf("native kickoff invented Crossplane context: %+v", signal)
	}
	prompt := investigation.TaskPrompt(investigation.Target{Kind: "Deployment", Group: "apps", Namespace: "default", Name: "native-copy"}, signal, investigation.Options{})
	if strings.Contains(prompt, "crossplane.io/paused") {
		t.Fatalf("native annotation projected as reconciliation intent: %s", prompt)
	}
}
