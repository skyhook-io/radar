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

func TestCleanArgoControllerMessage(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "single grpc envelope",
			in:   "rpc error: code = Unknown desc = app path does not exist",
			want: "app path does not exist",
		},
		{
			name: "nested grpc envelope keeps diagnostic context",
			in:   "application spec is invalid: rpc error: code = Unknown desc = failed to generate manifests: rpc error: code = Unknown desc = app path does not exist",
			want: "application spec is invalid: failed to generate manifests: app path does not exist",
		},
		{
			name: "nested command failure keeps middle layer",
			in:   "rpc error: code = Unknown desc = Manifest generation error (cached): helm template failed: rpc error: code = Unknown desc = exit status 1",
			want: "Manifest generation error (cached): helm template failed: exit status 1",
		},
		{
			name: "plain condition message unchanged",
			in:   "app path does not exist",
			want: "app path does not exist",
		},
		{
			name: "empty grpc desc preserves original",
			in:   "rpc error: code = Unknown desc = ",
			want: "rpc error: code = Unknown desc =",
		},
		{
			name: "empty",
			in:   "   ",
			want: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := CleanArgoControllerMessage(tc.in); got != tc.want {
				t.Fatalf("CleanArgoControllerMessage = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestCleanArgoControllerMessageWithRaw(t *testing.T) {
	raw := "rpc error: code = Unknown desc = app path does not exist"
	cleaned, rawOut := CleanArgoControllerMessageWithRaw(raw)
	if cleaned != "app path does not exist" {
		t.Fatalf("cleaned = %q, want app path error", cleaned)
	}
	if rawOut != raw {
		t.Fatalf("raw = %q, want %q", rawOut, raw)
	}

	cleaned, rawOut = CleanArgoControllerMessageWithRaw("app path does not exist")
	if cleaned != "app path does not exist" || rawOut != "" {
		t.Fatalf("plain message cleaned/raw = %q/%q, want unchanged/empty raw", cleaned, rawOut)
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
		{"RepeatedResourceWarning", "info", true},
		{"OtherWarning", "warning", true},
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

func TestParseArgoOperationError_EmptyAutoSync(t *testing.T) {
	got := ParseArgoOperationError("Skipping sync attempt to [abc123]: auto-sync will wipe out all resources")
	if got.Cause != "Argo CD blocked auto-sync because the render includes none of the resources this Application manages." {
		t.Fatalf("missing empty-state cause: %+v", got)
	}
	// The list row shows the summary; a leading revision SHA would push the
	// meaning past the truncation point.
	if got.Reason != ReasonAutoSyncBlockedEmpty || got.Summary != "Auto-sync blocked: the render includes none of the managed resources" || strings.Contains(got.Summary, "abc123") {
		t.Fatalf("summary must lead with the meaning: %+v", got)
	}
	if !strings.Contains(got.Action, "A sync with Prune enabled could delete all of them") {
		t.Fatalf("guard advice must warn against a manual sync: %q", got.Action)
	}
	if got.ActionSecondaryTitle != "If removing all managed resources is intended" || len(got.ActionSecondary) != 2 || got.Action != got.ActionPrimary+" "+strings.Join(got.ActionSecondary, " ") {
		t.Fatalf("must retain complete guidance plus its presentation: %+v", got)
	}
	if !strings.Contains(got.ActionSecondary[0], "resources finalizer decides") || !strings.Contains(got.ActionSecondary[0], "ApplicationSet generator") || !strings.Contains(got.ActionSecondary[1], "syncPolicy.automated.allowEmpty: true") || !strings.Contains(got.ActionSecondary[1], "all future syncs") {
		t.Fatalf("missing intended-render paths: %+v", got)
	}
	if got.RemediationKind != "" {
		t.Fatalf("must not disable the empty-state guard: %+v", got)
	}
}

func TestParseArgoOperationError_EmptyAutoSyncPrecision(t *testing.T) {
	for _, message := range []string{"Skipping auto-sync: most recent sync already to [abc123]", "Skipping auto-sync: another operation is in progress", "Failed last sync attempt to [abc123]: application path does not exist"} {
		parsed := ParseArgoOperationError(message)
		if parsed.Reason != "" || parsed.Action != "" {
			t.Errorf("ordinary skip/failure must not suggest allowEmpty: %+v", parsed)
		}
	}
	action := ActionForCondition("RepeatedResourceWarning")
	if strings.Contains(action, "multiple Argo Applications") || !strings.Contains(action, "last rendered occurrence") || !strings.Contains(action, "unintended") {
		t.Fatalf("repeated-resource guidance must distinguish intentional source overrides: %q", action)
	}
}

func TestEmptyAutoSyncAdviceOrderAndSafety(t *testing.T) {
	action := ParseArgoOperationError("Skipping sync attempt to [abc]: auto-sync will wipe out all resources").Action
	source := strings.Index(action, "source path")
	generator := strings.Index(action, "remove it from the ApplicationSet generator")
	recreation := strings.Index(action, "while the generator still emits it, the ApplicationSet recreates it")
	removal := strings.Index(action, "delete the Application")
	allow := strings.Index(action, "allowEmpty: true")
	if source < 0 || generator <= source || recreation <= generator || removal <= recreation || allow <= removal {
		t.Fatalf("expected source checks, generator removal and recreation warning, plain Application deletion, then allowEmpty: %s", action)
	}
	for _, required := range []string{"A sync with Prune enabled could delete all of them", "Prune=false", "Prune=confirm", "targetRevision", "Helm values", "include/exclude", "resources finalizer", "remove it from the ApplicationSet generator", "all future syncs", "If it should render empty"} {
		if !strings.Contains(action, required) {
			t.Errorf("missing %q: %s", required, action)
		}
	}
	if strings.Contains(action, "delete the ApplicationSet") || strings.Contains(action, "Application/ApplicationSet") {
		t.Fatalf("unsafe removal advice: %s", action)
	}
	orphan := ActionForCondition("OrphanedResourceWarning")
	if !strings.Contains(orphan, "spec.orphanedResources.ignore") || !strings.Contains(orphan, "spec.orphanedResources.warn") || strings.Contains(orphan, "label") {
		t.Fatalf("wrong orphan exclusion mechanism: %s", orphan)
	}
}

func TestIsArgoFailedAttemptCondition(t *testing.T) {
	for _, prefix := range []string{"Failed sync attempt to ", "Failed last sync attempt to "} {
		for _, operationMessage := range []string{"denied", "", "  "} {
			if got := IsArgoFailedAttemptCondition(prefix+"[abc]: denied", operationMessage); got != (operationMessage == "denied") {
				t.Errorf("%q / %q: got %v", prefix, operationMessage, got)
			}
		}
	}
	for _, message := range []string{"", "a different sync error", "Skipping sync attempt to [new]: auto-sync will wipe out all resources", "Failed sync attempt unrelated"} {
		if IsArgoFailedAttemptCondition(message, "denied") {
			t.Errorf("unrelated condition deduplicated: %q", message)
		}
	}
}

func TestSharedResourceWarningAction(t *testing.T) {
	// One condition row names one resource; generic advice must read right for
	// one resource or many.
	if got := ActionForCondition("SharedResourceWarning"); strings.Contains(got, "These resources") || !strings.Contains(got, "remove it from this Application's source") {
		t.Fatalf("expected number-neutral ownership guidance: %q", got)
	}
	one := ArgoConditionAction("SharedResourceWarning", "argocd/b", []string{"Deployment/guestbook-ui is part of applications argocd/live-shared-b and live-shared-a"})
	if !strings.HasPrefix(one, "Also tracked by Application live-shared-a. ") {
		t.Fatalf("expected the other owner named: %q", one)
	}
	for _, want := range []string{
		"To leave it with Application live-shared-a, remove it from this Application's source.",
		"To move it here, first note whether Application live-shared-a has auto-sync on, and turn it off if so. Do not sync it manually during the move.",
		"For an ApplicationSet or parent-managed Application, disable auto-sync in the template or Git so it stays off.",
	} {
		if !strings.Contains(one, want) {
			t.Fatalf("ownership advice missing %q: %q", want, one)
		}
	}
	last := -1
	for _, step := range []string{
		"first note whether Application live-shared-a has auto-sync on, and turn it off if so",
		"disable auto-sync in the template or Git so it stays off",
		"Finish or terminate any running or retrying operation on Application live-shared-a",
		"turning off auto-sync does not stop it",
		"Then sync this Application",
		"confirm Argo CD lists this Application as the resource's tracker",
		"remove it from the source of Application live-shared-a",
		"restore Application live-shared-a's previous auto-sync setting",
	} {
		at := strings.Index(one, step)
		if at <= last {
			t.Fatalf("missing or misordered move step %q: %q", step, one)
		}
		last = at
	}
	if strings.Contains(one, "remove it from the others") {
		t.Fatalf("advice must not leave the side to remove it from ambiguous: %q", one)
	}
	many := ArgoConditionAction("SharedResourceWarning", "argocd/b", []string{
		"Service/a is part of applications argocd/b and owner-2",
		"Service/b is part of applications argocd/b and owner-1",
		"Service/c is part of applications argocd/b and owner-2",
		"Service/d is part of applications argocd/b and owner-3",
		"Service/e is part of applications argocd/b and owner-4",
		"a message in some other shape",
	})
	if !strings.HasPrefix(many, "Also tracked by Applications owner-1, owner-2, owner-3 and 1 more. ") {
		t.Fatalf("expected deduplicated, bounded owners: %q", many)
	}
	if got := ArgoConditionAction("SharedResourceWarning", "argocd/b", []string{"unparseable"}); got != ActionForCondition("SharedResourceWarning") {
		t.Fatalf("unparseable message must fall back to generic advice: %q", got)
	}
}

func TestArgoSyncHazardActionSharedTrackingIsTemporary(t *testing.T) {
	for _, tc := range []struct{ message, owner string }{
		{"Deployment/web is part of applications argocd/billing and payments", "Application payments"},
		{"unparseable", "another Application"},
	} {
		got := ArgoSyncHazardAction(false, []string{tc.message})
		if !strings.Contains(got, "A successful sync can take over their tracking unless it uses FailOnSharedResource=true") || !strings.Contains(got, "While "+tc.owner+" still declares them, its next successful sync can take tracking back") || !strings.Contains(got, "remove them from this Application's source before syncing") {
			t.Errorf("must explain temporary tracking and how to leave ownership unchanged: %q", got)
		}
	}
}

func TestOrphanedResourceWarningActionSaysWhereToLook(t *testing.T) {
	got := ArgoConditionAction("OrphanedResourceWarning", "argocd/live-orphan", []string{"Application has 3 orphaned resources"})
	if !strings.Contains(got, "Argo CD doesn't associate with any Application") || !strings.Contains(got, `"argocd app resources argocd/live-orphan --orphaned"`) || !strings.Contains(got, "Argo CD UI") || !strings.Contains(got, "spec.orphanedResources.ignore") {
		t.Fatalf("orphan guidance must say where to see which resources: %q", got)
	}
	if generic := ActionForCondition("OrphanedResourceWarning"); strings.Contains(generic, "argocd app") || strings.Contains(generic, "  ") || !strings.Contains(generic, "Argo CD UI") || !strings.Contains(generic, "Argo CD doesn't associate with any Application") {
		t.Fatalf("generic orphan guidance without an app name: %q", generic)
	}
}

func TestArgoSyncHazardAction(t *testing.T) {
	shared := []string{"Deployment/web is part of applications argocd/b and live-shared-a"}
	for _, tc := range []struct {
		name     string
		prunes   bool
		shared   []string
		contains string
	}{
		{"plain drift", false, nil, ""},
		{"empty render", true, nil, "The render includes none of the resources this Application manages. A sync with Prune enabled could delete all of them, except resources marked Prune=false; Prune=confirm ones wait for confirmation."},
		{"empty render outranks shared", true, shared, "The render includes none of the resources this Application manages. A sync with Prune enabled could delete all of them, except resources marked Prune=false; Prune=confirm ones wait for confirmation."},
		{"shared", false, shared, "also tracked by Application live-shared-a"},
		{"shared, owner unknown", false, []string{"unparseable"}, "also tracked by another Application"},
	} {
		got := ArgoSyncHazardAction(tc.prunes, tc.shared)
		if tc.contains == "" {
			if got != "" {
				t.Errorf("%s: want no hazard, got %q", tc.name, got)
			}
			continue
		}
		if !strings.Contains(got, tc.contains) || strings.Contains(got, "click Sync") {
			t.Errorf("%s: got %q, want %q and no click-Sync advice", tc.name, got, tc.contains)
		}
	}
}
