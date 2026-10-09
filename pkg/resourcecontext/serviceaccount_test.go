package resourcecontext

import (
	"context"
	"errors"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"testing"
)

type serviceAccountReferenceProvider struct {
	mockResourceProvider
	accounts []*corev1.ServiceAccount
	err      error
}

func (p serviceAccountReferenceProvider) ServiceAccounts() ([]*corev1.ServiceAccount, error) {
	return p.accounts, p.err
}

func TestBuild_ServiceAccountSecretRolesAndReverse(t *testing.T) {
	sa := &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: "runner", Namespace: "prod"}, Secrets: []corev1.ObjectReference{{Name: "credentials"}}, ImagePullSecrets: []corev1.LocalObjectReference{{Name: "credentials"}}}
	rc := Build(context.Background(), sa, Options{Tier: TierBasic})
	if rc.ServiceAccountSummary == nil || len(rc.ServiceAccountSummary.SecretRefs) != 1 || len(rc.ServiceAccountSummary.ImagePullSecrets) != 1 {
		t.Fatalf("roles: %+v", rc)
	}
	if rc.Uses != nil {
		t.Fatalf("SA declarations asserted as Pod use: %+v", rc.Uses)
	}
	other := sa.DeepCopy()
	other.Namespace = "other"
	provider := serviceAccountReferenceProvider{accounts: []*corev1.ServiceAccount{nil, other, sa}}
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "credentials", Namespace: "prod"}}
	rc = Build(context.Background(), secret, Options{Tier: TierBasic, Provider: provider})
	if rc.ReferencedBy == nil || rc.ReferencedBy.Total != 1 {
		t.Fatalf("reverse: %+v", rc)
	}
	ref := rc.ReferencedBy.Items[0]
	if ref.Kind != "ServiceAccount" || ref.Name != "runner" || ref.Namespace != "prod" || len(ref.Paths) != 2 {
		t.Fatalf("reverse roles: %+v", ref)
	}
	rc = Build(context.Background(), secret, Options{Tier: TierBasic, Provider: provider, AccessChecker: denyChecker{kind: "ServiceAccount", namespace: "prod"}})
	if rc.ReferencedBy != nil || !hasOmitted(rc.Omitted, "referencedBy") {
		t.Fatalf("denied SA: %+v", rc)
	}
	rc = Build(context.Background(), sa, Options{Tier: TierBasic, AccessChecker: denyChecker{kind: "Secret", namespace: "prod"}})
	if rc.ServiceAccountSummary != nil || !hasOmitted(rc.Omitted, "serviceAccountSummary.secretRefs") || !hasOmitted(rc.Omitted, "serviceAccountSummary.imagePullSecrets") {
		t.Fatalf("denied Secrets: %+v", rc)
	}
	custom := &unstructured.Unstructured{Object: map[string]interface{}{"apiVersion": "custom.example/v1", "kind": "Secret", "metadata": map[string]interface{}{"namespace": "prod", "name": "credentials"}}}
	if rc := Build(context.Background(), custom, Options{Tier: TierBasic, Provider: provider}); rc.ReferencedBy != nil {
		t.Fatalf("custom Secret got core refs: %+v", rc)
	}
	if rc := Build(context.Background(), &corev1.ServiceAccount{}, Options{Tier: TierBasic}); rc.ServiceAccountSummary != nil {
		t.Fatalf("projected tokens fabricated: %+v", rc)
	}
}

func TestBuild_ServiceAccountReferenceSourceUnavailable(t *testing.T) {
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "credentials", Namespace: "prod"}}
	rc := Build(context.Background(), secret, Options{Tier: TierBasic, Provider: serviceAccountReferenceProvider{err: errors.New("account source unavailable")}})
	found := false
	for _, field := range rc.Omitted {
		if field.Field == "referencedBy" && field.Reason == OmittedUnavailable {
			found = true
		}
	}
	if !found || rc.ReferencedBy != nil {
		t.Fatalf("unavailability hidden: %+v", rc)
	}
}
