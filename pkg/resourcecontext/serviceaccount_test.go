package resourcecontext

import (
	"context"
	"errors"
	"fmt"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"slices"
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

func TestBuild_SecretReferenceCapRetainsDeclaringAccount(t *testing.T) {
	sa := &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: "runner", Namespace: "prod"}, Secrets: []corev1.ObjectReference{{Name: "credentials"}}, ImagePullSecrets: []corev1.LocalObjectReference{{Name: "credentials"}}}
	provider := serviceAccountReferenceProvider{accounts: []*corev1.ServiceAccount{sa}}
	for i := 0; i < 25; i++ {
		provider.deploys = append(provider.deploys, &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("consumer-%02d", i), Namespace: "prod"}, Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{ImagePullSecrets: []corev1.LocalObjectReference{{Name: "credentials"}}}}}})
	}
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "credentials", Namespace: "prod"}}
	rc := Build(context.Background(), secret, Options{Provider: provider})
	if rc.ReferencedBy == nil || rc.ReferencedBy.Total != 26 || !rc.ReferencedBy.Truncated || len(rc.ReferencedBy.Items) != maxReferencedByItems {
		t.Fatalf("visible count or cap changed: %+v", rc.ReferencedBy)
	}
	account := -1
	for i, ref := range rc.ReferencedBy.Items {
		if ref.Kind == "ServiceAccount" {
			account = i
		}
		if len(ref.Paths) == 0 {
			t.Fatalf("selected reference lost provenance: %+v", ref)
		}
	}
	if account < 0 || !slices.Equal(rc.ReferencedBy.Items[account].Paths, []string{"secrets[]", "imagePullSecrets[]"}) {
		t.Fatalf("busy Secret hid account declaration roles: %+v", rc.ReferencedBy.Items)
	}
	if account != 1 || rc.ReferencedBy.Items[0].Name != "consumer-00" {
		t.Fatalf("kind round-robin changed display order: %+v", rc.ReferencedBy.Items)
	}
	if slices.Contains(rc.Omitted, OmittedField{Field: "referencedBy", Reason: OmittedBudgetExceeded}) {
		t.Fatalf("truncated flag was duplicated by an omission: %+v", rc.Omitted)
	}
	rc = Build(context.Background(), secret, Options{Provider: provider, AccessChecker: denyChecker{kind: "ServiceAccount", namespace: "prod"}})
	if rc.ReferencedBy.Total != 25 || len(rc.ReferencedBy.Items) != maxReferencedByItems || slices.ContainsFunc(rc.ReferencedBy.Items, func(ref ReferenceUse) bool { return ref.Kind == "ServiceAccount" }) {
		t.Fatalf("hidden account affected visible count or cap: %+v", rc.ReferencedBy)
	}
	rc = Build(context.Background(), secret, Options{Provider: provider, AccessChecker: denyChecker{group: "apps", kind: "Deployment", namespace: "prod"}})
	if rc.ReferencedBy.Total != 1 || rc.ReferencedBy.Truncated || len(rc.ReferencedBy.Items) != 1 || rc.ReferencedBy.Items[0].Kind != "ServiceAccount" {
		t.Fatalf("denied workloads consumed slots: %+v", rc.ReferencedBy)
	}
}
