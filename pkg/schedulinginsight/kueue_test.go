package schedulinginsight

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/yaml"

	"github.com/skyhook-io/radar/pkg/resourcecontext"
)

func TestForResourceRequiresExactKueueWorkloadGVK(t *testing.T) {
	base := loadFixture(t, "waiting-granular.yaml")
	tests := []struct {
		name       string
		apiVersion string
		kind       string
		want       bool
	}{
		{name: "supported", apiVersion: "kueue.x-k8s.io/v1beta2", kind: "Workload", want: true},
		{name: "old served version is not a compatibility fallback", apiVersion: "kueue.x-k8s.io/v1beta1", kind: "Workload"},
		{name: "same kind in another group", apiVersion: "example.io/v1beta2", kind: "Workload"},
		{name: "other Kueue kind", apiVersion: "kueue.x-k8s.io/v1beta2", kind: "LocalQueue"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			u := base.DeepCopy()
			u.SetAPIVersion(test.apiVersion)
			u.SetKind(test.kind)
			if got := ForResource(u, resourcecontext.TierBasic); (got != nil) != test.want {
				t.Fatalf("ForResource() = %+v, want supported=%t", got, test.want)
			}
		})
	}

	if got := ForResource(&corev1.Pod{}, resourcecontext.TierBasic); got != nil {
		t.Fatalf("typed lookalike produced scheduling summary: %+v", got)
	}
}

func TestForResourceWaitingPreservesAuthoritativeCondition(t *testing.T) {
	summary := ForResource(loadFixture(t, "waiting-granular.yaml"), resourcecontext.TierBasic)
	if summary == nil {
		t.Fatal("missing summary")
	}
	if summary.Controller != "kueue" || summary.Stage != resourcecontext.SchedulingWaiting {
		t.Fatalf("identity/stage = %q/%q", summary.Controller, summary.Stage)
	}
	if summary.Queue == nil || *summary.Queue != (resourcecontext.ContextRef{
		Kind: "LocalQueue", Group: "kueue.x-k8s.io", Namespace: "training", Name: "gpu",
	}) {
		t.Fatalf("queue = %+v", summary.Queue)
	}
	if summary.Blocker == nil {
		t.Fatal("missing blocker")
	}
	if summary.Blocker.Condition != "QuotaReserved" || summary.Blocker.Status != "False" || summary.Blocker.Reason != "WaitingForQuota" {
		t.Fatalf("blocker did not preserve controller condition: %+v", summary.Blocker)
	}
	if summary.Blocker.ReasonPrecision != resourcecontext.SchedulingReasonGranular {
		t.Fatalf("reason precision = %q, want granular", summary.Blocker.ReasonPrecision)
	}
	if summary.Blocker.Message != "" || summary.Blocker.LastTransitionTime != "" {
		t.Fatalf("basic tier leaked diagnostic detail: %+v", summary.Blocker)
	}
	wire, err := json.Marshal(summary)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(wire), "severity") {
		t.Fatalf("normal waiting was projected as an Issue-like failure: %s", wire)
	}
}

func TestForResourceReasonPrecisionDoesNotGuessFeatureGateState(t *testing.T) {
	base := loadFixture(t, "waiting-granular.yaml")
	tests := []struct {
		name      string
		reason    string
		want      resourcecontext.SchedulingReasonPrecision
		deleteKey bool
	}{
		{name: "granular released reason", reason: "WaitingForQuota", want: resourcecontext.SchedulingReasonGranular},
		{name: "admission gated is granular", reason: "AdmissionGated", want: resourcecontext.SchedulingReasonGranular},
		{name: "deprecated coarse reason", reason: "Pending", want: resourcecontext.SchedulingReasonCoarse},
		{name: "unknown future reason has no invented precision", reason: "FutureGranularReason"},
		{name: "missing reason stays unknown", deleteKey: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			u := base.DeepCopy()
			conditions, _, _ := unstructured.NestedSlice(u.Object, "status", "conditions")
			condition := conditions[0].(map[string]any)
			if test.deleteKey {
				delete(condition, "reason")
			} else {
				condition["reason"] = test.reason
			}
			if err := unstructured.SetNestedSlice(u.Object, conditions, "status", "conditions"); err != nil {
				t.Fatal(err)
			}
			got := ForResource(u, resourcecontext.TierBasic)
			if got.Blocker.ReasonPrecision != test.want {
				t.Fatalf("precision = %q, want %q", got.Blocker.ReasonPrecision, test.want)
			}
		})
	}
}

func TestForResourceAdmissionCheckProjectionByTier(t *testing.T) {
	u := loadFixture(t, "admission-check-rejected.yaml")
	basic := ForResource(u, resourcecontext.TierBasic)
	diagnostic := ForResource(u, resourcecontext.TierDiagnostic)
	if basic.Stage != resourcecontext.SchedulingExternalCheck {
		t.Fatalf("basic stage = %q", basic.Stage)
	}
	if basic.Blocker == nil || basic.Blocker.Reason != "UnsatisfiedAdmissionChecks" || basic.Blocker.ReasonPrecision != resourcecontext.SchedulingReasonGranular {
		t.Fatalf("basic blocker = %+v", basic.Blocker)
	}
	if basic.ClusterQueue == nil || basic.ClusterQueue.Name != "gpu-team" {
		t.Fatalf("cluster queue = %+v", basic.ClusterQueue)
	}
	if got, want := namesOf(basic.Flavors), []string{"a10", "on-demand"}; fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("flavors = %v, want %v", got, want)
	}
	if got, want := checkNames(basic.AdmissionChecks), []string{"capacity", "policy"}; fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("checks = %v, want %v", got, want)
	}
	if basic.AdmissionChecks[0].State != "Rejected" || basic.AdmissionChecks[0].Message != "" || basic.AdmissionChecks[0].RetryCount != nil {
		t.Fatalf("basic check = %+v", basic.AdmissionChecks[0])
	}
	if diagnostic.Blocker.Message != "Admission checks are not ready" || diagnostic.Blocker.LastTransitionTime != "2026-08-30T10:02:00Z" {
		t.Fatalf("diagnostic blocker = %+v", diagnostic.Blocker)
	}
	capacity := diagnostic.AdmissionChecks[0]
	if capacity.Message == "" || capacity.RetryCount == nil || *capacity.RetryCount != 2 || capacity.RequeueAfterSeconds == nil || *capacity.RequeueAfterSeconds != 60 {
		t.Fatalf("diagnostic capacity check = %+v", capacity)
	}
	if diagnostic.Requeue == nil || diagnostic.Requeue.Count == nil || *diagnostic.Requeue.Count != 2 || diagnostic.Requeue.At != "2026-08-30T10:04:00Z" {
		t.Fatalf("requeue = %+v", diagnostic.Requeue)
	}
}

func TestWorkloadStagePrecedenceTracksCurrentState(t *testing.T) {
	condition := func(conditionType, status, reason string) workloadCondition {
		return workloadCondition{Type: conditionType, Status: status, Reason: reason}
	}
	tests := []struct {
		name       string
		conditions map[string]workloadCondition
		wantStage  resourcecontext.SchedulingStage
		wantBlock  string
	}{
		{
			name: "finished overrides active admission",
			conditions: map[string]workloadCondition{
				"Finished":      condition("Finished", "True", "Failed"),
				"Admitted":      condition("Admitted", "True", "Admitted"),
				"QuotaReserved": condition("QuotaReserved", "True", "QuotaReserved"),
			},
			wantStage: resourcecontext.SchedulingFailed,
		},
		{
			name: "successful finish is terminal not blocked",
			conditions: map[string]workloadCondition{
				"Finished": condition("Finished", "True", "Succeeded"),
			},
			wantStage: resourcecontext.SchedulingFinished,
		},
		{
			name: "failed start is terminal failure despite active admission",
			conditions: map[string]workloadCondition{
				"Finished":  condition("Finished", "True", "FailedToStart"),
				"Admitted":  condition("Admitted", "True", "Admitted"),
				"PodsReady": condition("PodsReady", "True", "Started"),
			},
			wantStage: resourcecontext.SchedulingFailed,
		},
		{
			name: "out of sync finish is terminal failure not blocked",
			conditions: map[string]workloadCondition{
				"Finished": condition("Finished", "True", "OutOfSync"),
			},
			wantStage: resourcecontext.SchedulingFailed,
		},
		{
			name: "missing owner finish is terminal failure not blocked",
			conditions: map[string]workloadCondition{
				"Finished": condition("Finished", "True", "OwnerNotFound"),
			},
			wantStage: resourcecontext.SchedulingFailed,
		},
		{
			name: "active admission overrides historical requeue and eviction",
			conditions: map[string]workloadCondition{
				"Admitted":      condition("Admitted", "True", "Admitted"),
				"QuotaReserved": condition("QuotaReserved", "True", "QuotaReserved"),
				"PodsReady":     condition("PodsReady", "True", "Started"),
				"Requeued":      condition("Requeued", "True", "BackoffFinished"),
				"Evicted":       condition("Evicted", "True", "PodsReadyTimeout"),
			},
			wantStage: resourcecontext.SchedulingRunning,
		},
		{
			name: "requeued is current after historical preemption",
			conditions: map[string]workloadCondition{
				"Requeued":  condition("Requeued", "True", "BackoffFinished"),
				"Preempted": condition("Preempted", "True", "InClusterQueue"),
				"Evicted":   condition("Evicted", "True", "Preempted"),
			},
			wantStage: resourcecontext.SchedulingRequeued,
		},
		{
			name: "preemption is more specific than eviction",
			conditions: map[string]workloadCondition{
				"Preempted": condition("Preempted", "True", "InClusterQueue"),
				"Evicted":   condition("Evicted", "True", "Preempted"),
			},
			wantStage: resourcecontext.SchedulingPreempted,
			wantBlock: "Preempted",
		},
		{
			name: "quota wait is context not failure inference",
			conditions: map[string]workloadCondition{
				"QuotaReserved": condition("QuotaReserved", "False", "WaitingForQuota"),
			},
			wantStage: resourcecontext.SchedulingWaiting,
			wantBlock: "QuotaReserved",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			stage, blocker := workloadStage(test.conditions, false, false, resourcecontext.TierBasic)
			if stage != test.wantStage {
				t.Fatalf("stage = %q, want %q", stage, test.wantStage)
			}
			if test.wantBlock == "" && blocker != nil {
				t.Fatalf("unexpected blocker: %+v", blocker)
			}
			if test.wantBlock != "" && (blocker == nil || blocker.Condition != test.wantBlock) {
				t.Fatalf("blocker = %+v, want %s", blocker, test.wantBlock)
			}
		})
	}
}

func TestForResourceExplicitlyInactiveWithoutConditionIsWaitingButNotBlocked(t *testing.T) {
	u := loadFixture(t, "waiting-granular.yaml")
	if err := unstructured.SetNestedSlice(u.Object, nil, "status", "conditions"); err != nil {
		t.Fatal(err)
	}
	if err := unstructured.SetNestedField(u.Object, false, "spec", "active"); err != nil {
		t.Fatal(err)
	}
	summary := ForResource(u, resourcecontext.TierBasic)
	if summary.Stage != resourcecontext.SchedulingWaiting || summary.Blocker != nil {
		t.Fatalf("inactive summary = %+v", summary)
	}
}

func TestForResourceStageUsesAdmissionChecksBeforeSerializationCap(t *testing.T) {
	u := loadFixture(t, "admission-check-rejected.yaml")
	conditions, _, _ := unstructured.NestedSlice(u.Object, "status", "conditions")
	conditions = conditions[:1]
	if err := unstructured.SetNestedSlice(u.Object, conditions, "status", "conditions"); err != nil {
		t.Fatal(err)
	}
	checks := make([]any, 0, maxAdmissionChecks+1)
	for i := 0; i < maxAdmissionChecks; i++ {
		checks = append(checks, map[string]any{"name": fmt.Sprintf("ready-%02d", i), "state": "Ready"})
	}
	checks = append(checks, map[string]any{"name": "zz-rejected", "state": "Rejected"})
	if err := unstructured.SetNestedSlice(u.Object, checks, "status", "admissionChecks"); err != nil {
		t.Fatal(err)
	}

	summary := ForResource(u, resourcecontext.TierBasic)
	if summary.Stage != resourcecontext.SchedulingExternalCheck {
		t.Fatalf("stage = %q, want external_check from full raw state", summary.Stage)
	}
	if len(summary.AdmissionChecks) != maxAdmissionChecks || !summary.AdmissionChecksTruncated {
		t.Fatalf("serialized checks len/truncated = %d/%t", len(summary.AdmissionChecks), summary.AdmissionChecksTruncated)
	}
}

func TestForResourceConcurrentAdmissionVariantUsesControllerOwner(t *testing.T) {
	variant := loadFixture(t, "concurrent-variant.yaml")
	summary := ForResource(variant, resourcecontext.TierBasic)
	if !summary.Variant || summary.ParentWorkload == nil {
		t.Fatalf("variant summary = %+v", summary)
	}
	if *summary.ParentWorkload != (resourcecontext.ContextRef{
		Kind: "Workload", Group: "kueue.x-k8s.io", Namespace: "training", Name: "training-parent",
	}) {
		t.Fatalf("parent = %+v", summary.ParentWorkload)
	}

	oldVersion := variant.DeepCopy()
	owners := oldVersion.GetOwnerReferences()
	owners[0].APIVersion = "kueue.x-k8s.io/v1beta1"
	oldVersion.SetOwnerReferences(owners)
	oldSummary := ForResource(oldVersion, resourcecontext.TierBasic)
	if oldSummary.Variant || oldSummary.ParentWorkload != nil {
		t.Fatalf("unsupported owner version was treated as documented v1beta2 Parent/Variant: %+v", oldSummary)
	}

	ordinary := ForResource(loadFixture(t, "waiting-granular.yaml"), resourcecontext.TierBasic)
	if ordinary.Variant || ordinary.ParentWorkload != nil {
		t.Fatalf("ordinary Job-owned workload classified as variant: %+v", ordinary)
	}
}

func TestForResourceBoundsRelatedRefsAndMessages(t *testing.T) {
	u := loadFixture(t, "admission-check-rejected.yaml")
	checks := make([]any, 0, maxAdmissionChecks+3)
	flavors := make(map[string]any, maxResourceFlavors+3)
	longMessage := strings.Repeat("界", maxMessageBytes)
	for i := 0; i < maxAdmissionChecks+3; i++ {
		checks = append(checks, map[string]any{
			"name":               fmt.Sprintf("check-%02d", i),
			"state":              "Rejected",
			"message":            longMessage,
			"lastTransitionTime": "2026-08-30T10:03:00Z",
		})
	}
	for i := 0; i < maxResourceFlavors+3; i++ {
		flavors[fmt.Sprintf("example.com/resource-%02d", i)] = fmt.Sprintf("flavor-%02d", i)
	}
	if err := unstructured.SetNestedSlice(u.Object, checks, "status", "admissionChecks"); err != nil {
		t.Fatal(err)
	}
	assignments, _, _ := unstructured.NestedSlice(u.Object, "status", "admission", "podSetAssignments")
	assignments[0].(map[string]any)["flavors"] = flavors
	if err := unstructured.SetNestedSlice(u.Object, assignments, "status", "admission", "podSetAssignments"); err != nil {
		t.Fatal(err)
	}

	summary := ForResource(u, resourcecontext.TierDiagnostic)
	if len(summary.AdmissionChecks) != maxAdmissionChecks || !summary.AdmissionChecksTruncated {
		t.Fatalf("checks len/truncated = %d/%t", len(summary.AdmissionChecks), summary.AdmissionChecksTruncated)
	}
	if len(summary.Flavors) != maxResourceFlavors || !summary.FlavorsTruncated {
		t.Fatalf("flavors len/truncated = %d/%t", len(summary.Flavors), summary.FlavorsTruncated)
	}
	for _, check := range summary.AdmissionChecks {
		if len(check.Message) > maxMessageBytes || !strings.HasSuffix(check.Message, "…") {
			t.Fatalf("message is not byte-bounded valid output: bytes=%d value=%q", len(check.Message), check.Message)
		}
	}
}

func TestResourceContextSchedulingOutputBudgets(t *testing.T) {
	u := loadFixture(t, "admission-check-rejected.yaml")
	checks := make([]any, 0, maxAdmissionChecks)
	flavors := make(map[string]any, maxResourceFlavors)
	for i := 0; i < maxAdmissionChecks; i++ {
		checks = append(checks, map[string]any{
			"name":                strings.Repeat(string(rune('a'+i)), 250),
			"state":               "Rejected",
			"message":             strings.Repeat("diagnostic message ", 100),
			"lastTransitionTime":  "2026-08-30T10:03:00Z",
			"retryCount":          int64(999),
			"requeueAfterSeconds": int64(999),
		})
		flavors[fmt.Sprintf("example.com/resource-%02d", i)] = strings.Repeat(string(rune('k'+i)), 250)
	}
	if err := unstructured.SetNestedSlice(u.Object, checks, "status", "admissionChecks"); err != nil {
		t.Fatal(err)
	}
	assignments, _, _ := unstructured.NestedSlice(u.Object, "status", "admission", "podSetAssignments")
	assignments[0].(map[string]any)["flavors"] = flavors
	if err := unstructured.SetNestedSlice(u.Object, assignments, "status", "admission", "podSetAssignments"); err != nil {
		t.Fatal(err)
	}

	const (
		basicBudgetBytes      = 8 << 10
		diagnosticBudgetBytes = 12 << 10
	)
	basic := ForResource(u, resourcecontext.TierBasic)
	diagnostic := ForResource(u, resourcecontext.TierDiagnostic)
	for _, test := range []struct {
		name   string
		value  resourcecontext.ResourceContext
		budget int
	}{
		{name: "basic", value: resourcecontext.ResourceContext{Tier: resourcecontext.TierBasic, Scheduling: basic}, budget: basicBudgetBytes},
		{name: "diagnostic", value: resourcecontext.ResourceContext{Tier: resourcecontext.TierDiagnostic, Scheduling: diagnostic}, budget: diagnosticBudgetBytes},
	} {
		t.Run(test.name, func(t *testing.T) {
			wire, err := json.Marshal(test.value)
			if err != nil {
				t.Fatal(err)
			}
			if len(wire) > test.budget {
				t.Fatalf("scheduling summary = %d bytes, budget = %d", len(wire), test.budget)
			}
			t.Logf("resourceContext with scheduling: %d bytes", len(wire))
		})
	}
	if len(diagnostic.AdmissionChecks) == 0 || diagnostic.AdmissionChecks[0].Message == "" {
		t.Fatal("diagnostic tier lost admission-check detail")
	}
	if basic.AdmissionChecks[0].Message != "" || basic.AdmissionChecks[0].LastTransitionTime != "" {
		t.Fatalf("basic tier includes diagnostic fields: %+v", basic.AdmissionChecks[0])
	}
}

func loadFixture(t *testing.T, name string) *unstructured.Unstructured {
	t.Helper()
	raw, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	jsonData, err := yaml.YAMLToJSON(raw)
	if err != nil {
		t.Fatal(err)
	}
	obj, _, err := unstructured.UnstructuredJSONScheme.Decode(jsonData, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	u, ok := obj.(*unstructured.Unstructured)
	if !ok {
		t.Fatalf("fixture decoded as %T", obj)
	}
	return u
}

func namesOf(refs []resourcecontext.ContextRef) []string {
	result := make([]string, 0, len(refs))
	for _, ref := range refs {
		result = append(result, ref.Name)
	}
	return result
}

func checkNames(checks []resourcecontext.SchedulingAdmissionCheck) []string {
	result := make([]string, 0, len(checks))
	for _, check := range checks {
		result = append(result, check.Check.Name)
	}
	return result
}
