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

// A missing TLS Secret is learned from the Secret: its hop finding shows for a
// caller who can read Secrets (or with auth off) and not for one who can't.
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

	hasTLS := func(access IssueAccess) bool {
		for _, f := range hopFindings(Deps{Issues: issues.NewCacheProvider(), IssueAccess: access}, ref) {
			if strings.Contains(f.Message, `Secret "shop-tls"`) {
				return true
			}
		}
		return false
	}
	if !hasTLS(allowAll) {
		t.Fatal("a caller who can read Secrets lost the missing-TLS finding")
	}
	if hasTLS(denySecrets) {
		t.Fatal("a caller without Secret access got the missing-TLS finding")
	}
}
