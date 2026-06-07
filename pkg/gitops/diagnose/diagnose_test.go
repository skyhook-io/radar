package diagnose

import (
	"strings"
	"testing"
)

func TestParseArgoOperationError(t *testing.T) {
	cases := []struct {
		name      string
		msg       string
		wantCause string // substring match — full text is brittle to copy edits
		wantKind  string
		wantName  string
		wantRetry int
		wantStuck bool
	}{
		{
			name:      "annotation too long with affected CRD and retry suffix",
			msg:       `one or more objects failed to apply, reason: error when patching "/dev/shm/foo": CustomResourceDefinition.apiextensions.k8s.io "scaledjobs.keda.sh" is invalid: metadata.annotations: Too long: may not be more than 262144 bytes (retried 5 times)`,
			wantCause: "256 KB metadata limit",
			wantKind:  "CustomResourceDefinition",
			wantName:  "scaledjobs.keda.sh",
			wantRetry: 5,
			wantStuck: true,
		},
		{
			name:      "admission webhook rejection",
			msg:       `admission webhook "validation.gatekeeper.sh" denied the request: missing required label "owner"`,
			wantCause: "admission webhook rejected",
			wantRetry: 0,
			wantStuck: false,
		},
		{
			name:      "admission webhook backend unreachable",
			msg:       `failed calling webhook "vcluster.cnpg.io": failed to call webhook: no endpoints available for service "cnpg-webhook-service"`,
			wantCause: "couldn't reach an admission/mutating webhook",
		},
		{
			name:      "rbac forbidden with resource extracted",
			msg:       `Deployment.apps "billing" is forbidden: User "system:serviceaccount:argocd:argocd-controller" cannot patch resource`,
			wantCause: "RBAC denied",
			wantKind:  "Deployment",
			wantName:  "billing",
		},
		{
			name:      "unrecognized message → no cause but raw still preserved by caller",
			msg:       "something completely novel went wrong",
			wantCause: "",
		},
		{
			name:      "single retry → not stuck",
			msg:       `whatever (retried 1 times)`,
			wantRetry: 1,
			wantStuck: false,
		},
		{
			name: "empty input → all zero values",
			msg:  "",
		},
		// Each row below pins one regex in argoErrorPatterns; a reorder of the
		// table or a regex regression surfaces here as a substring miss.
		{
			name:      "namespace not found populates Remediation",
			msg:       `failed to apply: namespaces "demo-broken-sync" not found`,
			wantCause: "destination namespace does not exist",
		},
		{
			name:      "labels too long",
			msg:       `Service "foo" is invalid: metadata.labels: Too long: must have at most 63 chars per key`,
			wantCause: "64-character-per-key limit",
			// argoAffectedRefRE happens to also capture from this fixture —
			// pin the values so a regex change is visible. Functionally these
			// flow into Issue.Refs and add a same-row ref to the failure card.
			wantKind: "Service",
			wantName: "foo",
		},
		{
			name:      "resource already exists outside GitOps",
			msg:       `Job.batch "migrate" already exists`,
			wantCause: "already exists",
			wantKind:  "Job",
			wantName:  "migrate",
		},
		{
			name:      "CRD not registered",
			msg:       `no matches for kind "Tenant" in version "capsule.clastix.io/v1beta1"`,
			wantCause: "CustomResourceDefinition for this kind isn't registered",
		},
		{
			name:      "cluster unreachable (i/o timeout)",
			msg:       `dial tcp 10.0.0.1:443: i/o timeout`,
			wantCause: "Cluster unreachable",
		},
		{
			name:      "cluster unreachable (connection refused)",
			msg:       `dial tcp 10.0.0.1:443: connect: connection refused`,
			wantCause: "Cluster unreachable",
		},
		{
			name:      "immutable field changed",
			msg:       `Service.spec.clusterIP: field is immutable`,
			wantCause: "Kubernetes treats as immutable",
		},
		{
			name: "unknown apiVersion (no 'no matches' clause)",
			// argoErrorPatterns intentionally matches 'no matches for kind'
			// first because it's the more actionable diagnosis; pin a fixture
			// that only triggers 'unable to recognize' so the more-generic
			// pattern is exercised on its own.
			msg:       `unable to recognize the resource: invalid manifest "foo.yaml"`,
			wantCause: "API version the cluster doesn't recognize",
		},
		{
			name:      "concurrent modification",
			msg:       `Operation cannot be fulfilled on deployments.apps "x": the object has been modified; please apply your changes to the latest version`,
			wantCause: "modified concurrently",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ParseArgoOperationError(tc.msg)
			if tc.wantCause != "" && !strings.Contains(got.Cause, tc.wantCause) {
				t.Errorf("Cause = %q, want substring %q", got.Cause, tc.wantCause)
			}
			if tc.wantCause == "" && got.Cause != "" {
				t.Errorf("Cause = %q, want empty (unrecognized pattern)", got.Cause)
			}
			if got.AffectedKind != tc.wantKind {
				t.Errorf("AffectedKind = %q, want %q", got.AffectedKind, tc.wantKind)
			}
			if got.AffectedName != tc.wantName {
				t.Errorf("AffectedName = %q, want %q", got.AffectedName, tc.wantName)
			}
			if got.RetryCount != tc.wantRetry {
				t.Errorf("RetryCount = %d, want %d", got.RetryCount, tc.wantRetry)
			}
			if got.Stuck != tc.wantStuck {
				t.Errorf("Stuck = %v, want %v", got.Stuck, tc.wantStuck)
			}
		})
	}
}

// TestParseArgoOperationError_NamespaceRemediation pins the structured-
// remediation path. The missing-namespace pattern is the only pattern that
// drives a one-click fix; a regex regression that loses the capture group
// would silently downgrade the failure-card UX to diagnosis-only. Asserts on
// the vocabulary-neutral primitives — the insights adapter maps these onto its
// Remediation wire type (see remediationFromParsed).
func TestParseArgoOperationError_NamespaceRemediation(t *testing.T) {
	got := ParseArgoOperationError(`failed to create resource: namespaces "demo-broken-sync" not found`)
	if got.RemediationKind != RemediationCreateNamespace {
		t.Errorf("RemediationKind = %q, want %q", got.RemediationKind, RemediationCreateNamespace)
	}
	if got.RemediationTarget != "demo-broken-sync" {
		t.Errorf("RemediationTarget = %q, want %q", got.RemediationTarget, "demo-broken-sync")
	}
	if got.RemediationHint == "" {
		t.Errorf("RemediationHint = empty, want non-empty operator copy")
	}
}

func TestParseArgoOperationError_HookFailures(t *testing.T) {
	cases := []struct {
		name      string
		msg       string
		wantCause string
	}{
		{
			name:      "PreSync hook failed",
			msg:       `PreSync phase failed: hook "db-migration" exited with status 1`,
			wantCause: "sync hook failed",
		},
		{
			name:      "generic hook failed wording",
			msg:       `hook "drain-cache" failed: timed out after 5m`,
			wantCause: "sync hook failed",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ParseArgoOperationError(tc.msg)
			if !strings.Contains(strings.ToLower(got.Cause), tc.wantCause) {
				t.Errorf("Cause = %q, want substring %q", got.Cause, tc.wantCause)
			}
		})
	}
}

func TestSeverityForConditionType(t *testing.T) {
	cases := []struct {
		typ            string
		wantToken      string
		wantRecognized bool
	}{
		{"ComparisonError", "critical", true},
		{"InvalidSpecError", "critical", true},
		{"OrphanedResourceWarning", "warning", true},
		{"SharedResourceWarning", "warning", true},
		{"SomeUnrelatedInfo", "info", false},
		{"", "info", false},
	}
	for _, tc := range cases {
		tok, recognized := SeverityForConditionType(tc.typ)
		if tok != tc.wantToken || recognized != tc.wantRecognized {
			t.Errorf("SeverityForConditionType(%q) = (%q, %v), want (%q, %v)", tc.typ, tok, recognized, tc.wantToken, tc.wantRecognized)
		}
	}
}

func TestActionForCondition(t *testing.T) {
	if ActionForCondition("ComparisonError") == "" {
		t.Error("ComparisonError should have an action string")
	}
	if ActionForCondition("SyncError") == "" {
		t.Error("SyncError should have an action string")
	}
	if ActionForCondition("OrphanedResourceWarning") == "" {
		t.Error("OrphanedResourceWarning should have an action string")
	}
	if got := ActionForCondition("SomethingUnknown"); got != "" {
		t.Errorf("unknown condition action = %q, want empty", got)
	}
}

func TestActionForFluxReason(t *testing.T) {
	if ActionForFluxReason("ArtifactFailed") == "" {
		t.Error("ArtifactFailed should have an action string")
	}
	if ActionForFluxReason("ChartNotReady") == "" {
		t.Error("ChartNotReady should have an action string")
	}
	// Unknown reasons fall back to generic guidance, never empty.
	if ActionForFluxReason("TotallyNovelReason") == "" {
		t.Error("unknown flux reason should fall back to generic guidance, got empty")
	}
}
