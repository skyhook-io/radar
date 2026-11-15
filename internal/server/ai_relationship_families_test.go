package server

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/resourcecontext"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
)

func TestAIContextFamiliesExactGetAndHiddenExistence(t *testing.T) {
	withOwnershipDiscovery(t)
	k8s.GetResourceDiscovery().AddAPIResource(k8s.APIResource{Group: "networking.k8s.io", Version: "v1", Name: "ingresses", Kind: "Ingress", Namespaced: true, Verbs: metav1.Verbs{"get", "list", "watch", "delete"}})
	one := int32(1)
	dep := &appsv1.Deployment{TypeMeta: metav1.TypeMeta{APIVersion: "apps/v1", Kind: "Deployment"}, ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "app", UID: "web"}, Spec: appsv1.DeploymentSpec{Replicas: &one, Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "web"}}, Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "web"}}, Spec: corev1.PodSpec{ImagePullSecrets: []corev1.LocalObjectReference{{Name: "hidden-secret"}}}}}}
	svc := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "hidden-service", Namespace: "app"}, Spec: corev1.ServiceSpec{Selector: map[string]string{"app": "web"}}}
	ingress := &networkingv1.Ingress{ObjectMeta: metav1.ObjectMeta{Name: "public", Namespace: "app"}, Spec: networkingv1.IngressSpec{DefaultBackend: &networkingv1.IngressBackend{Service: &networkingv1.IngressServiceBackend{Name: svc.Name}}}}
	hidden := ingress.DeepCopy()
	hidden.Name = "hidden-ingress"
	var before []byte
	for _, present := range []bool{false, true} {
		objects := []runtime.Object{dep, svc}
		if present {
			objects = append(objects, ingress, hidden, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "hidden-secret", Namespace: "app"}})
		}
		useTestResourceCache(t, fake.NewClientset(objects...))
		env := newAuthTestServer(t)
		perms := &auth.UserPermissions{AllowedNamespaces: []string{"app"}}
		for _, grant := range []struct{ group, resource string }{{"apps", "deployments"}, {"networking.k8s.io", "ingresses"}} {
			perms.SetCanI("get", grant.group, grant.resource, "app", true)
		}
		perms.SetCanI("get", "", "services", "app", false)
		perms.SetCanI("get", "", "secrets", "app", false)
		env.srv.permCache.Set("reader", nil, perms)
		req := httptest.NewRequest("GET", "/api/ai/resources/deployments/app/web", nil)
		req = req.WithContext(auth.ContextWithUser(req.Context(), &auth.User{Username: "reader"}))
		rc := env.srv.buildAIResourceContext(req, dep, "Deployment", "app", "web")
		if present && (len(rc.ExposedBy) != 2 || rc.ExposedBy[0].Kind != "Ingress") {
			t.Fatalf("readable ingress behind hidden Service lost: %+v", rc.ExposedBy)
		}
		raw, _ := json.Marshal(rc)
		if strings.Contains(string(raw), "hidden-service") || strings.Contains(string(raw), "hidden-secret") {
			t.Fatalf("hidden identity leaked: %s", raw)
		}
		// Deny the entire source kind: narrowing notes must not depend on matches.
		perms.SetCanI("get", "networking.k8s.io", "ingresses", "app", false)
		rc = env.srv.buildAIResourceContext(req, dep, "Deployment", "app", "web")
		raw, _ = json.Marshal(rc)
		if !hasContextOmission(rc, "exposedBy", resourcecontext.OmittedRBACDenied) || !hasContextOmission(rc, "uses.secrets", resourcecontext.OmittedRBACDenied) {
			t.Fatalf("missing policy narrowing notes: %s", raw)
		}
		if before != nil && string(before) != string(raw) {
			t.Fatalf("hidden existence changed context:\n%s\n%s", before, raw)
		}
		before = raw
	}
}

func hasContextOmission(rc *resourcecontext.ResourceContext, field string, reason resourcecontext.OmittedReason) bool {
	for _, omitted := range rc.Omitted {
		if omitted.Field == field && omitted.Reason == reason {
			return true
		}
	}
	return false
}
