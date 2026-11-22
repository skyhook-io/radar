package kagent

import (
	"regexp"
	"strconv"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// ReadSandboxTemplate reads a kagent SandboxTemplate's Ready condition:
// Preparing, Prepared, WorkerPoolNotFound or PreparationFailed, and
// ActorTemplateRetrying while a failed golden build is retried
// (go/core/internal/controller/sandbox_reconciler.go:254,
// sandbox_collections.go:89). Its status names no revision, so unlike an
// Agent's it can't say whether an earlier prepared one still serves.
func ReadSandboxTemplate(u *unstructured.Unstructured, now time.Time) Reading {
	if u.GetDeletionTimestamp() != nil {
		return deleting()
	}
	ready := findCondition(u, "Ready")
	if !ready.found {
		return notReported(u, now, KindSandboxTemplate)
	}
	current := withEgress(readSandboxTemplateReady(u, ready, now), readEgress(u))
	if observed, isStale := stale(u, ready); isStale {
		return staleOf(current, observed, u.GetGeneration(), KindSandboxTemplate)
	}
	return current
}

func readSandboxTemplateReady(u *unstructured.Unstructured, ready condition, now time.Time) Reading {
	switch ready.status {
	case "True":
		return Reading{State: StateReady, Tone: ToneHealthy, Text: "Ready", Summary: "Ready to start sandboxes", Condition: "Ready", Reason: ready.reason, Message: ready.message, Since: ready.since, SinceKnown: ready.sinceKnown}
	case "False":
	default:
		return Reading{State: StateUnknown, Tone: ToneUnknown, Text: "Unknown", Summary: "kagent can't tell whether the template is ready", Condition: "Ready", Reason: ready.reason, Message: ready.message}
	}
	switch {
	case ready.reason == "Preparing" || preparingReasons[ready.reason]:
		summary := "Preparing the sandbox runtime"
		if ready.reason == "ActorTemplateRetrying" {
			summary = "Retrying a failed build of the sandbox runtime"
		}
		return preparing(ready, now, KindSandboxTemplate, summary, "kagent has been preparing the sandbox runtime")
	case ready.reason == "WorkerPoolNotFound":
		pool, _, _ := unstructured.NestedString(u.Object, "spec", "substrate", "workerPoolRef", "name")
		ref := u.GetNamespace() + "/" + pool
		summary := "Blocked: WorkerPool " + ref + " not found"
		if pool == "" {
			summary = "Blocked: " + ready.message
		}
		r := Reading{State: StateBlocked, Tone: ToneUnhealthy, Text: "Blocked", Summary: summary, Condition: "Ready", Reason: ready.reason, Message: ready.message, Since: ready.since, SinceKnown: ready.sinceKnown}
		step := "Create the WorkerPool or point spec.substrate.workerPoolRef at an existing one."
		if pool != "" {
			step = "Create WorkerPool " + ref + " or point spec.substrate.workerPoolRef at an existing one."
		}
		r.finding = &Finding{Severity: SeverityCritical, Reason: ready.reason, Since: ready.since, SinceKnown: ready.sinceKnown, Message: sentence(summary) + " " + step}
		return r
	case ready.reason == "PreparationFailed":
		summary := "Failed"
		if ready.message != "" {
			summary += ": " + ready.message
		}
		r := Reading{State: StateFailed, Tone: ToneUnhealthy, Text: "Failed", Summary: summary, Condition: "Ready", Reason: ready.reason, Message: ready.message, Since: ready.since, SinceKnown: ready.sinceKnown}
		r.finding = &Finding{Severity: SeverityCritical, Reason: ready.reason, Since: ready.since, SinceKnown: ready.sinceKnown, Message: withStep(summary, ready.message, "Check kagent's controller logs for the cause.")}
		return r
	}
	return notReady(ready, KindSandboxTemplate)
}

// ReadModelConfig reads a ModelConfig's Accepted and ResolvedRefs conditions
// (go/core/internal/controller/modelconfig.go:60). Agents that use a
// ModelConfig kagent can't resolve report it as their own blocked reference,
// so the ModelConfig's own Issue stays a warning.
func ReadModelConfig(u *unstructured.Unstructured, now time.Time) Reading {
	if u.GetDeletionTimestamp() != nil {
		return deleting()
	}
	accepted := findCondition(u, "Accepted")
	refs := findCondition(u, "ResolvedRefs")
	if !accepted.found && !refs.found {
		return notReported(u, now, KindModelConfig)
	}
	var current Reading
	switch {
	case accepted.status == "False":
		current = configProblem(StateFailed, "Rejected", "Not accepted", "Accepted", accepted, "Fix the ModelConfig's spec; agents using it stay blocked until kagent accepts it.")
	case refs.status == "False":
		current = configProblem(StateBlocked, "Blocked", "References not resolved", "ResolvedRefs", refs, modelConfigStep(u, refs))
	case accepted.status == "True" && refs.status == "True":
		current = Reading{State: StateReady, Tone: ToneHealthy, Text: "Accepted", Summary: "Accepted; its Secrets and ConfigMaps resolve", Condition: "ResolvedRefs", Reason: refs.reason, Message: refs.message, Since: refs.since, SinceKnown: refs.sinceKnown}
	default:
		current = Reading{State: StateUnknown, Tone: ToneUnknown, Text: "Unknown", Summary: "kagent can't tell whether the ModelConfig is usable", Condition: "Accepted", Reason: accepted.reason, Message: accepted.message}
	}
	basis := refs
	if !basis.found {
		basis = accepted
	}
	if observed, isStale := stale(u, basis); isStale {
		return staleOf(current, observed, u.GetGeneration(), KindModelConfig)
	}
	return current
}

// secretKeyMissing is kagent's message for a Secret without the key a
// ModelConfig names (go/core/internal/translator/modelconfig.go:99).
var secretKeyMissing = regexp.MustCompile(`\bsecret\s+"?([^"\s]+)"?\s+does not contain key\s+"([^"]+)"`)

func modelConfigStep(u *unstructured.Unstructured, refs condition) string {
	ns := u.GetNamespace()
	if m := secretKeyMissing.FindStringSubmatch(refs.message); m != nil {
		return "Add key " + m[2] + " to Secret " + ns + "/" + m[1] + ", or point the ModelConfig at a key it has."
	}
	if m := missingSecret.FindStringSubmatch(refs.message); m != nil {
		field := "the reference"
		if refs.reason == "APIKeySecretNotFound" {
			field = "apiKeySecret"
		}
		return "Create Secret " + ns + "/" + m[1] + " or point " + field + " at an existing one."
	}
	return "Create the Secret or ConfigMap kagent names, or change the reference."
}

func configProblem(state State, text, lead, condType string, c condition, step string) Reading {
	summary := lead
	if c.message != "" {
		summary += ": " + c.message
	}
	r := Reading{State: state, Tone: ToneAlert, Text: text, Summary: summary, Condition: condType, Reason: c.reason, Message: c.message, Since: c.since, SinceKnown: c.sinceKnown}
	r.finding = &Finding{Severity: SeverityWarning, Reason: reasonOr(c.reason, condType+"=False"), Since: c.since, SinceKnown: c.sinceKnown, Message: sentence(summary) + " " + step}
	return r
}

// ReadModelProviderConfig reads a ModelProviderConfig's Ready condition. The
// type declares it, but no kagent 1.0 controller reconciles the kind, so its
// status stays empty; that reads as no status, neither a problem nor an Issue.
func ReadModelProviderConfig(u *unstructured.Unstructured, _ time.Time) Reading {
	if u.GetDeletionTimestamp() != nil {
		return deleting()
	}
	ready := findCondition(u, "Ready")
	if !ready.found {
		return Reading{State: StateNotReported, Tone: ToneNeutral, Text: "No status", Summary: "kagent doesn't report status for this kind"}
	}
	var current Reading
	switch ready.status {
	case "True":
		current = Reading{State: StateReady, Tone: ToneHealthy, Text: "Ready", Summary: "Ready for model discovery", Condition: "Ready", Reason: ready.reason, Message: ready.message, Since: ready.since, SinceKnown: ready.sinceKnown}
	case "False":
		current = configProblem(StateNotReady, reasonOr(ready.reason, "Not ready"), "Not ready", "Ready", ready, "Check the provider endpoint and the discovery Secret it names.")
	default:
		current = Reading{State: StateUnknown, Tone: ToneUnknown, Text: "Unknown", Summary: "kagent can't tell whether the provider is ready", Condition: "Ready", Reason: ready.reason, Message: ready.message}
	}
	if observed, isStale := stale(u, ready); isStale {
		return staleOf(current, observed, u.GetGeneration(), KindModelProviderConfig)
	}
	return current
}

// ReadRemoteMCPServer reads a RemoteMCPServer's Accepted condition, which
// reports tool discovery (go/core/internal/controller/remotemcpserver/reconciler.go:92-129).
// DiscoveryDisabled is True: discovery is off by label or because the server
// authenticates as the executing Actor, and each runtime discovers tools itself.
func ReadRemoteMCPServer(u *unstructured.Unstructured, now time.Time) Reading {
	if u.GetDeletionTimestamp() != nil {
		return deleting()
	}
	accepted := findCondition(u, "Accepted")
	if !accepted.found {
		return notReported(u, now, KindRemoteMCPServer)
	}
	var current Reading
	switch {
	case accepted.status == "True" && accepted.reason == "DiscoveryDisabled":
		current = Reading{State: StateDiscoveryOff, Tone: ToneNeutral, Text: "Discovery off", Summary: "Tools are discovered at run time", Condition: "Accepted", Reason: accepted.reason, Message: accepted.message, Since: accepted.since, SinceKnown: accepted.sinceKnown}
		if accepted.message != "" {
			current.Summary = accepted.message
		}
	case accepted.status == "True":
		current = Reading{State: StateReady, Tone: ToneHealthy, Text: "Ready", Summary: "Discovered " + toolCount(DiscoveredTools(u)), Condition: "Accepted", Reason: accepted.reason, Message: accepted.message, Since: accepted.since, SinceKnown: accepted.sinceKnown}
	case accepted.status == "False" && (accepted.reason == "DiscoveryFailed" || accepted.reason == "InvalidDiscovery"):
		// An Agent that uses it still reads Ready: kagent drops the tools it
		// can't discover, so this Issue is where the failure shows.
		step := "Agents that use it run without its tools; check its URL and credentials, and that kagent's controller can reach it."
		if accepted.reason == "InvalidDiscovery" {
			step = "Agents that use it run without its tools; check the tool list it returns, which kagent rejected."
		}
		current = configProblem(StateFailed, "Discovery failed", "Tool discovery failed", "Accepted", accepted, step)
	case accepted.status == "False":
		current = configProblem(StateNotReady, reasonOr(accepted.reason, "Not accepted"), "Not accepted", "Accepted", accepted, "Check kagent's controller logs for this RemoteMCPServer.")
	default:
		current = Reading{State: StateUnknown, Tone: ToneUnknown, Text: "Unknown", Summary: "kagent can't tell whether discovery works", Condition: "Accepted", Reason: accepted.reason, Message: accepted.message}
	}
	if observed, isStale := stale(u, accepted); isStale {
		return staleOf(current, observed, u.GetGeneration(), KindRemoteMCPServer)
	}
	return current
}

// DiscoveredTools are the tool names kagent's discovery published in status.
func DiscoveredTools(u *unstructured.Unstructured) []string {
	tools, _, _ := unstructured.NestedSlice(u.Object, "status", "discoveredTools")
	out := make([]string, 0, len(tools))
	for _, t := range tools {
		if m, ok := t.(map[string]any); ok {
			if name, _ := m["name"].(string); name != "" {
				out = append(out, name)
			}
		}
	}
	return out
}

func toolCount(tools []string) string {
	if len(tools) == 1 {
		return "1 tool"
	}
	return strconv.Itoa(len(tools)) + " tools"
}
