package investigation

// Bind promotes the agent's citations to server-authored provenance. linked
// reports whether one ref names exactly one eligible recorded result; what
// makes a result eligible (a completed, successful read from Radar's own
// transport, in this turn or an earlier one of the same run) is the product's
// decision, since only the party that issued the ref can make it.
//
// RootCauseEvidence binds as a set: one unlinkable ref invalidates the
// request, because a cause resting on a forged citation has no provenance at
// all. The case binds per item: every item keeps its parsed index so ruled_out
// indexes stay meaningful; an item that fails becomes Unlinked, and a
// ruled-out entry pointing at an unlinked or missing item is dropped.
// UnlinkedEvidence rolls those losses up with the entries the cap cut before
// parsing, which have no slot to carry a status.
func Bind(v *Verdict, c Citations, linked func(ref string) bool) {
	bindRootCause(v, c.rootCause, linked)
	bindCase(v, c.theCase, linked)
}

func bindRootCause(v *Verdict, request referenceRequest, linked func(string) bool) {
	if v.RootCause == "" {
		v.RootCauseEvidence = nil
		return
	}
	if request.invalid {
		v.RootCauseEvidence = &RootCauseEvidence{Status: Invalid}
		return
	}
	if !request.present || len(request.refs) == 0 {
		v.RootCauseEvidence = &RootCauseEvidence{Status: Missing}
		return
	}
	for _, ref := range request.refs {
		if !linked(ref) {
			v.RootCauseEvidence = &RootCauseEvidence{Status: Invalid}
			return
		}
	}
	v.RootCauseEvidence = &RootCauseEvidence{
		Status: Linked,
		Refs:   append([]string(nil), request.refs...),
	}
}

func bindCase(v *Verdict, request caseRequest, linked func(string) bool) {
	v.Evidence = nil
	v.RuledOut = nil
	v.UnlinkedEvidence = request.dropped
	v.EvidenceMalformed = request.malformed
	if len(request.items) == 0 {
		return
	}
	items := make([]EvidenceItem, len(request.items))
	for i, item := range request.items {
		if !item.valid || !linked(item.ref) {
			items[i] = EvidenceItem{Status: Unlinked}
			v.UnlinkedEvidence++
			continue
		}
		items[i] = EvidenceItem{
			Status: Linked, Ref: item.ref, Role: item.role, Claim: item.claim, Gap: item.gap,
		}
		if item.subject != nil {
			subject := *item.subject
			if subject.Group != nil {
				group := *subject.Group
				subject.Group = &group
			}
			if subject.Namespace != nil {
				namespace := *subject.Namespace
				subject.Namespace = &namespace
			}
			items[i].Subject = &subject
		}
	}
	v.Evidence = items
	for _, entry := range request.ruledOut {
		if entry.EvidenceIndex >= len(items) || items[entry.EvidenceIndex].Status != Linked {
			continue
		}
		v.RuledOut = append(v.RuledOut, entry)
	}
	// A hypothesis past the cap is a loss only if it would have been shown:
	// one pointing at an unlinked or missing item was never going to be.
	for _, entry := range request.overflowRuledOut {
		if entry.EvidenceIndex < len(items) && items[entry.EvidenceIndex].Status == Linked {
			v.OmittedEntries++
		}
	}
}
