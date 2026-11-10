package issues

import (
	"strings"
	"testing"
	"time"

	"github.com/skyhook-io/radar/pkg/issuesapi"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func renewalCertificate(now time.Time) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "cert-manager.io/v1", "kind": "Certificate",
		"metadata": map[string]any{"name": "web-tls", "namespace": "shop", "uid": "certificate-one", "generation": int64(1), "creationTimestamp": now.Add(-60 * 24 * time.Hour).Format(time.RFC3339)},
		"spec":     map[string]any{"secretName": "web-tls", "issuerRef": map[string]any{"name": "issuer"}},
		"status": map[string]any{
			"notBefore": now.Add(-60 * 24 * time.Hour).Format(time.RFC3339), "notAfter": now.Add(10 * 24 * time.Hour).Format(time.RFC3339),
			"renewalTime": now.Add(-24 * time.Hour).Format(time.RFC3339), "lastFailureTime": now.Add(-time.Hour).Format(time.RFC3339), "failedIssuanceAttempts": int64(2),
			"conditions": []any{
				map[string]any{"type": "Ready", "status": "True", "observedGeneration": int64(1)},
				map[string]any{"type": "Issuing", "status": "False", "observedGeneration": int64(1), "reason": "Failed"},
			},
		},
	}}
}

func TestCertificateRenewalFailureEvidence(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	gvr := schema.GroupVersionResource{Group: "cert-manager.io", Version: "v1", Resource: "certificates"}
	for _, tc := range []struct {
		name   string
		mutate func(*unstructured.Unstructured)
		want   bool
		retry  bool
	}{
		{name: "failed scheduled renewal", want: true},
		{name: "retry remains visible", want: true, retry: true, mutate: func(u *unstructured.Unstructured) {
			u.Object["status"].(map[string]any)["conditions"].([]any)[1].(map[string]any)["status"] = "True"
		}},
		{name: "successful renewal", mutate: func(u *unstructured.Unstructured) {
			unstructured.RemoveNestedField(u.Object, "status", "failedIssuanceAttempts")
			unstructured.RemoveNestedField(u.Object, "status", "lastFailureTime")
		}},
		{name: "zero failures", mutate: func(u *unstructured.Unstructured) {
			unstructured.SetNestedField(u.Object, int64(0), "status", "failedIssuanceAttempts")
		}},
		{name: "malformed counter", mutate: func(u *unstructured.Unstructured) {
			unstructured.SetNestedField(u.Object, "2", "status", "failedIssuanceAttempts")
		}},
		{name: "disabled", mutate: func(u *unstructured.Unstructured) {
			unstructured.SetNestedField(u.Object, "Disabled", "spec", "renewal", "policy")
		}},
		{name: "deleting", mutate: func(u *unstructured.Unstructured) { v := metav1.NewTime(now); u.SetDeletionTimestamp(&v) }},
		{name: "stale generation", mutate: func(u *unstructured.Unstructured) { u.SetGeneration(2) }},
		{name: "missing generation", mutate: func(u *unstructured.Unstructured) { u.SetGeneration(0) }},
		{name: "missing observed generation", mutate: func(u *unstructured.Unstructured) {
			delete(u.Object["status"].(map[string]any)["conditions"].([]any)[1].(map[string]any), "observedGeneration")
		}},
		{name: "ready unknown", mutate: func(u *unstructured.Unstructured) {
			u.Object["status"].(map[string]any)["conditions"].([]any)[0].(map[string]any)["status"] = "Unknown"
		}},
		{name: "no issuing condition", mutate: func(u *unstructured.Unstructured) {
			u.Object["status"].(map[string]any)["conditions"] = u.Object["status"].(map[string]any)["conditions"].([]any)[:1]
		}},
		{name: "issuing unknown", mutate: func(u *unstructured.Unstructured) {
			u.Object["status"].(map[string]any)["conditions"].([]any)[1].(map[string]any)["status"] = "Unknown"
		}},
		{name: "renewal due after expiry is contradictory", mutate: func(u *unstructured.Unstructured) {
			unstructured.SetNestedField(u.Object, now.Add(-48*time.Hour).Format(time.RFC3339), "status", "notAfter")
		}},
		{name: "future failure", mutate: func(u *unstructured.Unstructured) {
			unstructured.SetNestedField(u.Object, now.Add(time.Minute).Format(time.RFC3339), "status", "lastFailureTime")
		}},
		{name: "early reissue", mutate: func(u *unstructured.Unstructured) {
			unstructured.SetNestedField(u.Object, now.Add(time.Hour).Format(time.RFC3339), "status", "renewalTime")
		}},
		{name: "early failure remains early after due time", mutate: func(u *unstructured.Unstructured) {
			unstructured.SetNestedField(u.Object, now.Add(-time.Minute).Format(time.RFC3339), "status", "renewalTime")
		}},
		{name: "retry failed after due", want: true, mutate: func(u *unstructured.Unstructured) {
			unstructured.SetNestedField(u.Object, now.Add(-time.Minute).Format(time.RFC3339), "status", "renewalTime")
			unstructured.SetNestedField(u.Object, now.Format(time.RFC3339), "status", "lastFailureTime")
		}},
		{name: "failure before current certificate", mutate: func(u *unstructured.Unstructured) {
			unstructured.SetNestedField(u.Object, now.Add(-time.Minute).Format(time.RFC3339), "status", "notBefore")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u := renewalCertificate(now)
			if tc.mutate != nil {
				tc.mutate(u)
			}
			got := detectCertificateRenewalFailure(gvr, u, now)
			if !tc.want {
				if len(got) != 0 {
					t.Fatalf("unexpected finding: %+v", got)
				}
				return
			}
			if len(got) != 1 {
				t.Fatalf("expected one finding: %+v", got)
			}
			issue := got[0]
			if issue.Category != issuesapi.CategoryCertificateRenewalFailed || issue.Severity != SeverityWarning || issue.CategoryGroup != issuesapi.GroupSecurity {
				t.Fatalf("classification: %+v", issue)
			}
			if !strings.Contains(issue.Message, "cert-manager reports Ready") || !strings.Contains(issue.Message, "10d") || !strings.Contains(issue.Message, "failed issuance attempts") || strings.Contains(issue.Message, "retry is in progress") != tc.retry {
				t.Fatalf("message: %s", issue.Message)
			}
			if !issue.OnsetUnknown || !issue.FirstSeen.IsZero() {
				t.Fatalf("latest failure was used as episode onset: %+v", issue)
			}
			if issue.DiagnosticContext != nil || len(issue.AffectedRefs) != 0 {
				t.Fatalf("must not infer consumer outage: %+v", issue)
			}
		})
	}
	for _, field := range []string{"notBefore", "notAfter", "renewalTime", "lastFailureTime"} {
		for _, value := range []string{"", "invalid"} {
			t.Run(field+value, func(t *testing.T) {
				u := renewalCertificate(now)
				unstructured.SetNestedField(u.Object, value, "status", field)
				if got := detectCertificateRenewalFailure(gvr, u, now); len(got) != 0 {
					t.Fatalf("malformed evidence yielded finding: %+v", got)
				}
			})
		}
	}
}

func TestCertificateRenewalFailureComposition(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	gvr := schema.GroupVersionResource{Group: "cert-manager.io", Version: "v1", Resource: "certificates"}
	u := renewalCertificate(now)
	p := &fakeProvider{dynamic: map[schema.GroupVersionResource][]*unstructured.Unstructured{gvr: {u}}, kinds: map[schema.GroupVersionResource]string{gvr: "Certificate"}, namespaced: map[schema.GroupVersionResource]bool{gvr: true}}
	for _, grouped := range []bool{false, true} {
		got := Compose(p, Filters{Namespaces: []string{"shop"}, Grouped: grouped})
		if len(got) != 1 || got[0].Category != issuesapi.CategoryCertificateRenewalFailed {
			t.Fatalf("grouped=%v: %+v", grouped, got)
		}
		id := got[0].ID
		unstructured.SetNestedField(u.Object, int64(3), "status", "failedIssuanceAttempts")
		unstructured.SetNestedField(u.Object, now.Format(time.RFC3339), "status", "lastFailureTime")
		got = Compose(p, Filters{Namespaces: []string{"shop"}, Grouped: grouped})
		if len(got) != 1 || got[0].ID != id {
			t.Fatalf("retry changed identity: %+v", got)
		}
	}
	if got := Compose(p, Filters{Namespaces: []string{"shop"}, CanReadSubject: func(Ref) bool { return false }}); len(got) != 0 {
		t.Fatalf("denied subject leaked: %+v", got)
	}
	if got := Compose(strimziScopedProvider{p}, Filters{Namespaces: []string{"other"}}); len(got) != 0 {
		t.Fatalf("namespace leak: %+v", got)
	}
	conditions := u.Object["status"].(map[string]any)["conditions"].([]any)
	conditions[0].(map[string]any)["status"] = "False"
	conditions[0].(map[string]any)["reason"] = "DoesNotExist"
	got := Compose(p, Filters{Namespaces: []string{"shop"}})
	if len(got) != 1 || got[0].Category != issuesapi.CategoryCertificateNotReady {
		t.Fatalf("generic not-ready failure lost: %+v", got)
	}
	conditions[0].(map[string]any)["status"] = "True"
	for _, tc := range []struct{ group, kind string }{{"example.com", "Certificate"}, {"cert-manager.io", "CertificateRequest"}, {"acme.cert-manager.io", "Certificate"}} {
		other := schema.GroupVersionResource{Group: tc.group, Version: "v1", Resource: "certificates"}
		p.dynamic = map[schema.GroupVersionResource][]*unstructured.Unstructured{other: {u}}
		p.kinds = map[schema.GroupVersionResource]string{other: tc.kind}
		p.namespaced = map[schema.GroupVersionResource]bool{other: true}
		if got := Compose(p, Filters{Namespaces: []string{"shop"}}); len(got) != 0 {
			t.Fatalf("claimed unrelated group/kind %s/%s: %+v", tc.group, tc.kind, got)
		}
	}
}

func TestCertificateRenewalFailureExpiryAndMessage(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	gvr := schema.GroupVersionResource{Group: "cert-manager.io", Version: "v1", Resource: "certificates"}
	u := renewalCertificate(now)
	condition := u.Object["status"].(map[string]any)["conditions"].([]any)[1].(map[string]any)
	condition["message"] = "DNS challenge rejected"
	before := detectCertificateRenewalFailure(gvr, u, now)[0]
	if !strings.Contains(before.Message, "DNS challenge rejected") || !strings.Contains(before.Message, now.Add(-time.Hour).Format(time.RFC3339)) {
		t.Fatal(before.Message)
	}
	for _, expiry := range []time.Time{now, now.Add(-time.Minute)} {
		unstructured.SetNestedField(u.Object, expiry.Format(time.RFC3339), "status", "notAfter")
		got := detectCertificateRenewalFailure(gvr, u, now)
		if len(got) != 1 || got[0].Severity != SeverityCritical || !strings.Contains(got[0].Message, "recorded certificate expired") || got[0].ID != before.ID {
			t.Fatalf("expiry lost finding or identity: %+v", got)
		}
	}
	condition["message"] = strings.Repeat("界", 600)
	got := detectCertificateRenewalFailure(gvr, u, now)[0]
	if !strings.Contains(got.Message, strings.Repeat("界", 500)+"…") || strings.Contains(got.Message, strings.Repeat("界", 501)) {
		t.Fatal("controller detail not bounded on rune boundary")
	}
	condition["status"] = "True"
	condition["message"] = "Issuing renewal"
	got = detectCertificateRenewalFailure(gvr, u, now)[0]
	if strings.Contains(got.Message, "Controller report:") || !strings.Contains(got.Message, "retry is in progress") {
		t.Fatal(got.Message)
	}
}
