package resourcecontext

import (
	"context"
	"fmt"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"slices"
	"testing"
)

type unavailableIngressProvider struct{ mockResourceProvider }

func (p unavailableIngressProvider) Ingresses() ([]*networkingv1.Ingress, error) {
	return p.ingresses, fmt.Errorf("unavailable fixture source")
}

func TestBuild_IngressTLSUnavailableSource(t *testing.T) {
	ing := &networkingv1.Ingress{ObjectMeta: metav1.ObjectMeta{Name: "entry", Namespace: "a"}, Spec: networkingv1.IngressSpec{TLS: []networkingv1.IngressTLS{{SecretName: "tls"}}}}
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "tls", Namespace: "a"}}
	rc := Build(context.Background(), secret, Options{Tier: TierBasic, Provider: unavailableIngressProvider{mockResourceProvider{ingresses: []*networkingv1.Ingress{ing}}}})
	if rc.ReferencedBy != nil || !slices.Contains(rc.Omitted, OmittedField{Field: "referencedBy", Reason: OmittedUnavailable}) {
		t.Fatalf("failed source masqueraded as empty/success: %+v", rc)
	}
}

func TestBuild_IngressTLSReverseAndForward(t *testing.T) {
	ing := &networkingv1.Ingress{ObjectMeta: metav1.ObjectMeta{Name: "entry", Namespace: "a"}, Spec: networkingv1.IngressSpec{TLS: []networkingv1.IngressTLS{{SecretName: "tls"}, {SecretName: "tls"}, {}}}}
	other := ing.DeepCopy()
	other.Namespace = "b"
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "tls", Namespace: "a"}}
	provider := mockResourceProvider{ingresses: []*networkingv1.Ingress{nil, other, ing}}
	rc := Build(context.Background(), secret, Options{Tier: TierBasic, Provider: provider})
	if rc.ReferencedBy == nil || rc.ReferencedBy.Total != 1 {
		t.Fatalf("reverse: %+v", rc)
	}
	ref := rc.ReferencedBy.Items[0]
	if ref.Kind != "Ingress" || ref.Group != "networking.k8s.io" || ref.Namespace != "a" || len(ref.Paths) != 1 || ref.Paths[0] != "spec.tls[].secretName" {
		t.Fatalf("TLS provenance: %+v", ref)
	}
	rc = Build(context.Background(), secret, Options{Tier: TierBasic, Provider: provider, AccessChecker: denyChecker{kind: "Ingress", group: "networking.k8s.io", namespace: "a"}})
	if rc.ReferencedBy != nil || !hasOmitted(rc.Omitted, "referencedBy") {
		t.Fatalf("denied ingress: %+v", rc)
	}
	rc = Build(context.Background(), ing, Options{Tier: TierBasic})
	if rc.IngressSummary == nil || len(rc.IngressSummary.TLSSecrets) != 1 || rc.IngressSummary.TLSSecrets[0].Namespace != "a" {
		t.Fatalf("forward: %+v", rc)
	}
}
