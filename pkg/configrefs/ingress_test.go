package configrefs

import (
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"reflect"
	"testing"
)

func TestIngressTLSSecretReferences(t *testing.T) {
	ing := &networkingv1.Ingress{ObjectMeta: metav1.ObjectMeta{Namespace: "a"}, Spec: networkingv1.IngressSpec{TLS: []networkingv1.IngressTLS{{SecretName: "tls"}, {SecretName: "tls"}, {}}}}
	before := ing.DeepCopy()
	refs := IngressTLSSecretReferences(ing)
	if len(refs) != 1 || refs[0] != (Ref{Kind: "Secret", Namespace: "a", Name: "tls"}) {
		t.Fatalf("refs: %+v", refs)
	}
	if !reflect.DeepEqual(before, ing) {
		t.Fatal("input mutated")
	}
	if refs := IngressTLSSecretReferences(nil); refs != nil {
		t.Fatalf("nil: %+v", refs)
	}
}
