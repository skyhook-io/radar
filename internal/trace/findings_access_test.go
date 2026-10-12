package trace

import (
	"strings"
	"testing"
	"time"

	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/skyhook-io/radar/internal/issues"
	"github.com/skyhook-io/radar/internal/k8s"
)

// Hop findings follow the caller's issue access: a missing TLS Secret names only
// what the Ingress names, so it shows with namespace access, and a caller who
// can't open the Ingress gets no finding about it.
func TestHopFindingsUseTheCallersIssueAccess(t *testing.T) {
	ing := &networkingv1.Ingress{
		ObjectMeta: metav1.ObjectMeta{Name: "front", Namespace: "shop", CreationTimestamp: metav1.NewTime(time.Now().Add(-time.Hour))},
		Spec:       networkingv1.IngressSpec{TLS: []networkingv1.IngressTLS{{Hosts: []string{"shop.example.com"}, SecretName: "shop-tls"}}},
	}
	if err := k8s.InitTestResourceCache(fake.NewClientset(ing)); err != nil {
		t.Fatalf("InitTestResourceCache: %v", err)
	}
	ref := ResourceRef{Kind: "Ingress", Group: "networking.k8s.io", Namespace: "shop", Name: "front"}
	allowAll := IssueAccess{
		CanReadClusterScoped: func(string, string) bool { return true },
		CanReadSubject:       func(issues.Ref) bool { return true },
		CanReadEvidence:      func(issues.EvidenceRead) bool { return true },
	}
	denySecrets := allowAll
	denySecrets.CanReadEvidence = func(read issues.EvidenceRead) bool { return read.Resource != "secrets" }
	denyIngress := allowAll
	denyIngress.CanReadSubject = func(ref issues.Ref) bool { return ref.Kind != "Ingress" }

	hasTLS := func(access IssueAccess) bool {
		for _, f := range hopFindings(Deps{Issues: issues.NewCacheProvider(), IssueAccess: access}, ref) {
			if strings.Contains(f.Message, `Secret "shop-tls"`) {
				return true
			}
		}
		return false
	}
	if !hasTLS(allowAll) || !hasTLS(denySecrets) {
		t.Fatal("a caller who can open the Ingress lost the missing-TLS finding")
	}
	if hasTLS(denyIngress) {
		t.Fatal("a caller who can't open the Ingress got its finding")
	}
}
