package resourcecontext

import (
	"fmt"
	"sort"
	"strings"
)

// SynthesizeHints renders a short, deterministic prose summary of the
// structured fields in c. Returns at most maxHintsBasic lines for
// TierBasic; future tiers can expand the budget.
//
// Ordering is fixed (not data-driven) so golden tests stay stable across
// runs. No LLM is involved — every line maps to a single rule.
//
// Callers SHOULD NOT parse hints — the structured fields are the canonical
// surface. Hints exist solely as a prose convenience for AI consumers.
func SynthesizeHints(c *ResourceContext, tier ContextTier) []string {
	if c == nil {
		return nil
	}

	max := maxHintsBasic
	if tier == TierDiagnostic {
		max = maxHintsDiagnostic
	}

	out := make([]string, 0, max)

	if h := managedByHint(c.ManagedBy); h != "" {
		out = append(out, h)
	}
	if h := issueHint(c.IssueSummary); h != "" {
		out = append(out, h)
	}
	if h := auditHint(c.AuditSummary); h != "" {
		out = append(out, h)
	}
	if h := runsOnHint(c.RunsOn); h != "" {
		out = append(out, h)
	}
	if h := exposesHint(c.Exposes); h != "" {
		out = append(out, h)
	}
	if h := selectedByHint(c.SelectedBy); h != "" {
		out = append(out, h)
	}
	if h := scaledByHint(c.ScaledBy); h != "" {
		out = append(out, h)
	}
	if h := usesHint(c.Uses); h != "" {
		out = append(out, h)
	}
	if h := policyHint(c.PolicySummary); h != "" {
		out = append(out, h)
	}

	if len(out) > max {
		out = out[:max]
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

const (
	maxHintsBasic      = 8
	maxHintsDiagnostic = 12
)

func managedByHint(refs []ContextRef) string {
	if len(refs) == 0 {
		return ""
	}
	m := refs[0]
	return fmt.Sprintf("Managed by %s %s", m.Kind, m.Name)
}

func issueHint(s *IssueSummary) string {
	if s == nil || s.Count == 0 {
		return ""
	}
	noun := pluralize("issue", s.Count)
	var b strings.Builder
	fmt.Fprintf(&b, "%d %s", s.Count, noun)
	if s.HighestSeverity != "" {
		fmt.Fprintf(&b, " (%s", s.HighestSeverity)
		if s.TopReason != "" {
			fmt.Fprintf(&b, ": %s", s.TopReason)
		}
		b.WriteString(")")
	} else if s.TopReason != "" {
		fmt.Fprintf(&b, ": %s", s.TopReason)
	}
	return b.String()
}

func auditHint(s *AuditSummary) string {
	if s == nil || s.Count == 0 {
		return ""
	}
	noun := pluralize("audit finding", s.Count)
	if s.HighestSeverity == "" {
		return fmt.Sprintf("%d %s", s.Count, noun)
	}
	return fmt.Sprintf("%d %s (%s)", s.Count, noun, s.HighestSeverity)
}

func runsOnHint(r *ContextRef) string {
	if r == nil {
		return ""
	}
	return fmt.Sprintf("Running on node %s", r.Name)
}

func exposesHint(refs []ContextRef) string {
	if len(refs) == 0 {
		return ""
	}
	return fmt.Sprintf("Exposed by %s", summarizeKindsCounts(refs))
}

func selectedByHint(refs []ContextRef) string {
	if len(refs) == 0 {
		return ""
	}
	// Distinguish known SelectedBy kinds (PDB vs NetworkPolicy) in the hint —
	// they read very differently to a human, and lumping them together loses
	// signal. Match each kind explicitly: a future kind added to SelectedBy
	// (e.g. ValidatingAdmissionPolicy) would otherwise be silently rendered
	// as NetworkPolicy. Unrecognized kinds drop through to summarizeKindsCounts.
	var pdb, np, other []ContextRef
	for _, r := range refs {
		switch r.Kind {
		case "PodDisruptionBudget":
			pdb = append(pdb, r)
		case "NetworkPolicy":
			np = append(np, r)
		default:
			other = append(other, r)
		}
	}
	parts := make([]string, 0, 3)
	if n := len(np); n > 0 {
		parts = append(parts, fmt.Sprintf("%d %s", n, pluralize("NetworkPolicy", n)))
	}
	if n := len(pdb); n > 0 {
		parts = append(parts, fmt.Sprintf("%d %s", n, pluralize("PodDisruptionBudget", n)))
	}
	if len(other) > 0 {
		parts = append(parts, summarizeKindsCounts(other))
	}
	return strings.Join(parts, " and ") + " " + selectVerb(len(refs))
}

func selectVerb(n int) string {
	if n == 1 {
		return "selects this resource"
	}
	return "select this resource"
}

func scaledByHint(refs []ContextRef) string {
	if len(refs) == 0 {
		return ""
	}
	return fmt.Sprintf("Scaled by %s", summarizeKindsCounts(refs))
}

func usesHint(u *UsesBlock) string {
	if u == nil {
		return ""
	}
	parts := make([]string, 0, 4)
	if n := len(u.ConfigMaps); n > 0 {
		parts = append(parts, fmt.Sprintf("%d %s", n, pluralize("ConfigMap", n)))
	}
	if n := len(u.Secrets); n > 0 {
		parts = append(parts, fmt.Sprintf("%d %s", n, pluralize("Secret", n)))
	}
	if n := len(u.PVCs); n > 0 {
		parts = append(parts, fmt.Sprintf("%d PVCs", n))
		if n == 1 {
			parts[len(parts)-1] = "1 PVC"
		}
	}
	if u.ServiceAccount != nil {
		parts = append(parts, fmt.Sprintf("ServiceAccount %s", u.ServiceAccount.Name))
	}
	if len(parts) == 0 {
		return ""
	}
	return "Uses " + strings.Join(parts, ", ")
}

func policyHint(s *PolicySummary) string {
	if s == nil || s.Kyverno == nil {
		return ""
	}
	k := s.Kyverno
	if k.Fail == 0 && k.Warn == 0 {
		return ""
	}
	parts := make([]string, 0, 2)
	if k.Fail > 0 {
		parts = append(parts, fmt.Sprintf("%d failing", k.Fail))
	}
	if k.Warn > 0 {
		parts = append(parts, fmt.Sprintf("%d warning", k.Warn))
	}
	return "Kyverno: " + strings.Join(parts, ", ")
}

// summarizeKindsCounts groups refs by kind and emits "N Kind, M OtherKind"
// (deterministic order: alphabetical by kind).
func summarizeKindsCounts(refs []ContextRef) string {
	counts := make(map[string]int)
	for _, r := range refs {
		counts[r.Kind]++
	}
	kinds := make([]string, 0, len(counts))
	for k := range counts {
		kinds = append(kinds, k)
	}
	sort.Strings(kinds)
	parts := make([]string, 0, len(kinds))
	for _, k := range kinds {
		parts = append(parts, fmt.Sprintf("%d %s", counts[k], pluralize(k, counts[k])))
	}
	return strings.Join(parts, ", ")
}

// pluralize returns word + "s" when n != 1. Kept English-only; resource
// kinds are loanwords (Pod, Service, etc.) so naive pluralization works.
func pluralize(word string, n int) string {
	if n == 1 {
		return word
	}
	return word + "s"
}
