// Package kagent holds Radar's readings of kagent (api.kagent.dev) objects:
// how an Agent, a SandboxTemplate, a ModelConfig, a ModelProviderConfig and a
// RemoteMCPServer read, and which of those readings are Issues. Radar's Issues
// and AI summaries decide with the rules here; k8s-ui's
// resource-utils-kagent.ts mirrors the readings, and testdata/status.json pins
// both. AgentTemplate, Harness and EgressPolicy carry no status, so Radar makes
// no health claim about them.
package kagent

import (
	"strconv"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/skyhook-io/radar/pkg/timeutil"
)

// Group is kagent 1.x's API group. The 0.x group, kagent.dev, is not read here.
const Group = "api.kagent.dev"

const (
	KindAgent               = "Agent"
	KindAgentTemplate       = "AgentTemplate"
	KindHarness             = "Harness"
	KindModelConfig         = "ModelConfig"
	KindModelProviderConfig = "ModelProviderConfig"
	KindRemoteMCPServer     = "RemoteMCPServer"
	KindSandboxTemplate     = "SandboxTemplate"
	KindEgressPolicy        = "EgressPolicy"
)

// IssueAfter is how long an Agent or SandboxTemplate may stay Preparing, or
// any kind may go unreported, before Radar's Issues raise it. Preparing builds
// a golden snapshot, which pulls images and boots the runtime, so a shorter
// window would make every cold image pull an incident.
const IssueAfter = 30 * time.Minute

// Tone is the k8s-ui HealthLevel a reading presents with.
type Tone string

const (
	ToneHealthy   Tone = "healthy"
	ToneDegraded  Tone = "degraded"
	ToneAlert     Tone = "alert"
	ToneUnhealthy Tone = "unhealthy"
	ToneNeutral   Tone = "neutral"
	ToneUnknown   Tone = "unknown"
)

// State is what a reading says about the object.
type State string

const (
	StateDeleting     State = "deleting"
	StateNotReported  State = "notReported"
	StateStale        State = "stale"
	StateReady        State = "ready"
	StatePreparing    State = "preparing"
	StateBlocked      State = "blocked"
	StateFailed       State = "failed"
	StateNotReady     State = "notReady"
	StateUnknown      State = "unknown"
	StateDiscoveryOff State = "discoveryOff"
)

// Severity is an Issue's severity, in the Issues vocabulary.
type Severity string

const (
	SeverityWarning  Severity = "warning"
	SeverityCritical Severity = "critical"
)

// Reading is how one kagent object reads. Text and Tone are the badge: on a
// Ready object whose egress grants are not applied they say so, while State
// and Summary keep the readiness verdict.
type Reading struct {
	State State
	Tone  Tone
	Text  string
	// Summary is one sentence for the drawer and AI summaries, kagent's own
	// words where it gives any.
	Summary string
	// Condition, Reason and Message are the condition the verdict comes from.
	Condition, Reason, Message string
	Since                      time.Time
	SinceKnown                 bool
	Egress                     *Egress
	// Stale: the status was written for an earlier generation; Previous is
	// what it said then.
	Previous *Reading
	finding  *Finding
}

// Egress is a False EgressPoliciesApplied condition. kagent reports it
// independently of Ready: the runtime starts without the grants it names.
type Egress struct {
	Reason, Message, Summary string
	Since                    time.Time
	SinceKnown               bool
}

// Finding is one Issue a reading raises.
type Finding struct {
	Severity    Severity
	Reason      string
	Message     string
	Since       time.Time
	SinceKnown  bool
	Fingerprint string
}

// IsGroup reports whether group is kagent's.
func IsGroup(group string) bool { return group == Group }

// Read returns the reading of a kagent object that reports status. ok is false
// for the spec-only kinds and anything outside api.kagent.dev.
func Read(u *unstructured.Unstructured, now time.Time) (Reading, bool) {
	if u == nil || groupOf(u.GetAPIVersion()) != Group {
		return Reading{}, false
	}
	switch u.GetKind() {
	case KindAgent:
		return ReadAgent(u, now), true
	case KindSandboxTemplate:
		return ReadSandboxTemplate(u, now), true
	case KindModelConfig:
		return ReadModelConfig(u, now), true
	case KindModelProviderConfig:
		return ReadModelProviderConfig(u, now), true
	case KindRemoteMCPServer:
		return ReadRemoteMCPServer(u, now), true
	}
	return Reading{}, false
}

// ReportsStatus reports whether Radar reads status for a kagent kind, so the
// generic condition walk leaves it alone.
func ReportsStatus(kind string) bool {
	switch kind {
	case KindAgent, KindSandboxTemplate, KindModelConfig, KindModelProviderConfig, KindRemoteMCPServer:
		return true
	}
	return false
}

func groupOf(apiVersion string) string {
	if group, _, found := strings.Cut(apiVersion, "/"); found {
		return group
	}
	return ""
}

type condition struct {
	status, reason, message string
	observedGeneration      int64
	since                   time.Time
	sinceKnown              bool
	found                   bool
}

func findCondition(u *unstructured.Unstructured, condType string) condition {
	conds, _, _ := unstructured.NestedSlice(u.Object, "status", "conditions")
	for _, raw := range conds {
		c, ok := raw.(map[string]any)
		if !ok || c["type"] != condType {
			continue
		}
		out := condition{found: true}
		out.status, _ = c["status"].(string)
		out.reason, _ = c["reason"].(string)
		out.message, _ = c["message"].(string)
		// kagent quotes raw errors, a discovery error's URL with it, into
		// messages that reach Issues and AI summaries.
		out.message = RedactURLsInText(strings.TrimSpace(out.message))
		out.observedGeneration, _, _ = unstructured.NestedInt64(c, "observedGeneration")
		if ts, _ := c["lastTransitionTime"].(string); ts != "" {
			if t, err := time.Parse(time.RFC3339, ts); err == nil {
				out.since, out.sinceKnown = t, true
			}
		}
		return out
	}
	return condition{}
}

// observedGeneration is the generation the status was written for: the
// status's own field, else the given condition's.
func observedGeneration(u *unstructured.Unstructured, c condition) int64 {
	if g, found, _ := unstructured.NestedInt64(u.Object, "status", "observedGeneration"); found && g > 0 {
		return g
	}
	return c.observedGeneration
}

func stale(u *unstructured.Unstructured, c condition) (observed int64, isStale bool) {
	observed = observedGeneration(u, c)
	return observed, u.GetGeneration() > 0 && observed > 0 && observed < u.GetGeneration()
}

// ShortRevision is the 12-hex prefix kagent uses in names and labels.
func ShortRevision(revision string) string {
	if len(revision) > 12 {
		return revision[:12]
	}
	return revision
}

func deleting() Reading {
	return Reading{State: StateDeleting, Tone: ToneDegraded, Text: "Deleting", Summary: "Deleting"}
}

func notReported(u *unstructured.Unstructured, now time.Time, kind string) Reading {
	r := Reading{State: StateNotReported, Tone: ToneUnknown, Text: "Not reported", Summary: "kagent hasn't reported status yet"}
	created := u.GetCreationTimestamp().Time
	if !created.IsZero() {
		r.Since, r.SinceKnown = created, true
		if age := now.Sub(created); age > IssueAfter {
			r.finding = &Finding{
				Severity: SeverityWarning, Reason: "NotReported", Since: created, SinceKnown: true,
				Message: "kagent hasn't reported on this " + kind + " in the " + formatAge(age) + " since it was created. Check that kagent's controller is running and watches this namespace.",
			}
		}
	}
	return r
}

// staleOf wraps the reading of a status written for an earlier generation. A
// spec edit is not a recovery, so a failure it reported stays an Issue,
// qualified and at most a warning, until kagent judges the new generation.
func staleOf(prev Reading, observed, generation int64, kind string) Reading {
	r := Reading{
		State: StateStale, Tone: ToneUnknown, Text: "Updating",
		Summary:  "Status not yet updated for the latest change",
		Message:  "Last reported for generation " + itoa(observed) + ": " + prev.Summary,
		Previous: &prev,
	}
	qualify := "Reported for generation " + itoa(observed) + "; the " + kind + " is now at generation " + itoa(generation) + ". "
	if prev.finding != nil {
		f := *prev.finding
		f.Severity = SeverityWarning
		f.Message = qualify + f.Message
		r.finding = &f
	}
	if prev.Egress != nil {
		e := *prev.Egress
		r.Egress = &e
	}
	return r
}

// Findings are the Issues a reading raises: its verdict's, then its egress
// grants'. Egress Issues carry their own fingerprint, so both can stand.
func (r Reading) Findings() []Finding {
	var out []Finding
	if r.finding != nil {
		out = append(out, *r.finding)
	}
	if r.Egress != nil {
		message := r.Egress.Summary + ". " + egressStep(r.Egress.Reason)
		if r.State == StateStale {
			message = "Reported for an earlier generation. " + message
		}
		out = append(out, Finding{Severity: SeverityWarning, Reason: r.Egress.Reason, Message: message, Since: r.Egress.Since, SinceKnown: r.Egress.SinceKnown, Fingerprint: "egress"})
	}
	return out
}

func readEgress(u *unstructured.Unstructured) *Egress {
	c := findCondition(u, "EgressPoliciesApplied")
	if !c.found || c.status != "False" {
		return nil
	}
	reason := c.reason
	if reason == "" {
		reason = "EgressNotApplied"
	}
	summary := "Egress not applied"
	if c.message != "" {
		summary += ": " + c.message
	}
	return &Egress{Reason: reason, Message: c.message, Summary: summary, Since: c.since, SinceKnown: c.sinceKnown}
}

// withEgress puts the egress grants on the badge of a Ready object, whose
// readiness alone would read healthy.
func withEgress(r Reading, e *Egress) Reading {
	r.Egress = e
	if e != nil && r.Tone == ToneHealthy {
		r.Text, r.Tone = "Egress not applied", ToneAlert
	}
	return r
}

func egressStep(reason string) string {
	switch reason {
	case "EgressPolicyNotFound":
		return "Create the EgressPolicy or remove it from egressPolicyRefs."
	case "InvalidEgressPolicy":
		return "Fix the EgressPolicy kagent names; the other policies' grants still apply."
	case "EgressGrantConflict":
		return "Resolve the conflicting grant; kagent holds new grants until then."
	}
	return "Check the EgressPolicies in egressPolicyRefs."
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

func formatAge(d time.Duration) string { return timeutil.FormatAgeShort(d) }
