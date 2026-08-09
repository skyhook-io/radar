package ingressstatus

import (
	"testing"

	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestClassifyUnresolvedClass(t *testing.T) {
	missing := "missing"
	blank := "  "
	tests := []struct {
		name string
		ing  *networkingv1.Ingress
		want UnresolvedClass
	}{
		{name: "named missing", ing: &networkingv1.Ingress{Spec: networkingv1.IngressSpec{IngressClassName: &missing}}, want: NamedClassMissing},
		{name: "nil ingress", want: ClassResolutionNotActionable},
		{name: "no class", ing: &networkingv1.Ingress{}, want: NoClassOrDefault},
		{name: "blank class", ing: &networkingv1.Ingress{Spec: networkingv1.IngressSpec{IngressClassName: &blank}}, want: NoClassOrDefault},
		{name: "assigned address", ing: &networkingv1.Ingress{Spec: networkingv1.IngressSpec{IngressClassName: &missing}, Status: networkingv1.IngressStatus{LoadBalancer: networkingv1.IngressLoadBalancerStatus{Ingress: []networkingv1.IngressLoadBalancerIngress{{IP: "192.0.2.1"}}}}}, want: ClassResolutionNotActionable},
		{name: "legacy class", ing: &networkingv1.Ingress{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{"kubernetes.io/ingress.class": "nginx"}}, Spec: networkingv1.IngressSpec{IngressClassName: &missing}}, want: ClassResolutionNotActionable},
		{name: "cloud annotation", ing: &networkingv1.Ingress{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{"alb.ingress.kubernetes.io/scheme": "internet-facing"}}, Spec: networkingv1.IngressSpec{IngressClassName: &missing}}, want: ClassResolutionNotActionable},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := ClassifyUnresolvedClass(test.ing); got != test.want {
				t.Fatalf("ClassifyUnresolvedClass() = %v, want %v", got, test.want)
			}
		})
	}
}
