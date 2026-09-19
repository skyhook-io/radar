package investigation

import (
	"strings"
	"testing"
)

func TestApplyPrompt_BindsConfirmedFix(t *testing.T) {
	fix := "Set `spec.replicas` to `3` on Deployment `x`"
	target := Target{Kind: "Deployment", Namespace: "prod", Name: "x"}
	p := ApplyPrompt(target, fix)
	if !strings.Contains(p, fix) {
		t.Errorf("apply prompt should embed the confirmed fix; got %q", p)
	}
	if !strings.Contains(p, "Deployment prod/x") {
		t.Errorf("apply prompt should name the target resource; got %q", p)
	}
	if p := ApplyPrompt(Target{Kind: "Deployment", Name: "x"}, ""); strings.Contains(p, "EXACTLY this fix") {
		t.Errorf("empty fix should use the fallback prompt; got %q", p)
	}
}

func TestApplyPrompt_BindsImmutableAPIGroup(t *testing.T) {
	grouped := ApplyPrompt(Target{Kind: "Rollout", Group: "argoproj.io", Namespace: "prod", Name: "checkout"}, "")
	for _, want := range []string{
		`immutable target API group is "argoproj.io"`,
		`Pass group="argoproj.io" to patch_resource`,
		`manifest apiVersion must belong to "argoproj.io"`,
		"Never mutate a same-named resource from another API group",
	} {
		if !strings.Contains(grouped, want) {
			t.Errorf("group-qualified apply prompt missing %q:\n%s", want, grouped)
		}
	}
	core := ApplyPrompt(Target{Kind: "Pod", Namespace: "prod", Name: "checkout"}, "")
	for _, want := range []string{"Kubernetes core API group", "omit group", "apiVersion must be v1"} {
		if !strings.Contains(core, want) {
			t.Errorf("core-group apply prompt missing %q:\n%s", want, core)
		}
	}
}

func TestTaskPrompt_HealthAwareOpening(t *testing.T) {
	api := Target{Kind: "Deployment", Namespace: "prod", Name: "api"}
	healthy := TaskPrompt(api, &HealthSignal{Health: "healthy"}, Options{})
	for _, want := range []string{
		"Radar currently reports Deployment prod/api as healthy",
		"do not manufacture a problem",
		`"healthy": boolean`,
		`"root_cause_evidence_refs": [string]`,
		"[[radar:evidence-ref=ev_...]]",
	} {
		if !strings.Contains(healthy, want) {
			t.Errorf("healthy prompt missing %q:\n%s", want, healthy)
		}
	}
	if strings.Contains(healthy, "Investigate the unhealthy") {
		t.Errorf("healthy prompt still uses unhealthy framing:\n%s", healthy)
	}

	broken := TaskPrompt(api, &HealthSignal{IssueCount: 2, HighestSeverity: "critical", TopReason: "CrashLoopBackOff"}, Options{})
	for _, want := range []string{
		"Radar currently flags 2 active issues on Deployment prod/api",
		"highest severity critical: CrashLoopBackOff",
		"Find the specific root cause",
		"Start with Radar's `diagnose` tool for this workload",
	} {
		if !strings.Contains(broken, want) {
			t.Errorf("broken prompt missing %q:\n%s", want, broken)
		}
	}

	auditOnly := TaskPrompt(Target{Kind: "Pod", Namespace: "prod", Name: "api-7"},
		&HealthSignal{Health: "healthy", AuditCount: 1, AuditSeverity: "high", TopFinding: "runAsRoot"}, Options{})
	for _, want := range []string{"static posture finding", "highest severity high", "not evidence of an active outage", "Verify quickly"} {
		if !strings.Contains(auditOnly, want) {
			t.Errorf("audit-only prompt missing %q:\n%s", want, auditOnly)
		}
	}

	coexisting := TaskPrompt(api, &HealthSignal{
		IssueCount: 1, HighestSeverity: "critical", TopReason: "CrashLoopBackOff",
		AuditCount: 1, AuditSeverity: "high", TopFinding: "runAsRoot",
	}, Options{})
	for _, want := range []string{
		"highest severity critical: CrashLoopBackOff",
		"static posture finding; highest severity high: runAsRoot",
		"not evidence of an active outage",
		"Find the specific root cause",
	} {
		if !strings.Contains(coexisting, want) {
			t.Errorf("coexisting issue/audit prompt missing %q:\n%s", want, coexisting)
		}
	}
	if strings.Contains(coexisting, "Verify quickly") {
		t.Errorf("coexisting issue/audit prompt used audit-only healthy framing:\n%s", coexisting)
	}

	nonWorkload := TaskPrompt(Target{Kind: "ConfigMap", Namespace: "prod", Name: "api"}, nil, Options{})
	if strings.Contains(nonWorkload, "`diagnose` tool") {
		t.Errorf("unsupported resource prompt must not direct the agent to semantic diagnose:\n%s", nonWorkload)
	}
	if !strings.Contains(nonWorkload, "Radar did not attach a health summary") {
		t.Errorf("a run without a health frame must say so:\n%s", nonWorkload)
	}
	grouped := TaskPrompt(Target{Kind: "Rollout", Group: "argoproj.io", Namespace: "prod", Name: "api"}, nil, Options{})
	if !strings.Contains(grouped, "Pass `group=argoproj.io` to every Radar tool") {
		t.Errorf("a grouped target must pin the group on every call:\n%s", grouped)
	}
}

func TestHealthFrameDisclosesMissingAuditInputsWithoutFindings(t *testing.T) {
	frame := healthFrame("Pod app/api", &HealthSignal{AuditMissingInputs: []string{"secrets"}})
	if !strings.Contains(frame, "could not read these inputs: secrets") || !strings.Contains(frame, "Zero audit findings do not establish") {
		t.Fatal(frame)
	}
}

func TestReadOnlyToolsExcludeWrites(t *testing.T) {
	for _, tool := range ReadOnlyTools {
		if IsWriteTool(tool) {
			t.Errorf("write tool %q must not be in the read allowlist", tool)
		}
	}
	if !IsReadOnlyTool("mcp__radar__get_resource") || !IsReadOnlyTool("radar.get_events") || IsReadOnlyTool("mcp__grafana__query_prometheus") {
		t.Fatal("prefixed Radar tool names must resolve to the allowlist; foreign servers must not")
	}
}

// The prompt must say what the product's binder enforces: OSS proves refs
// from earlier turns of the run, Cloud only the current turn's.
func TestCitationPolicyIsStatedInThePrompt(t *testing.T) {
	run := FollowUpPrompt("why?", Options{Citations: CiteRun})
	turn := FollowUpPrompt("why?", Options{Citations: CiteTurn})
	if !strings.Contains(run, "results from earlier turns of this investigation") || strings.Contains(run, "only results read in this turn") {
		t.Fatalf("run policy not stated:\n%s", run)
	}
	if !strings.Contains(turn, "only results read in this turn") || strings.Contains(turn, "results from earlier turns") {
		t.Fatalf("turn policy not stated:\n%s", turn)
	}
	if !strings.Contains(verdictContract(CiteTurn), "read in this turn") || strings.Contains(verdictContract(CiteRun), "read in this turn") {
		t.Fatal("the evidence rule must follow the policy")
	}
}

func TestReadOnlyPromptsCarryTheContractAndTheNudge(t *testing.T) {
	connected := MetricsAvailability{Connected: true, Address: "http://prometheus.monitoring:9090"}
	target := Target{Kind: "Deployment", Namespace: "prod", Name: "api"}
	for name, prompt := range map[string]string{
		"task":         TaskPrompt(target, nil, Options{Metrics: connected}),
		"follow-up":    FollowUpPrompt("Why is it restarting?", Options{Metrics: connected}),
		"verification": VerificationPrompt("Did it recover?", Options{Metrics: connected}),
	} {
		if !strings.Contains(prompt, "VERDICT BLOCK:") || !strings.Contains(prompt, "PLACING A CARD:") {
			t.Errorf("%s prompt lost the contract:\n%s", name, prompt)
		}
		if !strings.Contains(prompt, "Prometheus is connected at http://prometheus.monitoring:9090") ||
			!strings.HasSuffix(strings.TrimSpace(prompt), "which also matches sibling workloads.") {
			t.Errorf("%s prompt lacks the metrics nudge after the contract:\n%s", name, prompt)
		}
	}
	if p := FollowUpPrompt("Why?", Options{}); !strings.HasPrefix(p, "Why?") || strings.Contains(p, "Prometheus") {
		t.Fatalf("an unprobed follow-up must lead with the question and say nothing about metrics:\n%s", p)
	}
	if p := TaskPrompt(target, nil, Options{Metrics: MetricsAvailability{Address: "http://prometheus.monitoring:9090"}}); strings.Contains(p, "Prometheus") {
		t.Fatalf("a failed probe must say nothing to the agent:\n%s", p)
	}
	if p := VerificationPrompt("Did it recover?", Options{}); !strings.Contains(p, "VERIFICATION.") || !strings.Contains(p, "FINAL ANSWER") {
		t.Fatalf("verification must answer as a fresh assessment:\n%s", p)
	}
	if p := ApplyPrompt(target, "set replicas to 2"); strings.Contains(p, "Prometheus") || strings.Contains(p, "VERDICT BLOCK") {
		t.Fatalf("an apply turn carries neither the nudge nor the verdict contract:\n%s", p)
	}
}

// The prompt is model-visible and leaves the machine, so every place a
// configured URL can carry a credential has to be gone: userinfo, the query
// string an auth proxy commonly uses, and the fragment.
func TestMetricsNudgeStripsCredentials(t *testing.T) {
	for _, tc := range []struct {
		name, address, secret, want string
	}{
		{"userinfo", "https://admin:s3cret@prom.example.com:9090", "s3cret", "https://prom.example.com:9090"},
		{"query", "https://prom.example.com/api?token=s3cret", "s3cret", "https://prom.example.com/api"},
		{"fragment", "https://prom.example.com/api#s3cret", "s3cret", "https://prom.example.com/api"},
		{"all three", "https://admin:p@prom.example.com/api?token=s3cret#f", "s3cret", "https://prom.example.com/api"},
	} {
		nudge := MetricsNudge(MetricsAvailability{Connected: true, Address: tc.address})
		if strings.Contains(nudge, tc.secret) || strings.Contains(nudge, "admin") {
			t.Fatalf("%s: a credential reached the prompt:\n%s", tc.name, nudge)
		}
		if !strings.Contains(nudge, tc.want) {
			t.Fatalf("%s: prompt lost the address %q:\n%s", tc.name, tc.want, nudge)
		}
	}
}

func TestExplanationPrompt(t *testing.T) {
	prompt := ExplanationPrompt(Verdict{
		Summary:     "Plain headline.",
		RootCause:   "stale password",
		Unresolved:  []string{"Atlas rotation"},
		Report:      "The pod crashes.\n\n[[radar:evidence=0]]\n\nBecause of auth.",
		Remediation: []string{"Restore configuration."},
		Evidence: []EvidenceItem{
			{Status: Linked, Role: RoleCause, Claim: "Atlas rejects this password."},
			{Status: Unlinked},
			{Status: Linked, Role: RoleContext},
		},
		RuledOut: []RuledOut{{Hypothesis: "Account locked", EvidenceIndex: 0}},
	})
	for _, want := range []string{
		`"summary":"Plain headline."`, "Atlas rotation", "stale password", "Restore configuration.",
		`"cause: Atlas rejects this password."`, `"Account locked"`, "must not add, change, or reassign",
		"Do not recheck the cluster or call tools", "Do not apply anything", "120-180", "Preserve uncertainty",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("explanation prompt missing %q:\n%s", want, prompt)
		}
	}
	if strings.Contains(prompt, "[[radar:evidence=") {
		t.Fatalf("placement markers must be stripped for the explaining model: %s", prompt)
	}
	if strings.Contains(prompt, "root_cause_evidence_refs") {
		t.Fatal("explanation must not request fresh evidence refs")
	}
	conditional := ExplanationPrompt(Verdict{RootCause: "x", Steps: []Step{{Text: "Roll back", Kind: StepMitigate, Precondition: "revision 7 still authenticates"}}, Remediation: []string{"Roll back"}})
	if !strings.Contains(conditional, `"Roll back (only if revision 7 still authenticates)"`) {
		t.Fatalf("a step's precondition must reach the explaining model: %s", conditional)
	}
	bare := ExplanationPrompt(Verdict{RootCause: "x"})
	if strings.Contains(bare, `"evidenceNotes"`) || strings.Contains(bare, `"ruledOut"`) {
		t.Fatalf("empty case must not appear in the prompt: %s", bare)
	}
}

func TestExplanationPromptPreservesHealthyAndInconclusive(t *testing.T) {
	for _, verdict := range []Verdict{{Healthy: true}, {Inconclusive: true}} {
		prompt := ExplanationPrompt(verdict)
		want := `"healthy":true`
		if verdict.Inconclusive {
			want = `"inconclusive":true`
		}
		if !strings.Contains(prompt, want) || !strings.Contains(prompt, "Do not recheck the cluster or call tools") {
			t.Fatalf("missing assessment state or tool boundary: %s", prompt)
		}
	}
}
