package issues

import (
	"strings"
	"testing"

	issuesapi "github.com/skyhook-io/radar/pkg/issuesapi"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

var metalLBConfigurationStateGVR = schema.GroupVersionResource{Group: MetalLBGroup, Version: "v1beta1", Resource: "configurationstates"}

// The errorSummary MetalLB v0.16.2 wrote for a BGPPeer naming a missing password Secret.
const missingSecretSummary = "configuration error: parsing peer router-secret secret ref not found for peer config \"metallb-system\"/\"router-secret\"\nfailed to parse peer router-secret password secret"

// Shaped like the ConfigurationStates MetalLB v0.16 writes: "controller" for
// the controller and "speaker-<node>" for each speaker, labelled by component.
func metalLBConfigurationState(name, component, node, result, summary string) *unstructured.Unstructured {
	labels := map[string]any{metalLBComponentLabel: component}
	if node != "" {
		labels[metalLBNodeLabel] = node
	}
	status := map[string]any{
		"result": result,
		"conditions": []any{map[string]any{
			"type": "configReconcilerValid", "status": "True", "reason": "Reconciled",
			"lastTransitionTime": "2026-11-20T10:00:00Z",
		}},
	}
	if summary != "" {
		status["errorSummary"] = summary
		status["conditions"] = []any{map[string]any{
			"type": "configReconcilerValid", "status": "False", "reason": "ConfigurationError",
			"message": summary, "lastTransitionTime": "2026-11-20T10:00:00Z",
		}}
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "metallb.io/v1beta1", "kind": "ConfigurationState",
		"metadata": map[string]any{"name": name, "namespace": "metallb-system", "labels": labels, "creationTimestamp": "2026-11-20T09:00:00Z"},
		"status":   status,
	}}
}

func TestMetalLBConfigurationIssuesGroupsSpeakers(t *testing.T) {
	items := []*unstructured.Unstructured{
		metalLBConfigurationState("controller", "controller", "", "Valid", ""),
		metalLBConfigurationState("speaker-worker", "speaker", "worker", "Invalid", missingSecretSummary),
		metalLBConfigurationState("speaker-control-plane", "speaker", "control-plane", "Invalid", missingSecretSummary),
		metalLBConfigurationState("speaker-worker2", "speaker", "worker2", "Invalid", missingSecretSummary),
	}
	got := MetalLBConfigurationIssues(metalLBConfigurationStateGVR, items, nil)
	if len(got) != 1 {
		t.Fatalf("got %d issues, want one per distinct rejection: %+v", len(got), got)
	}
	iss := got[0]
	if iss.Reason != ReasonMetalLBConfigurationInvalid || iss.Severity != SeverityWarning || iss.Kind != "ConfigurationState" || iss.Group != MetalLBGroup ||
		iss.Namespace != "metallb-system" || iss.Name != "speaker-control-plane" {
		t.Fatalf("got %+v", iss)
	}
	oneLine := `parsing peer router-secret secret ref not found for peer config "metallb-system"/"router-secret"; failed to parse peer router-secret password secret`
	for _, want := range []string{"speaker on node control-plane", oneLine, "Also rejected by 2 other speakers"} {
		if !strings.Contains(iss.Message, want) {
			t.Fatalf("message %q does not contain %q", iss.Message, want)
		}
	}
	if iss.Category != issuesapi.CategoryOperatorConditionFail {
		t.Fatalf("category = %q", iss.Category)
	}
	// The reconciler rewrites the condition time on every pass.
	if !iss.OnsetUnknown || !iss.FirstSeen.IsZero() {
		t.Fatalf("onset = %v (unknown %v), want unknown", iss.FirstSeen, iss.OnsetUnknown)
	}
}

func TestMetalLBConfigurationIssuesPrefersController(t *testing.T) {
	const summary = "configuration error: parsing ipaddresspool pool-a: CIDR \"172.18.255.0/24\" overlaps with CIDR \"172.18.255.200/29\" in pool pool-b"
	items := []*unstructured.Unstructured{
		metalLBConfigurationState("speaker-a", "speaker", "a", "Invalid", summary),
		metalLBConfigurationState("controller", "controller", "", "Invalid", summary),
	}
	got := MetalLBConfigurationIssues(metalLBConfigurationStateGVR, items, nil)
	if len(got) != 1 || got[0].Name != "controller" {
		t.Fatalf("got %+v", got)
	}
	if !strings.HasPrefix(got[0].Message, "The MetalLB controller rejected") || !strings.HasSuffix(got[0].Message, "Also rejected by 1 other speaker") {
		t.Fatalf("message = %q", got[0].Message)
	}
}

func TestMetalLBConfigurationIssuesSeparatesDistinctRejections(t *testing.T) {
	items := []*unstructured.Unstructured{
		metalLBConfigurationState("speaker-a", "speaker", "a", "Invalid", missingSecretSummary),
		metalLBConfigurationState("speaker-b", "speaker", "b", "Invalid", "configuration error: parsing peer peer-b: peer peer-b referencing non existing bfd profile missing"),
	}
	got := MetalLBConfigurationIssues(metalLBConfigurationStateGVR, items, nil)
	if len(got) != 2 {
		t.Fatalf("got %d issues, want 2: %+v", len(got), got)
	}
	for _, iss := range got {
		if strings.Contains(iss.Message, "Also rejected") {
			t.Fatalf("a rejection only one speaker reports counts others: %q", iss.Message)
		}
	}
}

func TestMetalLBConfigurationIssuesIgnoresAcceptedAndUnknown(t *testing.T) {
	deleting := metalLBConfigurationState("speaker-gone", "speaker", "gone", "Invalid", missingSecretSummary)
	now := metav1.Now()
	deleting.SetDeletionTimestamp(&now)
	noStatus := metalLBConfigurationState("speaker-new", "speaker", "new", "", "")
	unstructured.RemoveNestedField(noStatus.Object, "status")
	owned := metalLBConfigurationState("speaker-owned", "speaker", "owned", "Invalid", missingSecretSummary)
	items := []*unstructured.Unstructured{
		metalLBConfigurationState("controller", "controller", "", "Valid", ""),
		// A component that has not reported yet, or whose only failure was
		// not a parse error, reads Unknown or Valid, not a rejection.
		metalLBConfigurationState("speaker-a", "speaker", "a", "Unknown", ""),
		noStatus,
		deleting,
		owned,
	}
	ownedSubjects := map[string]bool{resourceKey(MetalLBGroup, "ConfigurationState", "metallb-system", "speaker-owned"): true}
	if got := MetalLBConfigurationIssues(metalLBConfigurationStateGVR, items, ownedSubjects); len(got) != 0 {
		t.Fatalf("unexpected issues: %+v", got)
	}
}

func TestMetalLBConfigurationIssuesWithoutLabelsOrSummary(t *testing.T) {
	u := metalLBConfigurationState("custom", "", "", "Invalid", "")
	u.SetLabels(nil)
	got := MetalLBConfigurationIssues(metalLBConfigurationStateGVR, []*unstructured.Unstructured{u}, nil)
	if len(got) != 1 || got[0].Message != "MetalLB component custom rejected the MetalLB configuration and keeps running the last configuration it accepted, if any" {
		t.Fatalf("got %+v", got)
	}
}

func TestGenericCRDPassReportsMetalLBRejectionOnce(t *testing.T) {
	p := &fakeProvider{
		dynamic: map[schema.GroupVersionResource][]*unstructured.Unstructured{metalLBConfigurationStateGVR: {
			metalLBConfigurationState("controller", "controller", "", "Valid", ""),
			metalLBConfigurationState("speaker-a", "speaker", "a", "Invalid", missingSecretSummary),
			metalLBConfigurationState("speaker-b", "speaker", "b", "Invalid", missingSecretSummary),
		}},
		kinds:      map[schema.GroupVersionResource]string{metalLBConfigurationStateGVR: "ConfigurationState"},
		namespaced: map[schema.GroupVersionResource]bool{metalLBConfigurationStateGVR: true},
	}
	got := detectGenericCRDIssues(p, Filters{}, nil, nil)
	if len(got) != 1 || got[0].Reason != ReasonMetalLBConfigurationInvalid || got[0].Name != "speaker-a" {
		t.Fatalf("got %+v", got)
	}
}
