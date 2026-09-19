package investigation

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

var (
	jsonBlockRe = regexp.MustCompile("(?s)```json\\s*(\\{.*?\\})\\s*```")
	// The ledger marks each tool result with [[radar:evidence-ref=ev_…]]; an
	// agent that copies that form into the story meant the placement marker,
	// so a ref that names a cited item becomes [[radar:evidence=N]] and reads
	// the same everywhere downstream.
	storyRefMarkerRe = regexp.MustCompile(`\[\[radar:evidence-ref=(ev_[a-z2-7]{26,128}_[a-z2-7]{26,128})(\|compact)?\]\]`)
	// No marker of any kind belongs in the headline fields.
	anyStoryMarkerRe = regexp.MustCompile(` ?\[\[radar:[^\]]*\]\]`)
	// placementMarkerRe matches the story's placements. They mean nothing to
	// a model reading the story back, so prompts strip them.
	placementMarkerRe = regexp.MustCompile(`\[\[radar:evidence(?:=\d+|-ref=[a-z0-9_]+)(?:\|compact)?\]\]`)
)

var certainties = map[Certainty]struct{}{Established: {}, Likely: {}, Suspected: {}}

var stepKinds = map[StepKind]struct{}{StepMitigate: {}, StepVerify: {}, StepInvestigate: {}}

var roles = func() map[EvidenceRole]struct{} {
	m := make(map[EvidenceRole]struct{}, len(Roles))
	for _, role := range Roles {
		m[role] = struct{}{}
	}
	return m
}()

// A non-revising follow-up may answer with nothing but revises_assessment,
// so that field marks a verdict block too; without it the block would read as
// quoted prose and its extension fields would be lost.
// Every subject field is an identifier the frontend resolves against a
// captured result; nothing a cluster names is longer than a DNS name.
const maxIdentifierRunes = 253

var verdictFields = []string{"root_cause", "summary", "healthy", "inconclusive", "steps", "evidence", "remediation", "revises_assessment"}

// Parsed is the model's final text as read, before binding. Citations are the
// untrusted part: the refs the agent asked to cite. They never cross an API
// boundary; Bind replaces them with server-authored provenance.
//
// Extensions are the verdict block's fields outside the contract, handed back
// unread. A product that asks the model for fields of its own (a notification
// line, say) states them in its prompt extension and reads them here, so the
// block stays one object the model writes once.
type Parsed struct {
	Verdict    Verdict
	Citations  Citations
	Extensions map[string]json.RawMessage
}

// Citations is the agent's citation request, opaque to callers. A product
// carries it from the parser to Bind and nowhere else.
type Citations struct {
	rootCause referenceRequest
	theCase   caseRequest
}

type referenceRequest struct {
	present bool
	invalid bool
	refs    []string
}

type caseItemRequest struct {
	valid   bool
	ref     string
	role    EvidenceRole
	claim   string
	gap     string
	subject *EvidenceSubject
}

// caseRequest is the untrusted evidence/ruled_out part of the agent's JSON.
type caseRequest struct {
	items    []caseItemRequest
	ruledOut []RuledOut
	// dropped counts entries cut by the per-case cap. Those get no slot in
	// items, so nothing downstream could otherwise see them go.
	dropped int
	// malformed is set when the agent sent an evidence field that is not a
	// list, so the whole case was unreadable and no count describes it.
	malformed bool
	// overflowRuledOut holds the hypotheses past the cap; the binder counts
	// the ones whose item linked, since only those would have been shown.
	overflowRuledOut []RuledOut
}

func isVerdictBlock(block string) bool {
	var fields map[string]json.RawMessage
	if json.Unmarshal([]byte(block), &fields) != nil {
		return false
	}
	for _, field := range verdictFields {
		if _, ok := fields[field]; ok {
			return true
		}
	}
	return false
}

// Parse assembles the verdict from the agent's final text. The last fenced
// json block that carries a verdict field is the verdict and a divider: prose
// after it is the story, prose before it the agent's working notes. With
// nothing after the block (the older trailing-block shape) the prose before
// it is the story. Absent any block, the whole text is the report.
//
// Deliberately no RootCause is fabricated from free text: a reply with no
// structured root_cause (e.g. "the resource looks healthy", or a clarifying
// question) must not render under the alarming "ROOT CAUSE" anchor. The UI
// shows such replies as a neutral analysis (Report carries the full text).
func Parse(text string) Parsed {
	text = strings.TrimSpace(text)
	p := Parsed{Verdict: Verdict{Report: text}}
	d := &p.Verdict
	locs := jsonBlockRe.FindAllStringSubmatchIndex(text, -1)
	if len(locs) == 0 {
		return p
	}
	// The story follows the verdict and may quote a patch or a spec in a json
	// fence of its own, so the verdict is the last block that carries a
	// verdict field, not merely the last block; an answer whose fences carry
	// none is prose quoting something, and keeps every fence.
	var last []int
	for i := len(locs) - 1; i >= 0; i-- {
		if isVerdictBlock(text[locs[i][2]:locs[i][3]]) {
			last = locs[i]
			break
		}
	}
	if last == nil {
		return p
	}
	var parsed struct {
		Healthy           *bool           `json:"healthy"`
		Inconclusive      *bool           `json:"inconclusive"`
		RootCause         string          `json:"root_cause"`
		EvidenceRefs      json.RawMessage `json:"root_cause_evidence_refs"`
		Evidence          json.RawMessage `json:"evidence"`
		RuledOut          json.RawMessage `json:"ruled_out"`
		Remediation       []string        `json:"remediation"`
		RecommendedIndex  *int            `json:"recommended_index"`
		RecommendedReason string          `json:"recommended_reason"`
		Confidence        *float64        `json:"confidence"`
		Summary           string          `json:"summary"`
		Certainty         string          `json:"certainty"`
		Unresolved        []string        `json:"unresolved"`
		Steps             json.RawMessage `json:"steps"`
		RevisesAssessment *bool           `json:"revises_assessment"`
	}
	block := []byte(text[last[2]:last[3]])
	if json.Unmarshal(block, &parsed) != nil {
		return p
	}
	p.Extensions = extensionFields(block)
	if parsed.Healthy != nil {
		d.Healthy = *parsed.Healthy
	}
	if parsed.Inconclusive != nil {
		d.Inconclusive = *parsed.Inconclusive
	}
	d.RootCause = parsed.RootCause
	p.Citations.rootCause = parseReferenceRequest(parsed.EvidenceRefs)
	p.Citations.theCase = parseCaseRequest(parsed.Evidence, parsed.RuledOut)
	d.Remediation = parsed.Remediation
	d.Confidence = parsed.Confidence
	before := strings.TrimSpace(jsonBlockRe.ReplaceAllString(text[:last[0]], ""))
	if after := strings.TrimSpace(text[last[1]:]); after != "" {
		d.Notes = before
		d.Report = after
	} else {
		d.Report = before
	}
	d.Report = canonicalStoryMarkers(d.Report, p.Citations.theCase.items)
	d.RootCause = stripStoryMarkers(d.RootCause)
	d.Summary = clampSummary(stripStoryMarkers(parsed.Summary), MaxSummaryRunes)
	if certainty := Certainty(strings.ToLower(strings.TrimSpace(parsed.Certainty))); certainty != "" {
		if _, known := certainties[certainty]; known && d.Summary != "" {
			d.Certainty = certainty
		}
	}
	for _, item := range parsed.Unresolved {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if len(d.Unresolved) == MaxUnresolved {
			d.OmittedEntries++
			continue
		}
		d.Unresolved = append(d.Unresolved, item)
	}
	var stepOrigins []int
	var omittedSteps int
	d.Steps, stepOrigins, omittedSteps = parseStepsIndexed(parsed.Steps)
	d.OmittedEntries += omittedSteps
	if len(d.Steps) > 0 {
		// One action list: the typed steps are authoritative and the legacy
		// remediation array is derived from them, so Apply, recommended_index
		// and the UI cannot disagree about a step.
		d.Remediation = make([]string, len(d.Steps))
		for i, step := range d.Steps {
			d.Remediation[i] = step.Text
		}
	}
	if parsed.RevisesAssessment != nil {
		d.RevisesAssessment = *parsed.RevisesAssessment
	}
	// Normalize verdict precedence so the object can't be self-contradictory.
	// A root cause wins over both flags, and an explicit "couldn't tell" wins
	// over healthy ("absence of evidence is not health"). A step never
	// establishes a cause: with typed steps, "cause unresolved; roll back to
	// restore service" is a legitimate verdict, so only the legacy
	// remediation-only shape keeps clearing inconclusive.
	if d.RootCause != "" || (len(d.Steps) == 0 && len(d.Remediation) > 0) {
		d.Healthy = false
		d.Inconclusive = false
	} else if d.Inconclusive {
		d.Healthy = false
	}
	// "Established" means nothing left would change the answer, which is
	// exactly what a non-empty unresolved list denies; the two cannot both be
	// true, and the caveats are the part the reader must not lose.
	if d.Certainty == Established && len(d.Unresolved) > 0 {
		d.Certainty = Likely
	}
	// Keep the index only when it points at a real step, and with typed steps
	// only at one Apply may perform: a mitigation, and one whose applicability
	// is not itself in question — a step that is right "only if" something
	// unverified holds is not a one-click fix. The agent's index counts the
	// steps it wrote, including any the parser dropped; it is translated to
	// the surviving list and cleared when the entry it named did not survive,
	// so Apply never runs a neighbour of the designated step.
	if parsed.RecommendedIndex != nil && *parsed.RecommendedIndex >= 1 {
		idx := *parsed.RecommendedIndex
		if len(d.Steps) > 0 {
			idx = 0
			for pos, origin := range stepOrigins {
				if origin == *parsed.RecommendedIndex-1 {
					idx = pos + 1
				}
			}
		}
		if idx >= 1 && idx <= len(d.Remediation) &&
			(len(d.Steps) == 0 ||
				(d.Steps[idx-1].Kind == StepMitigate && d.Steps[idx-1].Precondition == "")) {
			d.RecommendedIndex = &idx
			d.RecommendedReason = strings.TrimSpace(parsed.RecommendedReason)
		}
	}
	return p
}

var contractFields = map[string]struct{}{
	"healthy": {}, "inconclusive": {}, "root_cause": {}, "root_cause_evidence_refs": {},
	"evidence": {}, "ruled_out": {}, "remediation": {}, "recommended_index": {},
	"recommended_reason": {}, "confidence": {}, "summary": {}, "certainty": {},
	"unresolved": {}, "steps": {}, "revises_assessment": {},
}

func extensionFields(block []byte) map[string]json.RawMessage {
	var fields map[string]json.RawMessage
	if json.Unmarshal(block, &fields) != nil {
		return nil
	}
	for name := range fields {
		if _, contract := contractFields[name]; contract {
			delete(fields, name)
		}
	}
	if len(fields) == 0 {
		return nil
	}
	return fields
}

// parseStepsIndexed also returns, for each kept step, its index in the
// agent's original array, so an index the agent wrote can be translated, and
// how many valid steps the cap left out.
func parseStepsIndexed(raw json.RawMessage) ([]Step, []int, int) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil, 0
	}
	// Entries are read one at a time, like evidence items, so one malformed
	// step neither hides its valid neighbours nor their loss.
	var rawSteps []json.RawMessage
	if json.Unmarshal(raw, &rawSteps) != nil {
		return nil, nil, 0
	}
	var steps []Step
	var origins []int
	omitted := 0
	for origin, rawStep := range rawSteps {
		var item struct {
			Text         string `json:"text"`
			Kind         string `json:"kind"`
			Precondition string `json:"precondition"`
		}
		if json.Unmarshal(rawStep, &item) != nil {
			continue
		}
		text := strings.TrimSpace(item.Text)
		kind := StepKind(strings.ToLower(strings.TrimSpace(item.Kind)))
		if text == "" {
			continue
		}
		if _, known := stepKinds[kind]; !known {
			continue
		}
		if len(steps) == MaxSteps {
			omitted++
			continue
		}
		steps = append(steps, Step{
			Text:         text,
			Kind:         kind,
			Precondition: strings.TrimSpace(item.Precondition),
		})
		origins = append(origins, origin)
	}
	return steps, origins, omitted
}

// clampSummary keeps a headline whole: an over-long summary is cut at the
// last sentence end inside the budget, then at the last word, never mid-word.
// The cut is visible as an ellipsis, so a reader knows there was more.
func clampSummary(value string, limit int) string {
	if utf8.RuneCountInString(value) <= limit {
		return value
	}
	runes := []rune(value)[:limit]
	text := string(runes)
	// A sentence ends at punctuation followed by a space; "6.5" and
	// "missing.conf" are not boundaries.
	end := -1
	for _, mark := range []string{". ", "! ", "? "} {
		if i := strings.LastIndex(text, mark); i > end {
			end = i
		}
	}
	if end > limit/2 {
		return strings.TrimSpace(text[:end+1])
	}
	if space := strings.LastIndex(text, " "); space > limit/2 {
		return strings.TrimSpace(text[:space]) + "…"
	}
	return strings.TrimSpace(text) + "…"
}

func parseReferenceRequest(raw json.RawMessage) referenceRequest {
	if len(raw) == 0 {
		return referenceRequest{}
	}
	request := referenceRequest{present: true}
	if string(raw) == "null" {
		request.invalid = true
		return request
	}
	var refs []string
	if json.Unmarshal(raw, &refs) != nil || len(refs) > MaxRootCauseRefs {
		request.invalid = true
		return request
	}
	seen := make(map[string]struct{}, len(refs))
	for _, ref := range refs {
		if !refRe.MatchString(ref) {
			request.invalid = true
			return request
		}
		if _, duplicate := seen[ref]; duplicate {
			request.invalid = true
			return request
		}
		seen[ref] = struct{}{}
	}
	request.refs = refs
	return request
}

// parseCaseRequest reads the agent's evidence items and ruled-out hypotheses.
// A malformed item is kept at its index as invalid so ruled_out indexes still
// point where the agent meant; it never invalidates its siblings or the legacy
// root_cause_evidence_refs. Over-cap arrays are cut, not rejected.
func parseCaseRequest(evidenceRaw, ruledOutRaw json.RawMessage) caseRequest {
	var request caseRequest
	var rawItems []json.RawMessage
	if len(evidenceRaw) > 0 && string(evidenceRaw) != "null" &&
		json.Unmarshal(evidenceRaw, &rawItems) != nil {
		// The agent sent something for evidence that is not a list of items.
		// There is no honest count of what it meant to say, so the loss is
		// reported as a fact rather than a number.
		request.malformed = true
	}
	if len(evidenceRaw) > 0 && json.Unmarshal(evidenceRaw, &rawItems) == nil {
		if len(rawItems) > MaxEvidenceItems {
			// Items past the cap get no slot in Evidence at all, so their loss
			// is only visible if counted here.
			request.dropped = len(rawItems) - MaxEvidenceItems
			rawItems = rawItems[:MaxEvidenceItems]
		}
		for _, raw := range rawItems {
			request.items = append(request.items, parseCaseItem(raw))
		}
	}
	var rawRuledOut []json.RawMessage
	if len(ruledOutRaw) > 0 && json.Unmarshal(ruledOutRaw, &rawRuledOut) == nil {
		for _, raw := range rawRuledOut {
			entry, ok := parseRuledOut(raw)
			if !ok {
				continue
			}
			if len(request.ruledOut) == MaxRuledOut {
				request.overflowRuledOut = append(request.overflowRuledOut, entry)
				continue
			}
			request.ruledOut = append(request.ruledOut, entry)
		}
	}
	return request
}

func parseRuledOut(raw json.RawMessage) (RuledOut, bool) {
	var parsed struct {
		Hypothesis    string `json:"hypothesis"`
		EvidenceIndex *int   `json:"evidence_index"`
	}
	if json.Unmarshal(raw, &parsed) != nil {
		return RuledOut{}, false
	}
	hypothesis := strings.TrimSpace(parsed.Hypothesis)
	if hypothesis == "" || parsed.EvidenceIndex == nil || *parsed.EvidenceIndex < 0 {
		return RuledOut{}, false
	}
	return RuledOut{Hypothesis: hypothesis, EvidenceIndex: *parsed.EvidenceIndex}, true
}

func parseCaseItem(raw json.RawMessage) caseItemRequest {
	var parsed struct {
		Ref     string          `json:"ref"`
		Role    string          `json:"role"`
		Claim   string          `json:"claim"`
		Gap     string          `json:"gap"`
		Subject json.RawMessage `json:"subject"`
	}
	if json.Unmarshal(raw, &parsed) != nil {
		return caseItemRequest{}
	}
	ref := unwrapRef(parsed.Ref)
	if !refRe.MatchString(ref) {
		return caseItemRequest{}
	}
	role := EvidenceRole(strings.ToLower(strings.TrimSpace(parsed.Role)))
	if _, known := roles[role]; !known {
		return caseItemRequest{}
	}
	claim := strings.TrimSpace(parsed.Claim)
	// The story carries the argument, so a claim is optional — except for the
	// roles that qualify or exclude something: an empty benign claim would
	// satisfy the frontend's "the agent explained this card" check while
	// saying nothing, quietly softening a warning banner, and a bare demoted
	// or rules_out label asserts without a reason. A long claim is kept whole:
	// cutting a sentence mid-clause invents a claim the agent did not make.
	if claim == "" && (role == RoleBenign || role == RoleDemoted || role == RoleRulesOut) {
		return caseItemRequest{}
	}
	item := caseItemRequest{valid: true, ref: ref, role: role, claim: claim, gap: strings.TrimSpace(parsed.Gap)}
	if len(parsed.Subject) == 0 || string(parsed.Subject) == "null" {
		return item
	}
	var subject EvidenceSubject
	if json.Unmarshal(parsed.Subject, &subject) != nil {
		return caseItemRequest{}
	}
	for _, field := range []string{
		subject.Kind, subject.Name, subject.Container, subject.Stream, subject.Observation,
	} {
		if utf8.RuneCountInString(field) > maxIdentifierRunes {
			return caseItemRequest{}
		}
	}
	for _, field := range []*string{subject.Group, subject.Namespace} {
		if field != nil && utf8.RuneCountInString(*field) > maxIdentifierRunes {
			return caseItemRequest{}
		}
	}
	if subject.Kind == "" ||
		(subject.Stream != "" && subject.Stream != "current" && subject.Stream != "previous") {
		return caseItemRequest{}
	}
	item.subject = &subject
	return item
}

func canonicalStoryMarkers(report string, items []caseItemRequest) string {
	if !strings.Contains(report, "[[radar:evidence-ref=") {
		return report
	}
	// A ref cited by two items (one diagnose result, two subjects) names no
	// single card; it stays a ref for the renderer to flag.
	indexByRef := map[string]int{}
	for i, item := range items {
		if _, dup := indexByRef[item.ref]; dup {
			indexByRef[item.ref] = -1
			continue
		}
		indexByRef[item.ref] = i
	}
	rewrite := func(text string) string {
		return storyRefMarkerRe.ReplaceAllStringFunc(text, func(marker string) string {
			m := storyRefMarkerRe.FindStringSubmatch(marker)
			if i, ok := indexByRef[m[1]]; ok && i >= 0 {
				return "[[radar:evidence=" + strconv.Itoa(i) + m[2] + "]]"
			}
			return marker
		})
	}
	// Fenced code and inline code are the agent quoting something; the
	// renderer keeps them literal, so the parser must not rewrite them either.
	// A fence closes on a bare run of the same character at least as long as
	// it opened with; a code span closes on a backtick run of the same length.
	// The grammar is the tokenizer's (investigationStory.ts): a fence indents
	// at most three spaces, a blockquote or indented code line is quoted text,
	// and a code span may run across lines until the paragraph ends.
	lines := strings.Split(report, "\n")
	fenceChar, fenceLen := byte(0), 0
	openRun := 0
	for n, line := range lines {
		trimmed := strings.TrimLeft(line, " ")
		indent := len(line) - len(trimmed)
		if run := leadingRun(trimmed); indent <= 3 && run >= 3 && (trimmed[0] == '`' || trimmed[0] == '~') {
			if fenceLen == 0 {
				fenceChar, fenceLen = trimmed[0], run
				openRun = 0
				continue
			}
			if trimmed[0] == fenceChar && run >= fenceLen && strings.TrimSpace(trimmed[run:]) == "" {
				fenceChar, fenceLen = 0, 0
				continue
			}
		}
		if fenceLen > 0 {
			continue
		}
		if strings.TrimSpace(line) == "" {
			openRun = 0
			continue
		}
		if quotedLineRe.MatchString(line) {
			continue
		}
		lines[n], openRun = rewriteOutsideCodeSpans(line, openRun, rewrite)
	}
	return strings.Join(lines, "\n")
}

var quotedLineRe = regexp.MustCompile(`^( {0,3}>| {4,}|\t)`)

func leadingRun(text string) int {
	n := 0
	for n < len(text) && text[n] == text[0] {
		n++
	}
	return n
}

var backtickRunRe = regexp.MustCompile("`+")

func rewriteOutsideCodeSpans(line string, openRun int, rewrite func(string) string) (string, int) {
	var out strings.Builder
	cursor := 0
	for _, loc := range backtickRunRe.FindAllStringIndex(line, -1) {
		segment := line[cursor:loc[0]]
		if openRun == 0 {
			segment = rewrite(segment)
		}
		out.WriteString(segment)
		out.WriteString(line[loc[0]:loc[1]])
		run := loc[1] - loc[0]
		if openRun == 0 {
			openRun = run
		} else if run == openRun {
			openRun = 0
		}
		cursor = loc[1]
	}
	tail := line[cursor:]
	if openRun == 0 {
		tail = rewrite(tail)
	}
	out.WriteString(tail)
	return out.String(), openRun
}

func stripStoryMarkers(text string) string {
	return strings.TrimSpace(anyStoryMarkerRe.ReplaceAllString(text, ""))
}

// StripPlacementMarkers removes the story's [[radar:evidence=N]] placements
// for a model that reads the story back, such as an explanation turn.
func StripPlacementMarkers(report string) string {
	return strings.TrimSpace(placementMarkerRe.ReplaceAllString(report, ""))
}
