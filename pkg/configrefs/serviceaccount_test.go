package configrefs

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"reflect"
	"testing"
)

func TestServiceAccountSecretReferences(t *testing.T) {
	sa := &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: "runner", Namespace: "prod"}, Secrets: []corev1.ObjectReference{
		{Name: "token"}, {Name: "token"}, {Name: "pull", Namespace: "prod", APIVersion: "v1", Kind: "Secret"},
		{Name: "other-ns", Namespace: "other"}, {Name: "custom", Kind: "Secret", APIVersion: "custom.example/v1"}, {Name: "wrong-kind", Kind: "ConfigMap"}, {},
	}, ImagePullSecrets: []corev1.LocalObjectReference{{Name: "pull"}, {Name: "pull"}, {}}}
	before := sa.DeepCopy()
	refs := ServiceAccountSecretReferences(sa)
	if len(refs) != 3 {
		t.Fatalf("refs: %+v", refs)
	}
	for _, ref := range refs {
		if ref.Kind != "Secret" || ref.Namespace != "prod" {
			t.Fatalf("identity: %+v", ref)
		}
	}
	if refs[0].Path != "secrets[]" || refs[0].ImagePull || refs[2].Path != "imagePullSecrets[]" || !refs[2].ImagePull {
		t.Fatalf("roles: %+v", refs)
	}
	if !reflect.DeepEqual(sa, before) {
		t.Fatal("input mutated")
	}
	if refs := ServiceAccountSecretReferences(nil); refs != nil {
		t.Fatalf("nil input: %+v", refs)
	}
	if refs := ServiceAccountSecretReferences(&corev1.ServiceAccount{}); len(refs) != 0 {
		t.Fatalf("manufactured token Secret: %+v", refs)
	}
}
