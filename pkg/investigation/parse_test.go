package investigation

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"
)

func testRef(scope, nonce byte) string {
	return "ev_" + strings.Repeat(string(scope), 26) + "_" + strings.Repeat(string(nonce), 26)
}

func verdictJSON(fields string) string {
	return "```json\n{" + fields + "}\n```"
}

func TestParse_ParsesJSONBlock(t *testing.T) {
	text := "The pod crashloops.\n\n```json\n" +
		`{"root_cause": "bad image tag", "remediation": ["roll back"], "confidence": 0.9}` +
		"\n```"
	d := Parse(text).Verdict
	if d.RootCause != "bad image tag" {
		t.Errorf("root cause = %q", d.RootCause)
	}
	if len(d.Remediation) != 1 || d.Remediation[0] != "roll back" {
		t.Errorf("remediation = %v", d.Remediation)
	}
	if d.Confidence == nil || *d.Confidence != 0.9 {
		t.Errorf("confidence = %v", d.Confidence)
	}
	if strings.Contains(d.Report, "```json") {
		t.Errorf("report still has the json block: %q", d.Report)
	}
}

func TestParse_CertaintyRequiresSummary(t *testing.T) {
	for _, summary := range []string{"", "   ", "[[radar:evidence=0]]"} {
		for _, certainty := range []string{"established", "likely", "suspected"} {
			fields, _ := json.Marshal(map[string]any{"summary": summary, "certainty": certainty, "revises_assessment": false})
			d := Parse("```json\n" + string(fields) + "\n```\nA conversational answer.").Verdict
			if d.Certainty != "" || d.Summary != "" || d.Report != "A conversational answer." {
				t.Fatalf("summary=%q certainty=%q: %+v", summary, certainty, d)
			}
		}
	}
	if d := Parse(verdictJSON(`"summary":"All replicas are ready","healthy":true,"certainty":"established"`)).Verdict; d.Certainty != Established {
		t.Fatalf("valid certainty removed: %+v", d)
	}
}

func TestParse_ReadsRootCauseRefsPrivatelyAndStrictly(t *testing.T) {
	first := testRef('a', 'b')
	second := testRef('c', 'd')
	p := Parse(verdictJSON(`"root_cause":"bad tag","root_cause_evidence_refs":["` + first + `","` + second + `"]`))
	if p.Verdict.RootCauseEvidence != nil {
		t.Fatal("untrusted model refs must not enter the verdict before binding")
	}
	request := p.Citations.rootCause
	if !request.present || request.invalid {
		t.Fatalf("valid request state = %+v", request)
	}
	if len(request.refs) != 2 || request.refs[0] != first || request.refs[1] != second {
		t.Fatalf("evidence refs = %v", request.refs)
	}

	invalidFields := []string{
		`null`,
		`{"ref":"` + first + `"}`,
		`["not-a-ref"]`,
		`["` + first + `","` + first + `"]`,
		`["` + first + `","` + second + `","` + testRef('e', 'f') + `","` + testRef('g', 'h') + `"]`,
	}
	for _, field := range invalidFields {
		got := Parse(verdictJSON(`"root_cause":"still preserved","root_cause_evidence_refs":` + field))
		if got.Verdict.RootCause != "still preserved" {
			t.Fatalf("malformed evidence discarded root cause for %s", field)
		}
		if r := got.Citations.rootCause; !r.present || !r.invalid || len(r.refs) != 0 {
			t.Errorf("field %s request = %+v, want invalid with no refs", field, r)
		}
	}

	if r := Parse(verdictJSON(`"root_cause":"old response"`)).Citations.rootCause; r.present || r.invalid {
		t.Fatalf("omitted field = %+v, want missing", r)
	}
	if r := Parse(verdictJSON(`"root_cause":"uncited","root_cause_evidence_refs":[]`)).Citations.rootCause; !r.present || r.invalid || len(r.refs) != 0 {
		t.Fatalf("empty field = %+v", r)
	}
}

func TestParse_RecommendedIndex(t *testing.T) {
	valid := "x\n\n```json\n" +
		`{"root_cause":"r","remediation":["a","b"],"recommended_index":2}` + "\n```"
	if d := Parse(valid).Verdict; d.RecommendedIndex == nil || *d.RecommendedIndex != 2 {
		t.Errorf("recommended_index = %v, want 2", d.RecommendedIndex)
	}
	// Out of range (and the 0 = "no safe fix" sentinel) must be dropped, so
	// the UI never points Apply at a non-existent step.
	for _, bad := range []string{"0", "3", "-1"} {
		text := "x\n\n```json\n" +
			`{"root_cause":"r","remediation":["a","b"],"recommended_index":` + bad + "}\n```"
		if d := Parse(text).Verdict; d.RecommendedIndex != nil {
			t.Errorf("recommended_index %s should be dropped, got %v", bad, *d.RecommendedIndex)
		}
	}
}

func TestParse_HealthyAllClear(t *testing.T) {
	d := Parse("The deployment is healthy.\n\n```json\n" +
		`{"healthy":true,"root_cause":"","remediation":[],"recommended_index":0,"confidence":0.8}` +
		"\n```").Verdict
	if !d.Healthy {
		t.Fatal("healthy = false, want true")
	}
	if d.RootCause != "" || len(d.Remediation) != 0 {
		t.Errorf("all-clear carried a finding: %+v", d)
	}
	if d.RecommendedIndex != nil {
		t.Errorf("recommended_index should be dropped for all-clear, got %v", *d.RecommendedIndex)
	}
}

// Conclusion precedence must never produce a self-contradictory object: a
// concrete finding clears both flags; inconclusive clears healthy ("absence of
// evidence is not health"); at most one of {finding, inconclusive, healthy}
// survives.
func TestParse_ConclusionPrecedence(t *testing.T) {
	block := func(j string) string { return "prose\n\n```json\n" + j + "\n```" }

	d := Parse(block(`{"healthy":true,"root_cause":"bad image","remediation":["fix it"],"recommended_index":1}`)).Verdict
	if d.Healthy || d.Inconclusive || d.RootCause != "bad image" {
		t.Errorf("a root cause must clear both flags: %+v", d)
	}
	d = Parse(block(`{"healthy":true,"inconclusive":true,"root_cause":"","remediation":[]}`)).Verdict
	if d.Healthy || !d.Inconclusive {
		t.Errorf("inconclusive must win over healthy: %+v", d)
	}
	d = Parse(block(`{"inconclusive":true,"root_cause":"","remediation":[],"recommended_index":0,"recommended_reason":"x"}`)).Verdict
	if d.RecommendedReason != "" {
		t.Errorf("recommended_reason must be empty without a valid index, got %q", d.RecommendedReason)
	}
}

func TestParse_FreeTextIsReportNotRootCause(t *testing.T) {
	d := Parse("The deployment looks healthy; nothing is wrong.").Verdict
	if d.Report == "" {
		t.Fatalf("expected free text in Report, got %q", d.Report)
	}
	if d.RootCause != "" {
		t.Errorf("free text must not become a RootCause, got %q", d.RootCause)
	}
}

func TestParse_StoryFields(t *testing.T) {
	text := "The app crashes on start.\n\n[[radar:evidence=0]]\n\nSo the build changed.\n\n" + verdictJSON(
		`"summary":"  The app cannot log in to its database, and it started with the last deploy.  ",`+
			`"certainty":"Likely","unresolved":["Atlas password rotated?","","what would settle it","a fourth item"],`+
			`"root_cause":"Auth fails after revision 8.",`+
			`"steps":[{"text":"Roll back to revision 7","kind":"mitigate","precondition":"if revision 7 still authenticates"},`+
			`{"text":"Test the stored password with mongosh","kind":"VERIFY"},`+
			`{"text":"","kind":"investigate"},{"text":"Compare the two builds","kind":"guess"}],`+
			`"recommended_index":1,"recommended_reason":"reversible","revises_assessment":true`)
	d := Parse(text).Verdict
	if d.Summary != "The app cannot log in to its database, and it started with the last deploy." {
		t.Fatalf("summary = %q", d.Summary)
	}
	if len(d.Unresolved) != 3 || d.Unresolved[0] != "Atlas password rotated?" || d.Unresolved[1] != "what would settle it" {
		t.Fatalf("unresolved = %v (empty dropped, capped at %d)", d.Unresolved, MaxUnresolved)
	}
	if d.OmittedEntries != 0 {
		t.Fatalf("an empty entry is not a lost one: omitted = %d", d.OmittedEntries)
	}
	if len(d.Steps) != 2 || d.Steps[0].Kind != StepMitigate || d.Steps[1].Kind != StepVerify ||
		d.Steps[0].Precondition != "if revision 7 still authenticates" {
		t.Fatalf("steps = %+v (empty text and unknown kind dropped, kind lower-cased)", d.Steps)
	}
	if len(d.Remediation) != 2 || d.Remediation[0] != "Roll back to revision 7" {
		t.Fatalf("remediation must be derived from steps, got %v", d.Remediation)
	}
	if d.RecommendedIndex != nil {
		t.Fatalf("a mitigate step with an unverified precondition is not a one-click fix: %v", d.RecommendedIndex)
	}
	if d.Certainty != Likely {
		t.Fatalf("certainty = %q", d.Certainty)
	}
	if !d.RevisesAssessment {
		t.Fatal("revises_assessment not parsed")
	}
	if !strings.Contains(d.Report, "[[radar:evidence=0]]") || strings.Contains(d.Report, "```json") {
		t.Fatalf("report must keep placement markers and lose the verdict block: %q", d.Report)
	}
}

func TestParse_EstablishedCannotCoexistWithUnresolved(t *testing.T) {
	d := Parse(verdictJSON(`"summary":"Connection fails","certainty":"established","unresolved":["whether the password was rotated"],"root_cause":"x"`)).Verdict
	if d.Certainty != Likely {
		t.Fatalf("established with open questions must read as likely, got %q", d.Certainty)
	}
	d = Parse(verdictJSON(`"summary":"Connection fails","certainty":"established","unresolved":[],"root_cause":"x","steps":[{"text":"roll back","kind":"mitigate"}],"recommended_index":1`)).Verdict
	if d.Certainty != Established || d.RecommendedIndex == nil {
		t.Fatalf("an unconditional mitigate step stays recommendable: %+v", d)
	}
}

func TestParse_VerdictBlockDividesNotesFromStory(t *testing.T) {
	block := "```json\n{\"summary\":\"s\",\"root_cause\":\"x\"}\n```"
	d := Parse("Notes: one restart, one exit.\n\n" + block + "\n\nThe story.\n\n[[radar:evidence=0]]").Verdict
	if d.Notes != "Notes: one restart, one exit." || d.Report != "The story.\n\n[[radar:evidence=0]]" {
		t.Fatalf("notes=%q report=%q", d.Notes, d.Report)
	}
	old := Parse("The story.\n\n" + block).Verdict
	if old.Notes != "" || old.Report != "The story." {
		t.Fatalf("trailing-block shape: notes=%q report=%q", old.Notes, old.Report)
	}
}

func TestParse_StoryJSONFenceIsNotTheVerdict(t *testing.T) {
	text := "ledger line\n\n```json\n{\"summary\": \"The replica count is wrong.\", \"root_cause\": \"spec.replicas is 0.\"}\n```\n\nSet it back:\n\n```json\n{\"spec\": {\"replicas\": 2}}\n```\n"
	d := Parse(text).Verdict
	if d.Summary != "The replica count is wrong." || d.RootCause != "spec.replicas is 0." {
		t.Fatalf("expected the verdict block to win over the quoted patch, got summary %q root cause %q", d.Summary, d.RootCause)
	}
	if !strings.Contains(d.Report, "\"replicas\": 2") || !strings.HasPrefix(d.Report, "Set it back:") {
		t.Fatalf("expected the story to keep the quoted patch, got %q", d.Report)
	}
	if d.Notes != "ledger line" {
		t.Fatalf("expected the ledger before the verdict, got %q", d.Notes)
	}
}

func TestClampSummary(t *testing.T) {
	first := strings.Repeat("word ", 40) + "available."
	if got := clampSummary(first+" Restarting has been failing for about 6.5 hours.", 240); !strings.HasSuffix(got, "available.") {
		t.Fatalf("clamp should back up to the previous sentence, not cut at a decimal point, got %q", got)
	}
	got := clampSummary(strings.Repeat("word ", 40)+"tail", 180)
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("expected an ellipsis, got %q", got)
	}
	body := strings.TrimSuffix(got, "…")
	if strings.HasSuffix(body, " ") || !strings.HasSuffix(body, "word") {
		t.Fatalf("expected the clamp to end on a whole word with no trailing space, got %q", got)
	}
	if utf8.RuneCountInString(got) > 181 {
		t.Fatalf("expected at most the limit plus the ellipsis, got %d runes", utf8.RuneCountInString(got))
	}
}

func TestParse_RecommendedIndexFollowsTheStepTheAgentNamed(t *testing.T) {
	// The first step is malformed and dropped; the agent's index 2 names the
	// surviving mitigate step, which is now first.
	d := Parse(verdictJSON(`"root_cause":"x","steps":[{"text":"","kind":"mitigate"},{"text":"roll back","kind":"mitigate"},{"text":"check","kind":"verify"}],"recommended_index":2`)).Verdict
	if d.RecommendedIndex == nil || *d.RecommendedIndex != 1 || d.Remediation[0] != "roll back" {
		t.Fatalf("recommended index not translated: %v %v", d.RecommendedIndex, d.Remediation)
	}
	// The agent's index names the dropped entry: no recommendation.
	d = Parse(verdictJSON(`"root_cause":"x","steps":[{"text":"","kind":"mitigate"},{"text":"roll back","kind":"mitigate"}],"recommended_index":1`)).Verdict
	if d.RecommendedIndex != nil {
		t.Fatalf("a dropped step must not leave a recommendation on its neighbour: %v", *d.RecommendedIndex)
	}
}

func TestStripPlacementMarkers_HandlesCompact(t *testing.T) {
	got := StripPlacementMarkers("A.\n\n[[radar:evidence=0|compact]]\n\nB [[radar:evidence=1]].")
	if strings.Contains(got, "radar:evidence") {
		t.Fatalf("marker leaked into the explanation source: %q", got)
	}
}

func TestParse_SubjectMayNameAListingWithoutAnEntry(t *testing.T) {
	ref := testRef('a', 'b')
	item := Parse(verdictJSON(`"root_cause":"x","evidence":[{"ref":"` + ref + `","role":"context","claim":"c","subject":{"kind":"ConfigMap","namespace":"shop","observation":"resource"}}]`)).Citations.theCase.items[0]
	if !item.valid || item.subject == nil || item.subject.Kind != "ConfigMap" || item.subject.Name != "" {
		t.Fatalf("name-less subject rejected: %+v", item)
	}
}

func TestParse_EvidenceGap(t *testing.T) {
	ref := testRef('a', 'b')
	p := Parse(verdictJSON(`"root_cause":"x","evidence":[{"ref":"` + ref + `","role":"benign","claim":"Last exit was clean.","gap":"  covers the last termination only  "}]`))
	if got := p.Citations.theCase.items[0].gap; got != "covers the last termination only" {
		t.Fatalf("gap = %q", got)
	}
}

func TestParse_StoryFieldCapsAndValidation(t *testing.T) {
	d := Parse(verdictJSON(`"summary":"` + strings.Repeat("s", MaxSummaryRunes+40) + `","certainty":"certain"`)).Verdict
	if len([]rune(d.Summary)) != MaxSummaryRunes+1 || !strings.HasSuffix(d.Summary, "…") {
		t.Fatalf("summary not clamped with an ellipsis: %d %q", len([]rune(d.Summary)), d.Summary[len(d.Summary)-6:])
	}
	sentence := strings.Repeat("Word ", 30) + "end. " + strings.Repeat("more ", 40)
	d = Parse(verdictJSON(`"summary":"` + sentence + `"`)).Verdict
	if !strings.HasSuffix(d.Summary, "end.") {
		t.Fatalf("an over-long summary must cut at the last whole sentence, got %q", d.Summary)
	}
	if d.Certainty != "" {
		t.Fatalf("unknown certainty must be dropped, got %q", d.Certainty)
	}
	d = Parse(verdictJSON(`"steps":[` + strings.Repeat(`{"text":"x","kind":"verify"},`, MaxSteps+2) + `{"text":"last","kind":"verify"}]`)).Verdict
	if len(d.Steps) != MaxSteps || d.OmittedEntries != 3 {
		t.Fatalf("steps not capped with the overflow counted: %d steps, %d omitted", len(d.Steps), d.OmittedEntries)
	}
}

// A mitigation never establishes a cause: with typed steps, "cause unresolved;
// roll back to restore service" stays inconclusive. The legacy remediation-only
// shape keeps its old precedence so older transcripts do not change meaning.
func TestParse_InconclusiveSurvivesTypedSteps(t *testing.T) {
	typed := Parse(verdictJSON(`"inconclusive":true,"steps":[{"text":"Roll back","kind":"mitigate"},{"text":"Check Atlas","kind":"verify"}],"recommended_index":2`)).Verdict
	if !typed.Inconclusive || typed.Healthy {
		t.Fatalf("typed steps must not clear inconclusive: %+v", typed)
	}
	if typed.RecommendedIndex != nil {
		t.Fatalf("a verify step can never be the Apply target: %v", typed.RecommendedIndex)
	}
	legacy := Parse(verdictJSON(`"inconclusive":true,"remediation":["Roll back"],"recommended_index":1`)).Verdict
	if legacy.Inconclusive || legacy.RecommendedIndex == nil {
		t.Fatalf("legacy remediation keeps the old precedence: %+v", legacy)
	}
	cause := Parse(verdictJSON(`"inconclusive":true,"healthy":true,"root_cause":"x","steps":[{"text":"Roll back","kind":"mitigate"}]`)).Verdict
	if cause.Inconclusive || cause.Healthy {
		t.Fatalf("a root cause clears both flags: %+v", cause)
	}
}

func TestParse_RefMarkersBecomeIndexMarkersAndLeaveTheHeadline(t *testing.T) {
	ref := testRef('a', 'b')
	other := testRef('c', 'd')
	body := "```json\n{\"summary\":\"The pod crashes [[radar:evidence-ref=" + ref + "]].\",\"root_cause\":\"nginx exits [[radar:evidence-ref=" + ref + "]] on start.\",\"certainty\":\"established\",\"evidence\":[{\"ref\":\"" + ref + "\",\"role\":\"cause\",\"claim\":\"The log names it.\"}]}\n```\nThe log shows it:\n\n[[radar:evidence-ref=" + ref + "]]\n\nAlso [[radar:evidence-ref=" + ref + "|compact]] inline, and one nothing cites [[radar:evidence-ref=" + other + "]]."
	d := Parse(body).Verdict
	if d.Summary != "The pod crashes." {
		t.Fatalf("summary kept a marker: %q", d.Summary)
	}
	if d.RootCause != "nginx exits on start." {
		t.Fatalf("root_cause kept a marker: %q", d.RootCause)
	}
	if !strings.Contains(d.Report, "\n[[radar:evidence=0]]\n") || !strings.Contains(d.Report, "[[radar:evidence=0|compact]]") {
		t.Fatalf("ref markers not canonicalised: %q", d.Report)
	}
	if !strings.Contains(d.Report, "[[radar:evidence-ref="+other+"]]") {
		t.Fatalf("an uncited ref must stay for the renderer to flag: %q", d.Report)
	}
}

func TestParse_RefMarkerStaysWhenAmbiguousOrQuoted(t *testing.T) {
	ref := testRef('a', 'b')
	body := "```json\n{\"summary\":\"x\",\"root_cause\":\"y\",\"evidence\":[{\"ref\":\"" + ref + "\",\"role\":\"cause\",\"claim\":\"logs\",\"subject\":{\"kind\":\"Pod\",\"name\":\"a\",\"observation\":\"logs\"}},{\"ref\":\"" + ref + "\",\"role\":\"symptom\",\"claim\":\"events\",\"subject\":{\"kind\":\"Pod\",\"name\":\"a\",\"observation\":\"events\"}}]}\n```\nSee [[radar:evidence-ref=" + ref + "]] here, and `[[radar:evidence-ref=" + ref + "]]` quoted, and\n```\n[[radar:evidence-ref=" + ref + "]]\n```\n"
	if d := Parse(body).Verdict; strings.Contains(d.Report, "[[radar:evidence=") {
		t.Fatalf("an ambiguous or quoted ref must not be rewritten: %q", d.Report)
	}
}

func TestCanonicalStoryMarkers_RespectsCodeSpansAndFences(t *testing.T) {
	ref := testRef('a', 'b')
	items := []caseItemRequest{{ref: ref, valid: true}}
	in := "A `` [[radar:evidence-ref=" + ref + "]] `` span, then [[radar:evidence-ref=" + ref + "]] out.\n~~~\n[[radar:evidence-ref=" + ref + "]]\n~~~\n[[radar:evidence-ref=" + ref + "]]"
	out := canonicalStoryMarkers(in, items)
	if strings.Count(out, "[[radar:evidence=0]]") != 2 {
		t.Fatalf("expected the two markers outside code to be rewritten, got %q", out)
	}
	if strings.Count(out, "[[radar:evidence-ref=") != 2 {
		t.Fatalf("expected the two quoted markers to stay, got %q", out)
	}
}

func TestCanonicalStoryMarkers_FenceAndBlankLineCloseASpan(t *testing.T) {
	ref := testRef('a', 'b')
	items := []caseItemRequest{{ref: ref, valid: true}}
	marker := "[[radar:evidence-ref=" + ref + "]]"
	for _, in := range []string{
		"Quoted `example\n~~~\nx\n~~~\n" + marker,
		"Quoted `example\n    \n" + marker,
	} {
		if out := canonicalStoryMarkers(in, items); !strings.HasSuffix(out, "\n[[radar:evidence=0]]") {
			t.Fatalf("expected the marker after the break to be rewritten, got %q", out)
		}
	}
	if out := canonicalStoryMarkers("\t~~~\n"+marker+"\n\t~~~", items); !strings.Contains(out, "\n[[radar:evidence=0]]\n") {
		t.Fatalf("expected a tab-indented fence to be indented code, not a fence, got %q", out)
	}
	if out := canonicalStoryMarkers("\t"+marker, items); strings.Contains(out, "[[radar:evidence=0]]") {
		t.Fatalf("expected tab-indented code to stay literal, got %q", out)
	}
}

func TestCanonicalStoryMarkers_SpansAcrossLinesAndQuotedBlocks(t *testing.T) {
	ref := testRef('a', 'b')
	items := []caseItemRequest{{ref: ref, valid: true}}
	marker := "[[radar:evidence-ref=" + ref + "]]"
	in := strings.Join([]string{
		"Quoted `example",
		marker,
		"end` here.",
		"",
		"> " + marker,
		"    " + marker,
		"     ```",
		marker,
	}, "\n")
	out := canonicalStoryMarkers(in, items)
	if strings.Count(out, "[[radar:evidence=0]]") != 1 {
		t.Fatalf("expected only the marker after the indented non-fence to be rewritten, got %q", out)
	}
	if !strings.HasSuffix(out, "\n[[radar:evidence=0]]") {
		t.Fatalf("expected the last line rewritten, got %q", out)
	}
}

func TestParse_CaseWithoutTouchingRootCauseRefs(t *testing.T) {
	first := testRef('a', 'b')
	second := testRef('c', 'd')
	text := verdictJSON(`"root_cause":"bad tag","root_cause_evidence_refs":["` + first + `"],` +
		`"evidence":[` +
		`{"ref":"` + first + `","role":"cause","claim":"The image tag does not exist.","subject":{"kind":"Deployment","group":"apps","namespace":"shop","name":"api","observation":"resource"}},` +
		`{"ref":"` + second + `","role":"Rules_Out","claim":"  Same host, same user, connects fine.  "},` +
		`{"ref":"` + second + `","role":"symptom","claim":"The previous container exited on OOM.","subject":{"kind":"Pod","name":"api-1","container":"app","stream":"previous"}}` +
		`],"ruled_out":[{"hypothesis":"Atlas account locked","evidence_index":1}]`)
	p := Parse(text)
	if p.Verdict.RootCause != "bad tag" || len(p.Citations.rootCause.refs) != 1 || p.Citations.rootCause.refs[0] != first {
		t.Fatalf("root cause request changed: %+v", p.Citations.rootCause)
	}
	if p.Verdict.Evidence != nil || p.Verdict.RuledOut != nil {
		t.Fatal("untrusted case must not enter the verdict before binding")
	}
	items := p.Citations.theCase.items
	if len(items) != 3 {
		t.Fatalf("items = %d, want 3", len(items))
	}
	if !items[0].valid || items[0].role != RoleCause || items[0].subject == nil ||
		items[0].subject.Group == nil || *items[0].subject.Group != "apps" || items[0].subject.Observation != "resource" {
		t.Fatalf("item 0 = %+v", items[0])
	}
	if !items[1].valid || items[1].role != RoleRulesOut || items[1].claim != "Same host, same user, connects fine." || items[1].subject != nil {
		t.Fatalf("item 1 = %+v", items[1])
	}
	if !items[2].valid || items[2].subject == nil || items[2].subject.Stream != "previous" || items[2].subject.Container != "app" ||
		items[2].subject.Group != nil || items[2].subject.Namespace != nil {
		t.Fatalf("item 2 = %+v", items[2])
	}
	explicitCore := Parse(verdictJSON(`"root_cause":"x","evidence":[{"ref":"` + first + `","role":"cause","claim":"c","subject":{"kind":"Service","group":"","namespace":"","name":"api"}}]`))
	subject := explicitCore.Citations.theCase.items[0].subject
	if subject == nil || subject.Group == nil || *subject.Group != "" || subject.Namespace == nil || *subject.Namespace != "" {
		t.Fatalf("explicit empty group/namespace must survive parsing: %+v", subject)
	}
	wire, _ := json.Marshal(EvidenceItem{Status: Linked, Subject: subject})
	if !strings.Contains(string(wire), `"group":""`) || !strings.Contains(string(wire), `"namespace":""`) {
		t.Fatalf("explicit empty group/namespace must reach the wire: %s", wire)
	}
	if ruled := p.Citations.theCase.ruledOut; len(ruled) != 1 || ruled[0].EvidenceIndex != 1 {
		t.Fatalf("ruled out = %+v", ruled)
	}
}

func TestParse_CaseDropsBadItemsIndividually(t *testing.T) {
	ref := testRef('a', 'b')
	text := verdictJSON(`"root_cause":"x","evidence":[` +
		`{"ref":"not-a-ref","role":"cause","claim":"a"},` +
		`{"ref":"` + ref + `","role":"verdict","claim":"a"},` +
		`{"ref":"` + ref + `","role":"demoted","claim":"","subject":{"kind":"Pod","name":"p","name":"` + strings.Repeat("n", maxIdentifierRunes+1) + `"}},` +
		`{"ref":"` + ref + `","role":"cause","claim":"a","subject":{"name":"p"}},` +
		`{"ref":"` + ref + `","role":"cause","claim":"a","subject":{"kind":"Pod","name":"p","stream":"older"}},` +
		`{"ref":"` + ref + `","role":"context","claim":"fine"}` +
		`],"ruled_out":[{"hypothesis":"","evidence_index":5},{"hypothesis":"h","evidence_index":-1},{"hypothesis":"h"},"not-an-object",{"hypothesis":"kept","evidence_index":5}]`)
	request := Parse(text).Citations.theCase
	if len(request.items) != 6 {
		t.Fatalf("items = %d, want 6 (positions preserved)", len(request.items))
	}
	for i := range 5 {
		if request.items[i].valid {
			t.Errorf("item %d should be invalid: %+v", i, request.items[i])
		}
	}
	if !request.items[5].valid {
		t.Fatalf("valid sibling dropped: %+v", request.items[5])
	}
	if len(request.ruledOut) != 1 || request.ruledOut[0].Hypothesis != "kept" {
		t.Fatalf("ruled out = %+v", request.ruledOut)
	}
}

// Every role the prompt offers must survive the parser. A role the prompt asks
// for but the parser rejects is dropped as unlinked, which is invisible: the
// chip never appears and the healthy-conflict banner can never reframe, with
// nothing anywhere saying why.
func TestParse_AcceptsEveryPromptedRole(t *testing.T) {
	ref := testRef('a', 'b')
	contract := verdictContract(CiteRun)
	for _, role := range Roles {
		if !strings.Contains(contract, string(role)) {
			t.Errorf("role %q is accepted but the prompt never offers it", role)
		}
		items := Parse(verdictJSON(`"root_cause":"x","evidence":[{"ref":"` + ref + `","role":"` + string(role) + `","claim":"a"}]`)).Citations.theCase.items
		if len(items) != 1 || !items[0].valid {
			t.Errorf("role %q was rejected by the parser: %+v", role, items)
			continue
		}
		if items[0].role != role {
			t.Errorf("role %q parsed as %q", role, items[0].role)
		}
	}
}

func TestParse_CaseCapsAndMalformedArrays(t *testing.T) {
	ref := testRef('a', 'b')
	var items []string
	for i := 0; i < MaxEvidenceItems+3; i++ {
		items = append(items, `{"ref":"`+ref+`","role":"context","claim":"c"}`)
	}
	var ruled []string
	for i := 0; i < MaxRuledOut+2; i++ {
		ruled = append(ruled, `{"hypothesis":"h","evidence_index":0}`)
	}
	request := Parse(verdictJSON(`"root_cause":"x","evidence":[` + strings.Join(items, ",") + `],"ruled_out":[` + strings.Join(ruled, ",") + `]`)).Citations.theCase
	if len(request.items) != MaxEvidenceItems {
		t.Fatalf("items = %d, want cap %d", len(request.items), MaxEvidenceItems)
	}
	if len(request.ruledOut) != MaxRuledOut || len(request.overflowRuledOut) != 2 {
		t.Fatalf("ruled out = %d (overflow %d), want cap %d with 2 held for the binder", len(request.ruledOut), len(request.overflowRuledOut), MaxRuledOut)
	}
	if request.dropped != 3 {
		t.Fatalf("dropped = %d, want the 3 items the cap cut", request.dropped)
	}
	for _, field := range []string{`null`, `"text"`, `{"ref":"` + ref + `"}`} {
		p := Parse(verdictJSON(`"root_cause":"still","evidence":` + field + `,"ruled_out":` + field))
		if p.Verdict.RootCause != "still" || len(p.Citations.theCase.items) != 0 || len(p.Citations.theCase.ruledOut) != 0 {
			t.Errorf("field %s: root cause %q, case %+v", field, p.Verdict.RootCause, p.Citations.theCase)
		}
	}
	missing := Parse(verdictJSON(`"root_cause":"old response"`)).Citations.theCase
	if len(missing.items) != 0 || len(missing.ruledOut) != 0 {
		t.Fatalf("omitted case = %+v", missing)
	}
}

// The parser must reject what it cannot understand without inventing a
// reading of it: no unknown role mapped onto a known one, and never a cut
// claim, since a cut qualification is a different claim. The story carries
// the argument, so a cause may come without a claim; the qualifying roles may
// not, since a bare "benign" would satisfy the banner's explained-by check
// while saying nothing. Only the marker wrapper is unwrapped, because that
// text is Radar's own, quoted back.
func TestParse_CaseRejectsEmptyClaimAndUnwrapsMarkerRef(t *testing.T) {
	ref := testRef('a', 'b')
	long := strings.Repeat("x", 1000)
	items := Parse(verdictJSON(`"root_cause":"x","evidence":[` +
		`{"ref":"` + ref + `","role":"benign","claim":""},` +
		`{"ref":"` + ref + `","role":"cause","claim":"   "},` +
		`{"ref":"[[radar:evidence-ref=` + ref + `]]","role":"cause","claim":"Pasted the whole marker."},` +
		`{"ref":"` + ref + `","role":"benign","claim":"` + long + `"},` +
		`{"ref":"` + ref + `","role":"verdict","claim":"An unknown role."},` +
		`{"ref":"` + ref + `","role":"demoted","claim":""},` +
		`{"ref":"` + ref + `","role":"rules_out","claim":""}` +
		`]`)).Citations.theCase.items
	if len(items) != 7 {
		t.Fatalf("items = %d, want 7 positions", len(items))
	}
	if items[0].valid || items[5].valid || items[6].valid {
		t.Errorf("benign, demoted and rules_out need a reason: %+v %+v %+v", items[0], items[5], items[6])
	}
	if !items[1].valid || items[1].claim != "" {
		t.Errorf("a cause without a claim is the story's to explain: %+v", items[1])
	}
	if !items[2].valid || items[2].ref != ref {
		t.Errorf("a marker-wrapped ref must be unwrapped, got %+v", items[2])
	}
	if !items[3].valid || items[3].claim != long {
		t.Errorf("a long benign claim is kept whole, neither cut nor dropped: %+v", items[3])
	}
	bound := Parse(verdictJSON(`"healthy":true,"evidence":[{"ref":"` + ref + `","role":"benign","claim":"` + long + `"}]`))
	Bind(&bound.Verdict, bound.Citations, linkedSet(ref))
	if len(bound.Verdict.Evidence) != 1 || bound.Verdict.Evidence[0].Status != Linked || bound.Verdict.Evidence[0].Claim != long {
		t.Errorf("a long benign claim must reach the page linked and whole: %+v", bound.Verdict.Evidence)
	}
	if items[4].valid {
		t.Errorf("an unknown role must be dropped, not mapped: %+v", items[4])
	}
}

// A ref field is unwrapped only when it is exactly one marker. Two markers,
// or a marker inside prose, is the agent meaning something the parser cannot
// read, and picking one would be choosing an interpretation.
func TestUnwrapRefTakesOnlyAWholeMarker(t *testing.T) {
	ref := testRef('a', 'b')
	other := testRef('c', 'd')
	if got := unwrapRef("[[radar:evidence-ref=" + ref + "]]"); got != ref {
		t.Fatalf("whole marker: got %q", got)
	}
	if got := unwrapRef("  [[radar:evidence-ref=" + ref + "]]  "); got != ref {
		t.Fatalf("padded marker: got %q", got)
	}
	if got := unwrapRef(ref); got != ref {
		t.Fatalf("bare ref: got %q", got)
	}
	two := "[[radar:evidence-ref=" + ref + "]] or [[radar:evidence-ref=" + other + "]]"
	if got := unwrapRef(two); got != two {
		t.Fatalf("two markers should not resolve to one: got %q", got)
	}
	prose := "see [[radar:evidence-ref=" + ref + "]] for this"
	if got := unwrapRef(prose); got != prose {
		t.Fatalf("a marker in prose should not be unwrapped: got %q", got)
	}
}

// An evidence field that is not a list carries no items and no count that
// would describe what was lost, so it is reported as a fact of its own.
func TestParseCaseRequestReportsAMalformedEvidenceEnvelope(t *testing.T) {
	object := parseCaseRequest(json.RawMessage(`{"ref":"x","role":"cause","claim":"y"}`), nil)
	if !object.malformed || len(object.items) != 0 || object.dropped != 0 {
		t.Fatalf("object envelope: %+v", object)
	}
	if list := parseCaseRequest(json.RawMessage(`[]`), nil); list.malformed {
		t.Fatal("an empty list is a readable case, not a malformed one")
	}
	if null := parseCaseRequest(json.RawMessage(`null`), nil); null.malformed {
		t.Fatal("an absent case is not a malformed one")
	}
	if absent := parseCaseRequest(nil, nil); absent.malformed {
		t.Fatal("no evidence field at all is not a malformed one")
	}
}

// A fence that carries no verdict field is the agent quoting something; the
// answer is prose and keeps it, and nothing in it is handed back as a field.
func TestParse_AnswerWithOnlyAQuotedFenceIsProse(t *testing.T) {
	text := "Set it like this:\n\n```json\n{\"spec\": {\"replicas\": 2}}\n```\n\nThen wait."
	p := Parse(text)
	if p.Verdict.Report != text || p.Verdict.Notes != "" || p.Extensions != nil || p.Verdict.Structured() {
		t.Fatalf("a quoted fence was read as the verdict: %+v %v", p.Verdict, p.Extensions)
	}
}

// A follow-up that revises nothing may write only revises_assessment and a
// product's own fields: that is still the verdict block, not quoted prose.
func TestParse_MinimalFollowUpBlockIsTheVerdict(t *testing.T) {
	p := Parse("```json\n{\"revises_assessment\": false, \"cause_summary\": \"\"}\n```\n\nA PDB limits voluntary disruption.")
	if p.Verdict.Report != "A PDB limits voluntary disruption." || p.Verdict.RevisesAssessment || p.Verdict.Structured() {
		t.Fatalf("minimal follow-up block misread: %+v", p.Verdict)
	}
	if _, ok := p.Extensions["cause_summary"]; !ok {
		t.Fatalf("extension lost with the block: %v", p.Extensions)
	}
}

// A product may ask for fields of its own in the same block; the contract
// parser hands them back untouched and never mistakes them for its own.
func TestParse_HandsBackExtensionFields(t *testing.T) {
	p := Parse(verdictJSON(`"root_cause":"x","cause_summary":"The ConfigMap points at a Service that no longer exists","confidence":0.4`))
	if len(p.Extensions) != 1 || string(p.Extensions["cause_summary"]) != `"The ConfigMap points at a Service that no longer exists"` {
		t.Fatalf("extensions = %v", p.Extensions)
	}
	if p.Verdict.Confidence == nil || *p.Verdict.Confidence != 0.4 {
		t.Fatalf("a contract field must not be handed back as an extension: %+v", p.Verdict)
	}
	if p := Parse(verdictJSON(`"root_cause":"x"`)); p.Extensions != nil {
		t.Fatalf("no extensions must read as nil, got %v", p.Extensions)
	}
}

// Count caps count what they leave out, after validation: a malformed entry
// in the tail was never a valid one and is not counted; a valid one is.
func TestParse_CountCapsCountValidOverflowOnly(t *testing.T) {
	ref := testRef('a', 'b')
	steps := strings.Repeat(`{"text":"s","kind":"verify"},`, MaxSteps) + `{"text":"","kind":"verify"},{"text":"x","kind":"guess"},{"text":42,"kind":"verify"},{"text":"kept count","kind":"mitigate"}`
	ruled := strings.Repeat(`{"hypothesis":"h","evidence_index":0},`, MaxRuledOut) + `{"hypothesis":"","evidence_index":0},{"hypothesis":"past the case","evidence_index":9},{"hypothesis":"` + strings.Repeat("long ", 100) + `","evidence_index":0}`
	unresolved := strings.Repeat(`"open",`, MaxUnresolved) + `"","one more"`
	d := Parse(verdictJSON(`"root_cause":"x","evidence":[{"ref":"` + ref + `","role":"context","claim":"c"}],"steps":[` + steps + `],"ruled_out":[` + ruled + `],"unresolved":[` + unresolved + `]`))
	Bind(&d.Verdict, d.Citations, linkedSet(ref))
	if d.Verdict.OmittedEntries != 3 {
		t.Fatalf("omitted = %d, want one valid step, one hypothesis on a linked item and one valid unresolved item", d.Verdict.OmittedEntries)
	}
	unlinked := Parse(verdictJSON(`"root_cause":"x","evidence":[{"ref":"` + ref + `","role":"context","claim":"c"}],"ruled_out":[` + ruled + `]`))
	Bind(&unlinked.Verdict, unlinked.Citations, linkedSet())
	if unlinked.Verdict.OmittedEntries != 0 {
		t.Fatalf("a hypothesis on an item that did not link was never going to be shown: omitted = %d", unlinked.Verdict.OmittedEntries)
	}
	if len(d.Verdict.Steps) != MaxSteps || len(d.Verdict.Unresolved) != MaxUnresolved || len(d.Citations.theCase.ruledOut) != MaxRuledOut {
		t.Fatalf("caps not held: %d steps, %d unresolved, %d ruled out", len(d.Verdict.Steps), len(d.Verdict.Unresolved), len(d.Citations.theCase.ruledOut))
	}
	// One malformed step never hides its neighbours: the valid ones are kept.
	mixed := Parse(verdictJSON(`"root_cause":"x","steps":[{"text":"first","kind":"verify"},{"text":42,"kind":"verify"},{"text":"third","kind":"mitigate"}]`)).Verdict
	if len(mixed.Steps) != 2 || mixed.Steps[1].Text != "third" || mixed.OmittedEntries != 0 {
		t.Fatalf("a malformed step took its neighbours with it: %+v", mixed)
	}
	if d := Parse(verdictJSON(`"root_cause":"x","steps":[{"text":"one","kind":"verify"}]`)).Verdict; d.OmittedEntries != 0 {
		t.Fatalf("a list inside its cap reports no loss, got %d", d.OmittedEntries)
	}
}

// Text other than the headline is never cut: a long hypothesis, gap or
// precondition is what the agent wrote, whole.
func TestParse_TextFieldsAreKeptWhole(t *testing.T) {
	long := strings.TrimSpace(strings.Repeat("because the node drained at that second ", 30))
	ref := testRef('a', 'b')
	p := Parse(verdictJSON(`"root_cause":"x","unresolved":["` + long + `"],"steps":[{"text":"roll back","kind":"mitigate","precondition":"` + long + `"}],` +
		`"evidence":[{"ref":"` + ref + `","role":"context","claim":"c","gap":"` + long + `"}],"ruled_out":[{"hypothesis":"` + long + `","evidence_index":0}]`))
	if p.Verdict.Unresolved[0] != long || p.Verdict.Steps[0].Precondition != long || p.Citations.theCase.items[0].gap != long || p.Citations.theCase.ruledOut[0].Hypothesis != long {
		t.Fatal("a text field was cut or dropped")
	}
}
