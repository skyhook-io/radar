package kagent

import (
	"regexp"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// Ready=False reasons kagent sets while it builds the ActorTemplate's golden
// snapshot, and when it retries a failed build
// (go/core/internal/controller/status.go:54, golden_retry.go:21).
var preparingReasons = map[string]bool{"ActorTemplatePending": true, "ActorTemplateRetrying": true}

// Ready=False reasons that fail preparation outright
// (go/core/internal/controller/reconciler.go:434,449,493).
var preparationFailedReasons = map[string]bool{"ActorTemplateRejected": true, "ActorTemplateConflict": true, "RuntimePreparationFailed": true}

// AgentRevisions are the revisions an Agent's status names. New sessions pin
// LatestSuccessful (go/core/internal/database/sessions.go:118), so a change
// kagent cannot prepare leaves new sessions on the last one it did.
type AgentRevisions struct {
	Desired, LatestSuccessful string
	WorkloadImage             string
}

func ReadAgentRevisions(u *unstructured.Unstructured) AgentRevisions {
	var r AgentRevisions
	r.Desired, _, _ = unstructured.NestedString(u.Object, "status", "desiredRevision")
	r.LatestSuccessful, _, _ = unstructured.NestedString(u.Object, "status", "latestSuccessfulRevision")
	r.WorkloadImage, _, _ = unstructured.NestedString(u.Object, "status", "workloadImage")
	return r
}

// ReadAgent reads an Agent's conditions. kagent writes Accepted,
// EgressPoliciesApplied, ResolvedRefs, Compatible and Ready; a failed stage
// marks every later stage False with reason Blocked
// (go/core/internal/controller/status.go:62).
func ReadAgent(u *unstructured.Unstructured, now time.Time) Reading {
	if u.GetDeletionTimestamp() != nil {
		return deleting()
	}
	ready := findCondition(u, "Ready")
	// kagent reports egress grants apart from readiness, so they are read
	// even before Ready is.
	if !ready.found {
		return withEgress(notReported(u, now, KindAgent), readEgress(u))
	}
	current := withEgress(readAgentReady(u, ready, now), readEgress(u))
	if observed, isStale := stale(u, ready); isStale {
		return staleOf(current, observed, u.GetGeneration(), KindAgent)
	}
	return current
}

func readAgentReady(u *unstructured.Unstructured, ready condition, now time.Time) Reading {
	rev := ReadAgentRevisions(u)
	switch ready.status {
	case "True":
		return Reading{State: StateReady, Tone: ToneHealthy, Text: "Ready", Summary: "Ready to start sessions", Condition: "Ready", Reason: ready.reason, Message: ready.message, Since: ready.since, SinceKnown: ready.sinceKnown}
	case "False":
	default:
		return Reading{State: StateUnknown, Tone: ToneUnknown, Text: "Unknown", Summary: "kagent can't tell whether the agent is ready", Condition: "Ready", Reason: ready.reason, Message: ready.message}
	}
	if refs := findCondition(u, "ResolvedRefs"); refs.status == "False" {
		return agentBlocked(refs, rev)
	}
	if compatible := findCondition(u, "Compatible"); compatible.status == "False" {
		return agentFailed("Compatible", compatible, rev, compatibleStep)
	}
	if preparingReasons[ready.reason] {
		what := "the next revision"
		if rev.Desired != "" {
			what = "revision " + ShortRevision(rev.Desired)
		}
		lead := "Preparing "
		if ready.reason == "ActorTemplateRetrying" {
			lead = "Retrying a failed build of "
		}
		return preparing(ready, KindAgent, lead+what+agentSessions(rev, "until it is ready"), what)
	}
	if preparationFailedReasons[ready.reason] {
		return agentFailed("Ready", ready, rev, preparationStep)
	}
	return notReady(ready, KindAgent)
}

// agentSessions says which revision new sessions start on while the desired
// one is not ready.
func agentSessions(rev AgentRevisions, otherwise string) string {
	if rev.LatestSuccessful != "" {
		return "; new sessions still start on revision " + ShortRevision(rev.LatestSuccessful)
	}
	return strings.TrimSpace("; no session can start " + otherwise)
}

func agentBlocked(refs condition, rev AgentRevisions) Reading {
	summary := "Latest change blocked"
	if refs.message != "" {
		summary += ": " + refs.message
	}
	r := Reading{State: StateBlocked, Text: "Blocked", Summary: summary + agentSessions(rev, ""), Condition: "ResolvedRefs", Reason: refs.reason, Message: refs.message, Since: refs.since, SinceKnown: refs.sinceKnown}
	r.Summary = strings.TrimSuffix(r.Summary, " ")
	severity := SeverityCritical
	r.Tone = ToneUnhealthy
	if rev.LatestSuccessful != "" {
		severity, r.Tone = SeverityWarning, ToneAlert
	}
	reason := ReasonNoPreparedRevision
	if rev.LatestSuccessful != "" {
		reason = ReasonChangeBlocked
	}
	r.finding = &Finding{Severity: severity, Reason: reason, KagentReason: refs.reason, Since: refs.since, SinceKnown: refs.sinceKnown,
		Message: sentence(r.Summary), Action: referenceStep(refs.message, "agent")}
	return r
}

// agentFailed reads a revision kagent can't prepare. Like a blocked one, it
// leaves new sessions on the last prepared revision (and kagent retries
// RuntimePreparationFailed), so it is critical only when there is none.
func agentFailed(condType string, c condition, rev AgentRevisions, step func(string) string) Reading {
	summary := "Failed"
	if c.message != "" {
		summary += ": " + c.message
	}
	summary += agentSessions(rev, "")
	r := Reading{State: StateFailed, Tone: ToneUnhealthy, Text: "Failed", Summary: summary, Condition: condType, Reason: c.reason, Message: c.message, Since: c.since, SinceKnown: c.sinceKnown}
	severity, reason := SeverityCritical, ReasonNoPreparedRevision
	if rev.LatestSuccessful != "" {
		severity, reason, r.Tone = SeverityWarning, ReasonChangeFailed, ToneAlert
	}
	r.finding = &Finding{Severity: severity, Reason: reason, KagentReason: c.reason, Since: c.since, SinceKnown: c.sinceKnown,
		Message: sentence(summary), Action: step(c.reason)}
	return r
}

// preparing reads a revision kagent is building. Kubernetes keeps Ready's
// transition time when only the reason changes, so an Agent blocked for an
// hour and then fixed reads ActorTemplatePending with an hour-old time: that
// time is when Ready last turned False, not when preparation began, so a
// pending build claims no duration and raises no Issue. A retrying one is
// kagent saying a golden build failed: a warning at once.
func preparing(ready condition, kind, summary, what string) Reading {
	r := Reading{State: StatePreparing, Tone: ToneDegraded, Text: "Preparing", Summary: summary, Condition: "Ready", Reason: ready.reason, Message: ready.message, Since: ready.since, SinceKnown: ready.sinceKnown}
	if ready.reason == "ActorTemplateRetrying" {
		r.Tone = ToneAlert
		message := "A golden build of " + what + " failed and kagent is retrying it"
		if ready.message != "" {
			message += ": " + ready.message
		}
		r.finding = &Finding{Severity: SeverityWarning, Reason: ReasonPreparationRetrying, KagentReason: ready.reason, Since: ready.since, SinceKnown: ready.sinceKnown,
			Message: sentence(message), Action: "Check kagent's controller logs and the " + kind + "'s ActorTemplate in Substrate for why the build fails."}
	}
	return r
}

func notReady(c condition, kind string) Reading {
	summary := "Not ready"
	if c.message != "" {
		summary += ": " + c.message
	}
	text := c.reason
	if text == "" {
		text = "Not ready"
	}
	r := Reading{State: StateNotReady, Tone: ToneAlert, Text: text, Summary: summary, Condition: "Ready", Reason: c.reason, Message: c.message, Since: c.since, SinceKnown: c.sinceKnown}
	r.finding = &Finding{Severity: SeverityWarning, Reason: ReasonNotReady, KagentReason: c.reason, Since: c.since, SinceKnown: c.sinceKnown,
		Message: sentence(summary), Action: "Check kagent's controller logs for this " + kind + "."}
	return r
}

// The references kagent names when it can't resolve one: `resolve ModelConfig
// "ns/name": not found`, `WorkerPool "ns/name" not found`
// (go/core/internal/translator/compiler.go, references.go, validation.go:19).
var (
	missingReference = regexp.MustCompile(`\b(ModelConfig|Harness|AgentTemplate|RemoteMCPServer|WorkerPool|EgressPolicy|ConfigMap|Secret)\s+"([^"]+)":?\s+not found`)
	deniedReference  = regexp.MustCompile(`\b(ModelConfig|RemoteMCPServer)\s+"([^"]+)":\s+reference from namespace "([^"]+)" denied by allowedNamespaces`)
	missingSecret    = regexp.MustCompile(`\bsecret\s+"?([^"\s]+)"?\s+not found`)
)

// referenceStep is the one next step for an unresolved reference, naming the
// object kagent says is missing when it does.
func referenceStep(message, owner string) string {
	if m := deniedReference.FindStringSubmatch(message); m != nil {
		return "Allow namespace " + m[3] + " in " + m[1] + " " + m[2] + "'s allowedNamespaces, or point the " + owner + " at one it may use."
	}
	if m := missingReference.FindStringSubmatch(message); m != nil {
		return "Create " + m[1] + " " + m[2] + " or point the " + owner + " at an existing one."
	}
	if m := missingSecret.FindStringSubmatch(message); m != nil {
		return "Create Secret " + m[1] + " or point the " + owner + " at an existing one."
	}
	return "Fix the reference kagent names, or create the object it expects."
}

func compatibleStep(string) string {
	return "Change the agent, its template or its Harness to a configuration the Harness supports."
}

func preparationStep(reason string) string {
	switch reason {
	case "ActorTemplateRejected":
		return "Fix the agent's configuration; kagent won't retry a revision Substrate rejected."
	case "ActorTemplateConflict":
		return "Check Substrate for another ActorTemplate with this revision's name; kagent won't replace it."
	}
	return "kagent retries on its own; its controller logs name the cause."
}

func reasonOr(reason, fallback string) string {
	if reason != "" {
		return reason
	}
	return fallback
}

// sentence ends text with a full stop.
func sentence(text string) string {
	text = strings.TrimSpace(text)
	if text == "" || strings.HasSuffix(text, ".") {
		return text
	}
	return text + "."
}
