package k8s

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	fakediscovery "k8s.io/client-go/discovery/fake"
	fakeclientset "k8s.io/client-go/kubernetes/fake"

	"github.com/skyhook-io/radar/pkg/k8score"
)

// discoveryWith installs a singleton discovery exposing exactly the given
// resource lists, and restores the previous one afterwards.
func discoveryWith(t *testing.T, lists ...*metav1.APIResourceList) {
	t.Helper()
	prev := resourceDiscovery
	t.Cleanup(func() { resourceDiscovery = prev })

	fakeDisc := fakeclientset.NewSimpleClientset().Discovery().(*fakediscovery.FakeDiscovery)
	fakeDisc.Resources = lists
	core, err := k8score.NewResourceDiscovery(fakeDisc)
	if err != nil {
		t.Fatalf("NewResourceDiscovery: %v", err)
	}
	resourceDiscovery = &ResourceDiscovery{ResourceDiscovery: core}
}

func kyvernoModernList() *metav1.APIResourceList {
	return &metav1.APIResourceList{
		GroupVersion: "policies.kyverno.io/v1",
		APIResources: []metav1.APIResource{
			{Name: "validatingpolicies", Kind: "ValidatingPolicy", Namespaced: false, Verbs: metav1.Verbs{"get", "list", "watch"}},
		},
	}
}

func coreOnlyList() *metav1.APIResourceList {
	return &metav1.APIResourceList{
		GroupVersion: "v1",
		APIResources: []metav1.APIResource{
			{Name: "pods", Kind: "Pod", Namespaced: true, Verbs: metav1.Verbs{"get", "list", "watch"}},
		},
	}
}

// Warmup only runs when the dynamic cache initialized, and that init failure
// is non-fatal — so "no decision recorded" is permanent on
// such a cluster, not transient. Reporting it as `warmup` made OmittedReason
// emit `policySummary.kyverno: cache_cold` on every resource of every
// non-Kyverno cluster, forever, which is the exact noise the silent
// not-installed path exists to prevent.
func TestGetPolicyReportStatus_UndecidedWithoutKyvernoStaysSilent(t *testing.T) {
	ResetPolicyReportIndex()
	discoveryWith(t, coreOnlyList())

	status := GetPolicyReportStatus()
	if status.Status != KyvernoStatusNotInstalled {
		t.Fatalf("status = %q, want %q", status.Status, KyvernoStatusNotInstalled)
	}
	if _, omit := status.OmittedReason(); omit {
		t.Error("a cluster with no Kyverno must not emit an omitted policy note")
	}
}

// "We couldn't look" is not "there's nothing to see". With no discovery we
// never established whether Kyverno exists, so concluding
// absence would be inferring a negative from a failed lookup — the silent
// emptiness this codebase treats as a bug. The reason code separates it from
// genuine absence, and only genuine absence is silent.
func TestGetPolicyReportStatus_UndecidedWithoutDiscoveryIsNotSilent(t *testing.T) {
	ResetPolicyReportIndex()
	prev := resourceDiscovery
	resourceDiscovery = nil
	t.Cleanup(func() { resourceDiscovery = prev })

	status := GetPolicyReportStatus()
	if status.ReasonCode != ReasonNoDiscovery {
		t.Fatalf("reasonCode = %q, want %q", status.ReasonCode, ReasonNoDiscovery)
	}
	reason, omit := status.OmittedReason()
	if !omit {
		t.Fatal("an unestablished lookup must not be reported as absence")
	}
	if string(reason) != "cache_cold" {
		t.Errorf("reason = %q, want cache_cold", reason)
	}
}

// ...while a cluster we DID look at and found no Kyverno on stays silent, so
// the fix for the spurious-omit bug is not undone.
func TestGetPolicyReportStatus_GenuineAbsenceStaysSilent(t *testing.T) {
	if _, omit := (PolicyReportStatus{Status: KyvernoStatusNotInstalled, ReasonCode: ReasonNotInstalled}).OmittedReason(); omit {
		t.Error("an established absence must stay silent")
	}
}

// The genuinely transient case must still report cold, or a Kyverno cluster
// mid-warmup would look like it has no violations.
func TestGetPolicyReportStatus_UndecidedWithKyvernoReportsCold(t *testing.T) {
	ResetPolicyReportIndex()
	discoveryWith(t, kyvernoModernList())

	status := GetPolicyReportStatus()
	if status.Status != KyvernoStatusWarmup {
		t.Fatalf("status = %q, want %q", status.Status, KyvernoStatusWarmup)
	}
	reason, omit := status.OmittedReason()
	if !omit {
		t.Fatal("a Kyverno cluster with a cold index must say so")
	}
	if string(reason) != "cache_cold" {
		t.Errorf("reason = %q, want cache_cold", reason)
	}
}
