package datum

import (
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"testing"
)

func TestConditionCertainty(t *testing.T) {
	for _, tc := range []struct {
		name, kind, want string
		conditions       []any
		generation       int64
	}{
		{"unreported", "DNSZone", "Unknown", nil, 1},
		{"acceptance alone", "DNSZone", "Unknown", []any{map[string]any{"type": "Accepted", "status": "True"}}, 1},
		{"programmed", "DNSZone", "Ready", []any{map[string]any{"type": "Accepted", "status": "True"}, map[string]any{"type": "Programmed", "status": "True"}}, 1},
		{"stale failure", "DNSZone", "Reconciling", []any{map[string]any{"type": "Programmed", "status": "False", "observedGeneration": int64(1)}}, 2},
		{"pending verification", "Domain", "Reconciling", []any{map[string]any{"type": "Verified", "status": "False", "reason": "PendingVerification"}}, 1},
		{"alternative method", "Domain", "Ready", []any{map[string]any{"type": "ValidDomain", "status": "True"}, map[string]any{"type": "Verified", "status": "True"}, map[string]any{"type": "VerifiedHTTP", "status": "False", "reason": "RecordNotFound"}}, 1},
		{"claimed hostname", "HTTPProxy", "Attention needed", []any{map[string]any{"type": "HostnamesInUse", "status": "True"}}, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u := &unstructured.Unstructured{Object: map[string]any{"kind": tc.kind, "metadata": map[string]any{"generation": tc.generation}, "status": map[string]any{"conditions": tc.conditions}}}
			if got := State(u); got != tc.want {
				t.Fatalf("state=%q, want %q", got, tc.want)
			}
		})
	}
}
func TestNestedHostnameFailure(t *testing.T) {
	u := &unstructured.Unstructured{Object: map[string]any{"kind": "HTTPProxy", "status": map[string]any{"hostnameStatuses": []any{map[string]any{"hostname": "bad.example", "conditions": []any{map[string]any{"type": "CertificateReady", "status": "False", "reason": "IssuerFailed"}}}}}}}
	failures := Failures(u)
	if len(failures) != 1 || failures[0].Scope != "bad.example" {
		t.Fatalf("failures=%+v", failures)
	}
}
func TestInstanceBackendNamesEndpointSlice(t *testing.T) {
	u := &unstructured.Unstructured{Object: map[string]any{"apiVersion": NetworkGroup + "/v1alpha", "kind": "HTTPProxy", "metadata": map[string]any{"namespace": "p"}, "spec": map[string]any{"rules": []any{map[string]any{"backends": []any{map[string]any{"instance": map[string]any{"name": "origin", "port": int64(443)}}}}}}}}
	refs := References(u)
	if len(refs) != 1 || refs[0].Ref.Kind != "EndpointSlice" || refs[0].Ref.Group != "discovery.k8s.io" {
		t.Fatalf("refs=%+v", refs)
	}
	if _, ok := Lookup("projectcontour.io", "HTTPProxy"); ok {
		t.Fatal("foreign HTTPProxy matched")
	}
	if HostMatches("", "example.test") {
		t.Fatal("empty domain inferred")
	}
}

func TestScopedFailuresPreserveDistinctEvidenceWithoutAggregateDuplicate(t *testing.T) {
	u := &unstructured.Unstructured{Object: map[string]any{"kind": "HTTPProxy", "status": map[string]any{"conditions": []any{map[string]any{"type": "CertificatesReady", "status": "False", "reason": "IssuerFailed"}}, "hostnameStatuses": []any{map[string]any{"hostname": "one.example", "conditions": []any{map[string]any{"type": "CertificateReady", "status": "False", "reason": "IssuerFailed"}}}, map[string]any{"hostname": "two.example", "conditions": []any{map[string]any{"type": "CertificateReady", "status": "False", "reason": "IssuerFailed"}}}}}}}
	if got := Failures(u); len(got) != 2 {
		t.Fatalf("failures=%+v", got)
	}
	unstructured.SetNestedSlice(u.Object, []any{map[string]any{"type": "CertificatesReady", "status": "False", "reason": "AnotherFailure"}}, "status", "conditions")
	if got := Failures(u); len(got) != 3 {
		t.Fatalf("distinct aggregate lost: %+v", got)
	}
}
func TestWorkloadObservedGenerationAndExpectedProgress(t *testing.T) {
	u := &unstructured.Unstructured{Object: map[string]any{"kind": "Workload", "metadata": map[string]any{"generation": int64(2)}, "status": map[string]any{"observedGeneration": int64(1), "conditions": []any{map[string]any{"type": "Available", "status": "False", "reason": "ProvisioningFailed"}}}}}
	if State(u) != "Reconciling" || len(Failures(u)) != 0 {
		t.Fatalf("stale workload=%s", State(u))
	}
	for _, reason := range []string{"ChallengeInProgress", "CertificatesPending", "RetryPending", "PendingEvaluation"} {
		unstructured.SetNestedField(u.Object, int64(2), "status", "observedGeneration")
		unstructured.SetNestedSlice(u.Object, []any{map[string]any{"type": "Available", "status": "False", "reason": reason}}, "status", "conditions")
		if State(u) != "Reconciling" || len(Failures(u)) != 0 {
			t.Fatalf("progress %s = %s", reason, State(u))
		}
	}
}
