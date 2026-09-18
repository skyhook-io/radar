package investigation

// Caps on the shape of the page: how many cards, hypotheses, steps and open
// items a Findings page holds, and how long its headline is. The prompt states
// the same numbers. Text fields other than the headline are never cut: a
// clamped qualification is a different claim, so a long one is kept whole and
// a list that overflows says how much it lost (OmittedEntries).
const (
	MaxRootCauseRefs = 3
	MaxEvidenceItems = 8
	MaxRuledOut      = 5
	MaxSteps         = 6
	MaxUnresolved    = 3
	MaxSummaryRunes  = 180
)

// LinkStatus is server-authored provenance for a citation.
type LinkStatus string

const (
	Linked  LinkStatus = "linked"
	Missing LinkStatus = "missing"
	Invalid LinkStatus = "invalid"
	// Unlinked marks one evidence item the binder dropped; the rest of the
	// case stands.
	Unlinked LinkStatus = "unlinked"
)

// RootCauseEvidence is the bound form of root_cause_evidence_refs. Refs are
// promoted only after every requested reference bound to one eligible result.
type RootCauseEvidence struct {
	Status LinkStatus `json:"status"`
	Refs   []string   `json:"refs,omitempty"`
}

// EvidenceRole is how the agent frames one cited Radar result. Roles order
// the Findings list and label cards; they can never hide, collapse, or recolor
// a card.
type EvidenceRole string

const (
	RoleCause    EvidenceRole = "cause"
	RoleSymptom  EvidenceRole = "symptom"
	RoleContext  EvidenceRole = "context"
	RoleDemoted  EvidenceRole = "demoted"
	RoleRulesOut EvidenceRole = "rules_out"
	// RoleBenign is the only role that says an adverse-looking result does not
	// indicate an active problem. "Excludes a hypothesis" and "is less
	// relevant here" are different statements, and neither reconciles a
	// healthy verdict with evidence that contradicts it.
	RoleBenign EvidenceRole = "benign"
)

// Roles lists every role the prompt offers and the parser accepts, in
// contract order. One half of a Go↔TS contract: the DiagnosisEvidenceRole
// union in web/src/api/diagnose.ts and EVIDENCE_ROLES in
// web/src/components/diagnose/investigationCase.ts must list exactly these.
// A role accepted here but absent there is parsed and then dropped by the
// frontend; change both sides together.
var Roles = []EvidenceRole{RoleCause, RoleSymptom, RoleContext, RoleBenign, RoleDemoted, RoleRulesOut}

// EvidenceSubject names which observation inside one tool result a claim is
// about. One diagnose call fans out into many cards, so the ref alone cannot
// place a claim. Every field is agent text copied verbatim; the frontend
// resolves it against the captured evidence and never trusts it as a fact.
type EvidenceSubject struct {
	// Group and Namespace are pointers because an explicit empty string is a
	// statement (core group, cluster scope) that must reach the frontend
	// distinct from the agent saying nothing.
	Group     *string `json:"group,omitempty"`
	Kind      string  `json:"kind"`
	Namespace *string `json:"namespace,omitempty"`
	Name      string  `json:"name"`
	Container string  `json:"container,omitempty"`
	// Stream is "current" or "previous" for a container log excerpt.
	Stream string `json:"stream,omitempty"`
	// Observation is the evidence kind (resource, logs, events, changes,
	// metrics, …) when one resource yields several observations in one result.
	Observation string `json:"observation,omitempty"`
}

// EvidenceItem is one server-bound entry of the agent's case. Only a linked
// item carries a ref. An unlinked item keeps its position so RuledOut indexes
// stay meaningful, but is never rendered.
type EvidenceItem struct {
	Status LinkStatus   `json:"status"`
	Ref    string       `json:"ref,omitempty"`
	Role   EvidenceRole `json:"role,omitempty"`
	Claim  string       `json:"claim,omitempty"`
	// Gap is the agent's own statement of what this result does not cover —
	// the half of an honest citation that a persuasive story leaves out.
	Gap     string           `json:"gap,omitempty"`
	Subject *EvidenceSubject `json:"subject,omitempty"`
}

// RuledOut is a hypothesis the agent dropped, pointing at the evidence item
// that contradicted it.
type RuledOut struct {
	Hypothesis    string `json:"hypothesis"`
	EvidenceIndex int    `json:"evidenceIndex"`
}

type Certainty string

const (
	Established Certainty = "established"
	Likely      Certainty = "likely"
	Suspected   Certainty = "suspected"
)

type StepKind string

const (
	StepMitigate    StepKind = "mitigate"
	StepVerify      StepKind = "verify"
	StepInvestigate StepKind = "investigate"
)

// Step is one typed next step. Kind says what the step is for — changing the
// cluster, settling an unresolved question, or gathering more information —
// so the UI never presents a diagnostic check as a fix, and the precondition
// travels with the step into the Apply confirmation.
type Step struct {
	Text         string   `json:"text"`
	Kind         StepKind `json:"kind"`
	Precondition string   `json:"precondition,omitempty"`
}

// Verdict is the agent's final answer as the page renders it. The JSON names
// are the wire contract with @skyhook-io/radar-app (web/src/api/diagnose.ts);
// a product embeds it in its own terminal event alongside transport state
// such as session, cost and turn count.
type Verdict struct {
	Healthy bool `json:"healthy,omitempty"`
	// Inconclusive means the agent investigated but could NOT determine an
	// answer (RBAC walls, missing data, ambiguous evidence) — distinct from
	// Healthy ("I verified it's fine") and from a root cause. The UI renders
	// this as its own honest "couldn't determine" state rather than a false
	// all-clear.
	Inconclusive bool   `json:"inconclusive,omitempty"`
	RootCause    string `json:"rootCause"`
	// Summary is the plain-language headline a person reads first; RootCause
	// stays the technical one-liner that explanations, previews and Apply
	// consume. Report is the story: the agent's prose, which may place Radar
	// results with [[radar:evidence=N]] markers the frontend resolves against
	// Evidence by index.
	Summary string `json:"summary,omitempty"`
	// Certainty qualifies Summary in the agent's own words and is rendered as
	// such; it never becomes a Radar mark.
	Certainty Certainty `json:"certainty,omitempty"`
	// Unresolved lists what would change the answer or could not be verified.
	// The UI renders it above the story fold whenever it is present, and shows
	// its absence explicitly, because a story is persuasive whether or not it
	// is right.
	Unresolved []string `json:"unresolved,omitempty"`
	// RevisesAssessment marks a follow-up answer that replaces the assessment
	// on screen. A product keeps it only on a complete verdict (SettleRevision);
	// a bare flag can never retire the assessment a reader is looking at.
	RevisesAssessment bool `json:"revisesAssessment,omitempty"`
	// Steps are the typed next steps. When present, Remediation is derived
	// from them so Apply, recommended_index and the UI all read one list.
	Steps  []Step `json:"steps,omitempty"`
	Report string `json:"report"`
	// Notes is what the agent wrote before the verdict block: its evidence
	// ledger, kept in Activity and out of Findings.
	Notes             string             `json:"notes,omitempty"`
	RootCauseEvidence *RootCauseEvidence `json:"rootCauseEvidence,omitempty"`
	// Evidence is the agent's case over Radar's facts: role + one-sentence
	// claim per cited result, bound for every assessment including healthy and
	// inconclusive ones. RootCauseEvidence is unchanged by it.
	Evidence []EvidenceItem `json:"evidence,omitempty"`
	// UnlinkedEvidence rolls up every agent evidence entry that did not reach
	// the UI: items the parser rejected or the binder could not link (each an
	// Unlinked slot in Evidence) plus entries cut by the per-case cap, which
	// get no slot at all. A consumer states the loss instead of showing a case
	// that silently shrank.
	UnlinkedEvidence int `json:"unlinkedEvidence,omitempty"`
	// EvidenceMalformed reports an evidence field that was not a list of
	// items at all. Nothing in it could be read, and no count would describe
	// how much was lost.
	EvidenceMalformed bool       `json:"evidenceMalformed,omitempty"`
	RuledOut          []RuledOut `json:"ruledOut,omitempty"`
	// OmittedEntries counts the valid steps, unresolved items and ruled-out
	// hypotheses a count cap left out, so a reader learns the list was longer
	// than the page shows. Malformed entries were never valid and are not
	// counted; evidence losses are UnlinkedEvidence.
	OmittedEntries int      `json:"omittedEntries,omitempty"`
	Remediation    []string `json:"remediation"`
	Confidence     *float64 `json:"confidence"`
	// RecommendedIndex is the 1-based index into Remediation of the single
	// step the agent recommends applying (what an Apply action performs).
	// 0/nil = no safe automatic fix. Pointing into the list (vs restating the
	// fix) keeps the UI free of duplication and binds Apply to a specific step.
	RecommendedIndex *int `json:"recommendedIndex,omitempty"`
	// RecommendedReason is a one-clause "why this step is the safe pick"
	// (reversible, lowest blast radius, …), shown under the Recommended label
	// so a non-expert understands why one step is special — not just which.
	RecommendedReason string `json:"recommendedReason,omitempty"`
}

// Structured reports whether the verdict block parsed into a conclusion — a
// root cause, remediation steps, or an explicit healthy/inconclusive call. A
// turn without one and with a failed process is a failed turn, not an answer.
func (v Verdict) Structured() bool {
	return v.RootCause != "" || len(v.Remediation) > 0 || v.Healthy || v.Inconclusive
}

// Complete reports whether the turn carries everything Findings shows as an
// assessment: a headline and a verdict. Only such a turn may replace the
// assessment on screen.
func (v Verdict) Complete() bool {
	return v.Summary != "" && (v.RootCause != "" || v.Healthy || v.Inconclusive)
}

// SettleRevision clears RevisesAssessment on an incomplete verdict and reports
// whether it did. A revision must be a complete verdict; the flag alone can
// never retire the assessment the reader is looking at.
func (v *Verdict) SettleRevision() (demoted bool) {
	if v.RevisesAssessment && !v.Complete() {
		v.RevisesAssessment = false
		return true
	}
	return false
}

// Preview is the one line a run list shows for this verdict: the headline
// when the verdict is complete, else the technical cause, else "Healthy".
// Empty when the turn was an answer rather than an assessment.
func (v Verdict) Preview() string {
	switch {
	case v.Summary != "" && (v.RootCause != "" || v.Healthy || v.Inconclusive):
		return v.Summary
	case v.RootCause != "":
		return v.RootCause
	case v.Healthy:
		return "Healthy"
	}
	return ""
}

// AsExplanation keeps only the prose of a verdict. An explanation is prose
// about a saved assessment, even if the model repeats the structured format
// from its resumed session; nothing in it may replace the assessment.
func (v Verdict) AsExplanation() Verdict {
	return Verdict{Report: v.Report}
}

// Turn is what a product recorded when it started the turn that produced a
// verdict. The frontend keeps the same rule (investigationIsAssessmentTurn).
type Turn struct {
	// Question is empty on the opening turn.
	Question    string
	Apply       bool
	Verify      bool
	Explanation bool
}

// Assesses reports whether v, produced by turn, is an assessment Findings
// shows: the opening turn, a verification, or a question whose verdict
// revised the assessment. Apply and explanation turns never assess.
func (v Verdict) Assesses(turn Turn) bool {
	if turn.Apply || turn.Explanation {
		return false
	}
	if turn.Question == "" || turn.Verify {
		return true
	}
	return v.RevisesAssessment
}
