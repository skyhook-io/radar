package resourcecontext

import (
	"context"
	"fmt"
	"slices"
	"testing"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/skyhook-io/radar/pkg/ownership"
	"github.com/skyhook-io/radar/pkg/readreason"
	"github.com/skyhook-io/radar/pkg/relations"
	"github.com/skyhook-io/radar/pkg/resourceid"
	"github.com/skyhook-io/radar/pkg/topology"
)

// secretDecider decides every Secret as decision and allows the rest.
type secretDecider struct{ decision ownership.Decision }

func (d secretDecider) CanRead(ctx context.Context, group, kind, namespace string) bool {
	return d.DecideRead(ctx, group, kind, namespace) == ownership.Allowed
}
func (d secretDecider) DecideRead(_ context.Context, _, kind, _ string) ownership.Decision {
	if kind == "Secret" {
		return d.decision
	}
	return ownership.Allowed
}

// Without a relationship answer to give a declared Secret its state, the
// summaries take the host's decision: a refusal reads denied, a check that
// couldn't be made unverified.
func TestDeclaredSecretsWithoutAnAnswerTakeTheHostsDecision(t *testing.T) {
	ctx := context.Background()
	ingress := &networkingv1.Ingress{ObjectMeta: metav1.ObjectMeta{Name: "entry", Namespace: "a"}, Spec: networkingv1.IngressSpec{TLS: []networkingv1.IngressTLS{{SecretName: "tls"}}}}
	account := &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: "runner", Namespace: "a"}, Secrets: []corev1.ObjectReference{{Name: "token"}}, ImagePullSecrets: []corev1.LocalObjectReference{{Name: "registry"}}}
	unanswered := topology.UnansweredRelationships(readreason.ClusterConnecting)
	for _, tc := range []struct {
		checker RefAccessChecker
		want    string
	}{
		{secretDecider{ownership.Allowed}, ""},
		{secretDecider{ownership.Denied}, topology.ResourceStateDenied},
		{secretDecider{ownership.Undecided}, topology.ResourceStateUnverified},
		{denyChecker{kind: "Secret", namespace: "a"}, topology.ResourceStateDenied},
	} {
		opts := Options{AccessChecker: tc.checker, RelationshipExtras: unanswered}
		rc := Build(ctx, ingress, opts)
		if s := rc.IngressSummary; s == nil || !slices.Equal(s.TLSSecrets, []ContextRef{{Kind: "Secret", Namespace: "a", Name: "tls", State: tc.want}}) {
			t.Errorf("%T %v: ingress summary = %+v", tc.checker, tc.checker, rc.IngressSummary)
		}
		rc = Build(ctx, account, opts)
		s := rc.ServiceAccountSummary
		if s == nil || !slices.Equal(s.SecretRefs, []ContextRef{{Kind: "Secret", Namespace: "a", Name: "token", State: tc.want}}) || !slices.Equal(s.ImagePullSecrets, []ContextRef{{Kind: "Secret", Namespace: "a", Name: "registry", State: tc.want}}) {
			t.Errorf("%T %v: account summary = %+v", tc.checker, tc.checker, s)
		}
	}
}

// An Ingress lists every TLS Secret it declares; a ServiceAccount's lists
// stay capped at maxReferencedByItems each, and say so.
func TestDeclaredSecretListBounds(t *testing.T) {
	ctx := context.Background()
	ingress := &networkingv1.Ingress{ObjectMeta: metav1.ObjectMeta{Name: "entry", Namespace: "a"}}
	account := &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: "runner", Namespace: "a"}}
	for i := range maxReferencedByItems + 1 {
		name := fmt.Sprintf("secret-%02d", i)
		ingress.Spec.TLS = append(ingress.Spec.TLS, networkingv1.IngressTLS{SecretName: name})
		account.Secrets = append(account.Secrets, corev1.ObjectReference{Name: name})
	}
	rc := Build(ctx, ingress, Options{})
	if rc.IngressSummary == nil || len(rc.IngressSummary.TLSSecrets) != maxReferencedByItems+1 || marksField(rc.Omitted, "ingressSummary.tlsSecrets") {
		t.Fatalf("ingress TLS Secrets capped: %d listed, omitted %+v", len(rc.IngressSummary.TLSSecrets), rc.Omitted)
	}
	rc = Build(ctx, account, Options{})
	if rc.ServiceAccountSummary == nil || len(rc.ServiceAccountSummary.SecretRefs) != maxReferencedByItems || !slices.Contains(rc.Omitted, OmittedField{Field: "serviceAccountSummary.secretRefs", Reason: OmittedBudgetExceeded}) {
		t.Fatalf("account Secrets past the cap: %+v, omitted %+v", rc.ServiceAccountSummary, rc.Omitted)
	}
}

// A declared Secret takes the state of the first declaration of it the
// relationship answer holds; one the answer doesn't hold takes the host's
// decision. The summary keeps the declared names' order.
func TestDeclaredRefsTakeTheFirstMatchingDeclaration(t *testing.T) {
	ingress := &networkingv1.Ingress{ObjectMeta: metav1.ObjectMeta{Name: "entry", Namespace: "a"}, Spec: networkingv1.IngressSpec{TLS: []networkingv1.IngressTLS{{SecretName: "c"}, {SecretName: "a"}, {SecretName: "b"}}}}
	secret := func(name string) resourceid.Ref { return resourceid.NewRef("", "Secret", "a", name) }
	extras := &topology.RelationshipExtras{Declarations: []relations.Declared{
		{Ref: secret("a"), State: relations.TargetPresent},
		{Ref: secret("a"), State: relations.TargetWithheld},
		{Ref: secret("b"), State: relations.TargetUnread, Gap: ownership.Gap{Kind: resourceid.GroupKind{Kind: "Secret"}, Namespace: "a", Reason: readreason.UserPermissionCheckFailed}},
	}}
	rc := Build(context.Background(), ingress, Options{RelationshipExtras: extras, AccessChecker: secretDecider{ownership.Denied}})
	want := []ContextRef{
		{Kind: "Secret", Namespace: "a", Name: "a"},
		{Kind: "Secret", Namespace: "a", Name: "b", State: topology.ResourceStateUnverified},
		{Kind: "Secret", Namespace: "a", Name: "c", State: topology.ResourceStateDenied},
	}
	if rc.IngressSummary == nil || !slices.Equal(rc.IngressSummary.TLSSecrets, want) {
		t.Fatalf("tls secrets = %+v, want %+v", rc.IngressSummary, want)
	}
}

func BenchmarkDeclaredRefsManyTLSSecrets(b *testing.B) {
	ingress := &networkingv1.Ingress{ObjectMeta: metav1.ObjectMeta{Name: "entry", Namespace: "a"}}
	extras := &topology.RelationshipExtras{}
	for i := range 5000 {
		name := fmt.Sprintf("tls-%04d", i)
		ingress.Spec.TLS = append(ingress.Spec.TLS, networkingv1.IngressTLS{SecretName: name})
		extras.Declarations = append(extras.Declarations, relations.Declared{Ref: resourceid.NewRef("", "Secret", "a", name), State: relations.TargetPresent})
	}
	b.ReportAllocs()
	for b.Loop() {
		Build(context.Background(), ingress, Options{RelationshipExtras: extras})
	}
}
