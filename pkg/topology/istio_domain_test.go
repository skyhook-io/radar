package topology

import (
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func TestIstioObservedServiceDomain(t *testing.T) {
	deployment := func(args ...string) *appsv1.Deployment {
		return &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "istiod"}}, Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "discovery", Args: args}}}}}}
	}
	for _, tc := range []struct {
		name, domain string
		deployments  []*appsv1.Deployment
	}{
		{"no observed control plane", "cluster.local", nil},
		{"documented default", "cluster.local", []*appsv1.Deployment{deployment("discovery")}},
		{"custom domain", "mesh.internal", []*appsv1.Deployment{deployment("discovery", "--domain", "mesh.internal")}},
		{"equals argument", "mesh.internal", []*appsv1.Deployment{deployment("discovery", "--domain=mesh.internal")}},
		{"same revisions", "mesh.internal", []*appsv1.Deployment{deployment("--domain=mesh.internal"), deployment("--domain=mesh.internal")}},
		{"conflicting revisions", "", []*appsv1.Deployment{deployment("--domain=mesh.internal"), deployment("discovery")}},
		{"unresolved env", "", []*appsv1.Deployment{deployment("--domain=$(DOMAIN)")}},
		{"missing argument", "", []*appsv1.Deployment{deployment("--domain")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			domain := NewBuilder(&mockProvider{deployments: tc.deployments}).istioServiceDomain()
			if domain != tc.domain {
				t.Fatalf("domain=%q, want %q", domain, tc.domain)
			}
			name, namespace := parseIstioHost("api.prod.svc.mesh.internal", "app", domain)
			if (name == "api" && namespace == "prod") != (domain == "mesh.internal") {
				t.Fatalf("unexpected custom-domain join %s/%s", namespace, name)
			}
			if name, _ := parseIstioHost("api.prod.svc.unobserved.test", "app", domain); name != "" {
				t.Fatal("inferred unobserved domain")
			}
		})
	}
}

func TestDestinationRuleDomainReachesRelationshipProducer(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "networking.istio.io", Version: "v1", Resource: "destinationrules"}
	dr := genericIdentityObject(gvr, "DestinationRule", "app", "reviews")
	dr.Object["spec"] = map[string]any{"host": "reviews.prod.svc.mesh.internal"}
	d := &genericIdentityDynamic{watched: []schema.GroupVersionResource{gvr}, kinds: map[schema.GroupVersionResource]string{gvr: "DestinationRule"}, resources: map[schema.GroupVersionResource][]*unstructured.Unstructured{gvr: {dr}}, listCalls: map[schema.GroupVersionResource]int{}}
	p := &mockProvider{deployments: []*appsv1.Deployment{{ObjectMeta: metav1.ObjectMeta{Name: "istiod", Namespace: "istio-system", Labels: map[string]string{"app": "istiod"}}, Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "discovery", Args: []string{"--domain=mesh.internal"}}}}}}}}, services: []*corev1.Service{{ObjectMeta: metav1.ObjectMeta{Name: "reviews", Namespace: "prod"}}}}
	topo, err := NewBuilder(p).WithDynamic(d).Build(DefaultBuildOptions())
	if err != nil {
		t.Fatal(err)
	}
	rel := GetRelationshipsWithObject("destinationrules", "app", "reviews", dr, topo, p, d, nil)
	if rel == nil || rel.IstioServiceDomain != "mesh.internal" {
		t.Fatalf("domain missing: %+v", rel)
	}
}
