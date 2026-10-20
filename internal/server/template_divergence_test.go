package server

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// The resource API carries the Pod/template comparison the drawer shows, and
// only when the live Pod differs.
func TestGetResourceIncludesTemplateDivergence(t *testing.T) {
	const ns = "drift-api"
	ctx := context.Background()
	controller := true
	template := corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "nginx:1.27",
		Resources: corev1.ResourceRequirements{Limits: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("256Mi")}}}}}}
	rs := &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Name: "app-1", Namespace: ns, UID: "drift-rs"}, Spec: appsv1.ReplicaSetSpec{Template: template}}
	pod := func(name, memory string) *corev1.Pod {
		spec := template.Spec.DeepCopy()
		spec.Containers[0].Resources.Limits[corev1.ResourceMemory] = resource.MustParse(memory)
		return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns,
			OwnerReferences: []metav1.OwnerReference{{Kind: "ReplicaSet", APIVersion: "apps/v1", Name: rs.Name, UID: rs.UID, Controller: &controller}}}, Spec: *spec}
	}
	if _, err := testFakeClient.AppsV1().ReplicaSets(ns).Create(ctx, rs, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = testFakeClient.AppsV1().ReplicaSets(ns).Delete(context.Background(), rs.Name, metav1.DeleteOptions{})
	})
	for _, p := range []*corev1.Pod{pod("app-1-mutated", "16Mi"), pod("app-1-healthy", "256Mi")} {
		if _, err := testFakeClient.CoreV1().Pods(ns).Create(ctx, p, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			_ = testFakeClient.CoreV1().Pods(ns).Delete(context.Background(), p.Name, metav1.DeleteOptions{})
		})
	}
	get := func(path string) map[string]any {
		t.Helper()
		resp, err := http.Get(testServer.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil
		}
		var body map[string]any
		if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		return body
	}
	deadline := time.Now().Add(5 * time.Second)
	var body map[string]any
	for time.Now().Before(deadline) {
		if body = get("/api/resources/pods/" + ns + "/app-1-mutated"); body != nil && body["templateDivergence"] != nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	divergence, ok := body["templateDivergence"].(map[string]any)
	if !ok {
		t.Fatalf("missing templateDivergence: %+v", body)
	}
	if divergence["baseline"] != "ReplicaSet app-1" || divergence["differences"].([]any)[0] != "containers[app].resources.limits.memory is 16Mi; template specifies 256Mi" {
		t.Fatalf("templateDivergence=%+v", divergence)
	}
	if body["resource"] == nil {
		t.Fatal("the comparison replaced the resource")
	}
	healthy := get("/api/resources/pods/" + ns + "/app-1-healthy")
	if _, present := healthy["templateDivergence"]; present || healthy["resource"] == nil {
		t.Fatalf("matching Pod carried a comparison: %+v", healthy)
	}
	rsBody := get("/api/resources/replicasets/" + ns + "/app-1")
	if workload, ok := rsBody["templateDivergence"].(map[string]any); !ok || workload["podsCompared"] != float64(2) || workload["podsDiffering"] != float64(1) || workload["examplePod"] != "app-1-mutated" {
		t.Fatalf("replicaset templateDivergence=%+v", rsBody["templateDivergence"])
	}
}
