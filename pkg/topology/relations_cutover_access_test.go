package topology

import (
	"encoding/json"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/skyhook-io/radar/pkg/ownership"
)

func TestRelationsAnswerDoesNotRevealHiddenReverseReferenceExistence(t *testing.T) {
	workload := projectionObject(&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "app"}, Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "web"}}}}}, "apps", "Deployment")
	service := projectionObject(&corev1.Service{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "front"}, Spec: corev1.ServiceSpec{Selector: map[string]string{"app": "web"}}}, "", "Service")
	ingress := projectionObject(&networkingv1.Ingress{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "hidden"}, Spec: networkingv1.IngressSpec{DefaultBackend: &networkingv1.IngressBackend{Service: &networkingv1.IngressServiceBackend{Name: "front"}}}}, "networking.k8s.io", "Ingress")
	access := projectionAccess{denied: []string{"Ingress"}}
	var before []byte
	for _, exists := range []bool{false, true} {
		session := newProjectionSession(t, workload, service, ingress)
		if !exists {
			delete(session.objects, projectionRef(ingress))
		}
		answer := projectionAnswer(t, session, workload, access, ownership.Unrestricted())
		data, err := json.Marshal(answer)
		if err != nil {
			t.Fatal(err)
		}
		if before != nil && string(data) != string(before) {
			t.Fatalf("hidden existence changed the answer: %s => %s", before, data)
		}
		before = data
		for _, ref := range session.reads {
			if ref == projectionRef(ingress) {
				t.Fatal("read a ref denied for emission")
			}
		}
	}
}
