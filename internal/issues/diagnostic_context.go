package issues

import (
	"fmt"
	"sort"
	"strings"

	"github.com/skyhook-io/radar/pkg/issuesapi"
)

const (
	maxDiagnosticRefs       = 5
	maxDiagnosticIssueRefs  = 5
	maxDiagnosticFacts      = 4
	factExplicitReference   = "explicit_reference"
	factOwnerRollup         = "owner_rollup"
	factSelectedBackend     = "selected_backend_issue"
	factServiceConfig       = "service_config_mismatch"
	factServiceEnvReference = "service_env_reference"
	factProbeTarget         = "probe_target_mismatch"
	factBlockedInit         = "blocked_init_container"
	factRestartCause        = "restart_cause"
)

type serviceBackendIssueProvider interface {
	SelectedPodsForService(namespace, name string) []Ref
}

type changeContextProvider interface {
	ChangeContextForIssue(Issue) *issuesapi.ChangeContext
}

func enrichDiagnosticContext(shaped, flat, grouped []Issue, p Provider) []Issue {
	if len(shaped) == 0 {
		return shaped
	}

	groupedByID := map[string]Issue(nil)
	if len(grouped) > 0 {
		groupedByID = make(map[string]Issue, len(grouped))
		for _, g := range grouped {
			groupedByID[g.ID] = g
		}
	}

	flatByResource := make(map[string][]Issue, len(flat))
	for _, f := range flat {
		key := resourceKey(f.Group, f.Kind, f.Namespace, f.Name)
		flatByResource[key] = append(flatByResource[key], f)
	}

	var serviceProvider serviceBackendIssueProvider
	if sp, ok := p.(serviceBackendIssueProvider); ok {
		serviceProvider = sp
	}
	var changeProvider changeContextProvider
	if cp, ok := p.(changeContextProvider); ok {
		changeProvider = cp
	}

	out := append([]Issue(nil), shaped...)
	for idx := range out {
		var b diagnosticContextBuilder
		i := &out[idx]
		if changeProvider != nil {
			i.ChangeContext = changeProvider.ChangeContextForIssue(*i)
		}

		if i.Source == SourceMissingRef {
			b.add(issuesapi.DiagnosticRoleCandidate, issuesapi.DiagnosticFact{
				Type:    factExplicitReference,
				Message: "Detected from an explicit reference to an object that does not exist or cannot be resolved.",
			})
		}

		if isServiceConfigMismatch(*i) {
			b.add(issuesapi.DiagnosticRoleCandidate, issuesapi.DiagnosticFact{
				Type:    factServiceConfig,
				Message: i.Reason,
			})
		}

		if isServiceEnvReferenceMismatch(*i) {
			b.add(issuesapi.DiagnosticRoleCandidate, issuesapi.DiagnosticFact{
				Type:    factServiceEnvReference,
				Message: diagnosticMessage(*i),
			})
		}

		if isProbeTargetMismatch(*i) {
			b.add(issuesapi.DiagnosticRoleCandidate, issuesapi.DiagnosticFact{
				Type:    factProbeTarget,
				Message: diagnosticMessage(*i),
			})
		}

		if isBlockedInitContainer(*i) {
			b.add(issuesapi.DiagnosticRoleCandidate, issuesapi.DiagnosticFact{
				Type:    factBlockedInit,
				Message: diagnosticMessage(*i),
			})
		}

		if fact, ok := restartCauseFact(*i); ok {
			b.add(issuesapi.DiagnosticRoleContext, fact)
		}

		if i.GroupingScope == issuesapi.ScopeWorkload && len(i.Members) > 0 {
			refs := limitRefs(i.Members, maxDiagnosticRefs)
			msg := fmt.Sprintf("Grouped from %d affected resource(s) under this %s.", i.Count, i.Kind)
			if i.MembersTruncated {
				msg += " Member refs are truncated."
			}
			b.add(issuesapi.DiagnosticRoleRollup, issuesapi.DiagnosticFact{
				Type:    factOwnerRollup,
				Message: msg,
				Refs:    refs,
			})
		}

		if serviceProvider != nil && isServiceBackendContextCandidate(*i) {
			addServiceBackendContext(&b, *i, serviceProvider, flatByResource, groupedByID)
		}

		if ctx := b.build(); ctx != nil {
			i.DiagnosticContext = ctx
		}
	}

	return out
}

func addServiceBackendContext(b *diagnosticContextBuilder, issue Issue, serviceProvider serviceBackendIssueProvider, flatByResource map[string][]Issue, groupedByID map[string]Issue) {
	if !isServiceBackendContextCandidate(issue) {
		return
	}
	pods := serviceProvider.SelectedPodsForService(issue.Namespace, issue.Name)
	if len(pods) == 0 {
		return
	}
	pods = append([]Ref(nil), pods...)
	sortRefs(pods)

	seenIDs := make(map[string]bool)
	var related []issuesapi.IssueRef
	var refs []Ref
	for _, pod := range pods {
		key := resourceKey(pod.Group, pod.Kind, pod.Namespace, pod.Name)
		for _, flatIssue := range flatByResource[key] {
			if flatIssue.ID == issue.ID || seenIDs[flatIssue.ID] {
				continue
			}
			grouped, ok := groupedByID[flatIssue.ID]
			if !ok {
				grouped = flatIssue
			}
			related = append(related, issueRef(grouped))
			refs = append(refs, pod)
			seenIDs[flatIssue.ID] = true
			if len(related) >= maxDiagnosticIssueRefs {
				break
			}
		}
		if len(related) >= maxDiagnosticIssueRefs {
			break
		}
	}
	if len(related) == 0 {
		return
	}

	sortIssueRefs(related)
	sortRefs(refs)
	b.add(issuesapi.DiagnosticRoleAffected, issuesapi.DiagnosticFact{
		Type:          factSelectedBackend,
		Message:       "Selected backend pod(s) already have active issues.",
		Refs:          limitRefs(dedupeRefs(refs), maxDiagnosticRefs),
		RelatedIssues: limitIssueRefs(related, maxDiagnosticIssueRefs),
	})
}

func isServiceBackendContextCandidate(issue Issue) bool {
	return issue.Kind == "Service" && issue.Category == issuesapi.CategoryServiceNoEndpoints && strings.Contains(issue.Reason, "selected pods ready")
}

func isServiceConfigMismatch(i Issue) bool {
	if i.Category != issuesapi.CategoryServiceNoEndpoints {
		return false
	}
	reason := strings.ToLower(i.Reason)
	return strings.Contains(reason, "selector matches no pods") || strings.Contains(reason, "unresolved named targetport")
}

func isServiceEnvReferenceMismatch(i Issue) bool {
	return i.Reason == "Service port mismatch" || i.Reason == "Missing referenced Service"
}

func isProbeTargetMismatch(i Issue) bool {
	return i.Reason == "ReadinessProbeInvalid" || i.Reason == "LivenessProbeInvalid"
}

func isBlockedInitContainer(i Issue) bool {
	return i.Reason == "InitContainerStalled"
}

func restartCauseFact(i Issue) (issuesapi.DiagnosticFact, bool) {
	if i.RestartCount <= 0 && i.LastTerminatedReason == "" {
		return issuesapi.DiagnosticFact{}, false
	}
	var parts []string
	if i.RestartCount > 0 {
		parts = append(parts, fmt.Sprintf("restartCount=%d", i.RestartCount))
	}
	if i.LastTerminatedReason != "" {
		parts = append(parts, fmt.Sprintf("lastTerminatedReason=%s", i.LastTerminatedReason))
	}
	if i.Reason == "LivenessProbeFailed" || i.Reason == "ReadinessProbeFailed" {
		parts = append(parts, fmt.Sprintf("probeFailure=%s", i.Reason))
	}
	return issuesapi.DiagnosticFact{
		Type:    factRestartCause,
		Message: "Container restart evidence: " + strings.Join(parts, ", ") + ".",
	}, true
}

func diagnosticMessage(i Issue) string {
	if i.Message != "" {
		return i.Message
	}
	return i.Reason
}

func issueRef(i Issue) issuesapi.IssueRef {
	return issuesapi.IssueRef{
		Ref:      Ref{Group: i.Group, Kind: i.Kind, Namespace: i.Namespace, Name: i.Name},
		Reason:   i.Reason,
		Category: i.Category,
		Severity: i.Severity,
	}
}

type diagnosticContextBuilder struct {
	role      issuesapi.DiagnosticRole
	facts     []issuesapi.DiagnosticFact
	factRanks []int
}

func (b *diagnosticContextBuilder) add(role issuesapi.DiagnosticRole, fact issuesapi.DiagnosticFact) {
	if fact.Type == "" {
		return
	}
	rank := diagnosticRoleRank(role)
	if rank > diagnosticRoleRank(b.role) {
		b.role = role
	}
	if len(b.facts) >= maxDiagnosticFacts {
		replace := -1
		lowest := rank
		for idx, existing := range b.factRanks {
			if existing < lowest {
				lowest = existing
				replace = idx
			}
		}
		if replace < 0 {
			return
		}
		b.facts[replace] = fact
		b.factRanks[replace] = rank
		return
	}
	b.facts = append(b.facts, fact)
	b.factRanks = append(b.factRanks, rank)
}

func (b diagnosticContextBuilder) build() *issuesapi.DiagnosticContext {
	if len(b.facts) == 0 {
		return nil
	}
	return &issuesapi.DiagnosticContext{Role: b.role, Facts: b.facts}
}

func diagnosticRoleRank(role issuesapi.DiagnosticRole) int {
	switch role {
	case issuesapi.DiagnosticRoleCandidate:
		return 4
	case issuesapi.DiagnosticRoleAffected:
		return 3
	case issuesapi.DiagnosticRoleRollup:
		return 2
	case issuesapi.DiagnosticRoleContext:
		return 1
	default:
		return 0
	}
}

func limitRefs(refs []Ref, max int) []Ref {
	if len(refs) == 0 || max <= 0 {
		return nil
	}
	out := append([]Ref(nil), refs...)
	if len(out) > max {
		out = out[:max]
	}
	return out
}

func limitIssueRefs(refs []issuesapi.IssueRef, max int) []issuesapi.IssueRef {
	if len(refs) == 0 || max <= 0 {
		return nil
	}
	out := append([]issuesapi.IssueRef(nil), refs...)
	if len(out) > max {
		out = out[:max]
	}
	return out
}

func dedupeRefs(refs []Ref) []Ref {
	seen := make(map[string]bool, len(refs))
	out := make([]Ref, 0, len(refs))
	for _, ref := range refs {
		key := resourceKey(ref.Group, ref.Kind, ref.Namespace, ref.Name)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, ref)
	}
	return out
}

func sortIssueRefs(refs []issuesapi.IssueRef) {
	sort.SliceStable(refs, func(i, j int) bool {
		if refs[i].Severity != refs[j].Severity {
			return SeverityRank(refs[i].Severity) > SeverityRank(refs[j].Severity)
		}
		if refs[i].Ref.Namespace != refs[j].Ref.Namespace {
			return refs[i].Ref.Namespace < refs[j].Ref.Namespace
		}
		if refs[i].Ref.Name != refs[j].Ref.Name {
			return refs[i].Ref.Name < refs[j].Ref.Name
		}
		if refs[i].Ref.Kind != refs[j].Ref.Kind {
			return refs[i].Ref.Kind < refs[j].Ref.Kind
		}
		if refs[i].Ref.Group != refs[j].Ref.Group {
			return refs[i].Ref.Group < refs[j].Ref.Group
		}
		return refs[i].Reason < refs[j].Reason
	})
}
