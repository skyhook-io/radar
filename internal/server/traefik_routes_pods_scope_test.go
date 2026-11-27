package server

import (
	"net/http"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/k8score"
)

// A pod store that doesn't hold the backend's namespace lists nothing with
// no error; that must read as unknown, not "0 of 0 pods ready".
func TestTraefikRoutesNoCountsFromAPodStoreMissingTheNamespace(t *testing.T) {
	restoreFixtureCache(t)
	k8s.ResetResourceCache()
	client := fake.NewClientset(
		&corev1.Service{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "ui"}, Spec: corev1.ServiceSpec{Selector: map[string]string{"app": "ui"}, Ports: []corev1.ServicePort{{Port: 80}}}},
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "ui-1", Labels: map[string]string{"app": "ui"}}, Status: corev1.PodStatus{PodIP: "10.0.0.1", Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}}},
	)
	if err := k8s.InitScopedTestResourceCacheNamespaces(client,
		map[string]k8score.ResourceScope{k8score.Pods: {Enabled: true, Namespace: "other"}, k8score.Services: {Enabled: true}, k8score.Namespaces: {Enabled: true}},
		map[string][]string{k8score.Pods: {"other"}}); err != nil {
		t.Fatal(err)
	}
	seedCNPGWorkspace(t, traefikKinds, traefikIR("shop", "ui", "Host(`ui.example.com`)", "", "", nil))
	resp, err := http.Get(testServer.URL + "/api/traefik/routes?namespace=shop&name=ui")
	if err != nil {
		t.Fatal(err)
	}
	got, _ := decodeTraefik(t, resp)
	if len(got.Routes) != 1 || len(got.Routes[0].Services) != 1 {
		t.Fatalf("routes = %+v", got.Routes)
	}
	if b := got.Routes[0].Services[0]; b.ReadyPods != nil || b.TotalPods != nil {
		t.Errorf("counts from a pod store that doesn't cover shop: %+v", b)
	}
	if hasProblem(got.Routes[0], "backend_not_ready") {
		t.Errorf("backend_not_ready from an uncovered namespace: %+v", got.Routes[0].Problems)
	}
}
