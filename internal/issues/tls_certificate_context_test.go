package issues

import (
	"fmt"
	"strings"
	"testing"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/issuesapi"
)

var tlsConsumers = []Ref{
	{Group: "networking.k8s.io", Kind: "Ingress", Namespace: "shop", Name: "api"},
	{Group: "networking.k8s.io", Kind: "Ingress", Namespace: "shop", Name: "web"},
}

func expiredTLSDetection(refs ...Ref) k8s.Detection {
	return k8s.Detection{
		Kind: "Secret", Namespace: "shop", Name: "shop-tls",
		Severity: "critical", Reason: k8s.TLSCertificateExpiredReason,
		Message:      "The certificate in this Secret expired 1d ago (2026-10-26T00:00:00Z), and an Ingress with a load-balancer address references it for TLS.",
		Fingerprint:  "tls-certificate-expired",
		OnsetUnknown: true,
		AffectedRefs: refs,
	}
}

var shopSecret = Ref{Kind: "Secret", Namespace: "shop", Name: "shop-tls"}

func TestExpiredTLSIssueShowsOnSecretAndIngresses(t *testing.T) {
	p := &fakeProvider{problems: []k8s.Detection{expiredTLSDetection(tlsConsumers...)}}
	got := Compose(p, Filters{Limit: NoLimit, Grouped: true})
	if len(got) != 1 {
		t.Fatalf("Compose = %+v, want one issue", got)
	}
	iss := got[0]
	if iss.Category != issuesapi.CategoryCertificateNotReady || iss.Severity != SeverityCritical || iss.Kind != "Secret" || !iss.OnsetUnknown {
		t.Fatalf("issue = %+v", iss)
	}
	if iss.DiagnosticContext == nil || len(iss.DiagnosticContext.Facts) != 1 || iss.DiagnosticContext.Facts[0].Type != factTLSCertificateConsumers || len(iss.DiagnosticContext.Facts[0].Refs) != 2 {
		t.Fatalf("diagnostic context = %+v", iss.DiagnosticContext)
	}
	readAll := RelatedIssueOptions{CanReadRelated: func(Ref) bool { return true }}
	for _, ref := range append([]Ref{shopSecret}, tlsConsumers...) {
		if got := RelatedIssues(p, readAll, ref.Group, ref.Kind, ref.Namespace, ref.Name); len(got) != 1 || got[0].Kind != "Secret" {
			t.Errorf("RelatedIssues(%s %s) = %+v, want the Secret issue", ref.Kind, ref.Name, got)
		}
	}
	notYet := expiredTLSDetection(tlsConsumers...)
	notYet.Reason = k8s.TLSCertificateNotYetValidReason
	if got := Compose(&fakeProvider{problems: []k8s.Detection{notYet}}, Filters{Limit: NoLimit}); len(got) != 1 || got[0].Category != issuesapi.CategoryCertificateNotReady || got[0].DiagnosticContext == nil {
		t.Fatalf("not-yet-valid issue = %+v", got)
	}
}

// A caller who reads the Ingresses but not the Secret gets nothing: no row,
// no match through the Ingress, no count.
func TestExpiredTLSIssueNeedsSecretRead(t *testing.T) {
	p := &fakeProvider{problems: []k8s.Detection{expiredTLSDetection(tlsConsumers...)}}
	noSecrets := func(ref Ref) bool { return ref.Kind != "Secret" }
	got, stats := ComposeWithStats(p, Filters{Limit: NoLimit, Grouped: true, CanReadSubject: noSecrets, CanReadRelated: noSecrets})
	if len(got) != 0 || stats.TotalMatched != 0 {
		t.Fatalf("caller without Secret access got %+v (total %d)", got, stats.TotalMatched)
	}
	opts := RelatedIssueOptions{CanReadSubject: noSecrets, CanReadRelated: noSecrets}
	if got := RelatedIssues(p, opts, "networking.k8s.io", "Ingress", "shop", "web"); len(got) != 0 {
		t.Fatalf("matched through the Ingress without Secret access: %+v", got)
	}
}

// A caller who reads the Secret but not the Ingresses keeps the row and loses
// the links; the text names no Ingress either way.
func TestExpiredTLSIssueRedactsUnreadableIngresses(t *testing.T) {
	p := &fakeProvider{problems: []k8s.Detection{expiredTLSDetection(tlsConsumers...)}}
	noIngresses := func(ref Ref) bool { return ref.Kind != "Ingress" }
	got := Compose(p, Filters{Limit: NoLimit, Grouped: true, CanReadSubject: noIngresses, CanReadRelated: noIngresses})
	if len(got) != 1 || len(got[0].AffectedRefs) != 0 || got[0].DiagnosticContext != nil {
		t.Fatalf("issue = %+v, want the row without Ingress links", got)
	}
	for _, text := range []string{got[0].Message, got[0].Cause, got[0].Action} {
		if strings.Contains(text, "web") || strings.Contains(text, "api") {
			t.Errorf("text names a consumer: %q", text)
		}
	}
	opts := RelatedIssueOptions{CanReadSubject: noIngresses, CanReadRelated: noIngresses}
	if got := RelatedIssues(p, opts, "", "Secret", "shop", "shop-tls"); len(got) != 1 {
		t.Fatalf("Secret lookup = %+v, want the issue", got)
	}
	onlyWeb := func(ref Ref) bool { return ref.Kind != "Ingress" || ref.Name == "web" }
	partial := Compose(p, Filters{Limit: NoLimit, CanReadRelated: onlyWeb})
	if len(partial) != 1 || len(partial[0].DiagnosticContext.Facts[0].Refs) != 1 || partial[0].DiagnosticContext.Facts[0].Refs[0].Name != "web" {
		t.Fatalf("partial redaction = %+v", partial)
	}
}

// Links are capped after unreadable consumers are dropped, so hidden ones
// neither crowd out a readable consumer nor show up as a truncation note.
func TestExpiredTLSIssueCapsLinksAfterRedaction(t *testing.T) {
	var refs []Ref
	for i := 0; i < maxAffectedRefLinks; i++ {
		refs = append(refs, Ref{Group: "networking.k8s.io", Kind: "Ingress", Namespace: "hidden", Name: fmt.Sprintf("ing-%02d", i)})
	}
	refs = append(refs, Ref{Group: "networking.k8s.io", Kind: "Ingress", Namespace: "shop", Name: "web"})
	p := &fakeProvider{problems: []k8s.Detection{expiredTLSDetection(refs...)}}
	if got := Compose(p, Filters{Limit: NoLimit}); !strings.HasSuffix(got[0].DiagnosticContext.Facts[0].Message, truncatedLinksNote) {
		t.Fatalf("unredacted fact = %+v, want the truncation note", got[0].DiagnosticContext.Facts[0])
	}
	shopOnly := func(ref Ref) bool { return ref.Kind != "Ingress" || ref.Namespace == "shop" }
	got := Compose(p, Filters{Limit: NoLimit, CanReadRelated: shopOnly})
	fact := got[0].DiagnosticContext.Facts[0]
	if len(fact.Refs) != 1 || fact.Refs[0].Name != "web" || strings.Contains(fact.Message, "Only the first") {
		t.Fatalf("redacted fact = %+v, want only web and no truncation note", fact)
	}
}

// kind=Ingress finds the Secret row through the Ingresses it affects, but
// not through Ingresses the caller can't read.
func TestExpiredTLSIssueMatchesAffectedKindFilter(t *testing.T) {
	p := &fakeProvider{problems: []k8s.Detection{expiredTLSDetection(tlsConsumers...)}}
	if got := Compose(p, Filters{Limit: NoLimit, Kinds: []string{"Ingress"}}); len(got) != 1 || got[0].Kind != "Secret" {
		t.Fatalf("kind=Ingress = %+v, want the Secret row", got)
	}
	noIngresses := func(ref Ref) bool { return ref.Kind != "Ingress" }
	if got := Compose(p, Filters{Limit: NoLimit, Kinds: []string{"Ingress"}, CanReadRelated: noIngresses}); len(got) != 0 {
		t.Fatalf("kind=Ingress without Ingress access = %+v, want none", got)
	}
}
