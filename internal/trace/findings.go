package trace

import (
	"fmt"
	"sort"
	"strings"

	"github.com/skyhook-io/radar/internal/issues"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
)

// hopFindings collects findings for a hop from the live issues stream. We
// match the same way internal/mcp/tools_diagnose.go's RelatedIssues selection
// does - by (group, kind, namespace, name) - so a Phase 0 detection that
// fires for a Service shows up on that Service's hop with no special-casing.
func hopFindings(p *issues.CacheProvider, ref ResourceRef) []Finding {
	if p == nil {
		return nil
	}
	// Empty options means no namespace filter, and nil authorization predicates
	// that fail closed on cross-resource diagnostic references rather than
	// widening what a hop can surface.
	related := issues.RelatedIssues(p, issues.RelatedIssueOptions{}, ref.Group, ref.Kind, ref.Namespace, ref.Name)
	if len(related) == 0 {
		return nil
	}
	out := make([]Finding, 0, len(related))
	for _, iss := range related {
		out = append(out, issueToFinding(iss, ref))
	}
	sortFindingsBySeverity(out)
	return out
}

// sortFindingsBySeverity orders worst-first so a hop with mixed severities
// leads with its most actionable row. Code is the tiebreaker for stable
// output across polls.
func sortFindingsBySeverity(out []Finding) {
	sort.SliceStable(out, func(i, j int) bool {
		ri, rj := severityRank(out[i].Severity), severityRank(out[j].Severity)
		if ri != rj {
			return ri > rj
		}
		return out[i].Code < out[j].Code
	})
}

func issueToFinding(iss issues.Issue, ref ResourceRef) Finding {
	severity := SeverityWarning
	if iss.Severity == issues.SeverityCritical {
		severity = SeverityCritical
	}
	code := issueCode(iss)
	msg := iss.Reason
	if iss.Message != "" {
		if msg != "" {
			msg = msg + " - " + iss.Message
		} else {
			msg = iss.Message
		}
	}
	return Finding{
		Code:     code,
		Severity: severity,
		Message:  msg,
		// Cause + Action are populated by the detectors that classify a
		// failure into plain-English diagnosis (CrashLoopBackOff exit codes,
		// ImagePullBackOff registry/auth/not-found, PVC provisioning). The
		// UI surfaces them as the primary readout when present so the
		// operator doesn't have to navigate to Issues to see "why".
		Cause:   iss.Cause,
		Action:  iss.Action,
		Command: reproducerForRef(ref),
	}
}

// issueCode derives a stable, agent-friendly code from an issue. Fingerprint
// is preferred when set - it's the same identity the issues pipeline uses for
// dedupe. Falling back to "SOURCE:reason" matches what an agent reading the
// trace would write themselves: which detector + what it found.
func issueCode(iss issues.Issue) string {
	if iss.Fingerprint != "" {
		return iss.Fingerprint
	}
	return string(iss.Source) + ":" + iss.Reason
}

// Finding codes the policy layer emits. The reconcile pass (probes.go) keys on
// these after live probes run, so they must stay stable.
const (
	codePolicyWouldDeny  = "netpol:would-deny"
	codePolicyAdvisory   = "netpol:advisory"
	codePolicyEgressNote = "netpol:egress-note"
)

// policyFinding runs the static NetworkPolicy evaluator for a Service's ready
// endpoint pods and turns the caller-INDEPENDENT verdict into a plain-language
// finding. The operator-facing copy never says "NetworkPolicy", "ingress",
// "egress", or "podSelector" - those live only in the kubectl command and the
// expert Cause line. notRestricted is suppressed (silence is correct on the
// clear case). A wouldDeny is a WARNING, never a hard-red verdict: the CNI is
// the only authority on enforcement, so the live in-cluster probe must confirm
// it (the reconcile pass downgrades it when live traffic actually got through).
// backendPorts narrows the evaluation to the Service port(s) an upstream
// (Ingress/HTTPRoute backendRef) actually references. When empty - a bare
// Service entrypoint - every Service port is evaluated. Restricting to the
// path's port stops a deny on an unused port from condemning the traced path
// with a would-block warning the reconcile pass can never clear.
func policyFinding(deps Deps, svc *corev1.Service, pods []*corev1.Pod, backendPorts ...string) (policyResult, Finding, bool) {
	none := policyResult{verdict: policyNotEvaluated}
	if deps.Cache == nil || svc == nil || len(pods) == 0 {
		return none, Finding{}, false
	}
	npLister := deps.Cache.NetworkPolicies()
	if npLister == nil {
		return none, Finding{}, false
	}
	policies, err := npLister.NetworkPolicies(svc.Namespace).List(labels.Everything())
	if err != nil || len(policies) == 0 {
		return none, Finding{}, false
	}
	// Evaluate only ready endpoint pods - the ones that actually receive Service
	// traffic. A NotReady pod outside the data path must not add policy noise.
	// Exception: a Service with publishNotReadyAddresses routes to NotReady
	// endpoints too, so an allowed NotReady pod is reachable. Filtering it out
	// could leave only default-denied ready pods and produce a would-deny
	// false-condemn before any probe - so include every endpoint pod then.
	ready := make([]*corev1.Pod, 0, len(pods))
	for _, p := range pods {
		if svc.Spec.PublishNotReadyAddresses || isPodReadyForTrace(p) {
			ready = append(ready, p)
		}
	}
	if len(ready) == 0 {
		return none, Finding{}, false
	}
	res := evaluateNetpol(ready, filterPortMapsByRef(servicePortMaps(svc), backendPorts), policies)

	cmd := fmt.Sprintf("kubectl get networkpolicy -n %s", svc.Namespace)
	if len(res.policyNames) == 1 {
		cmd = fmt.Sprintf("kubectl describe networkpolicy %s -n %s", res.policyNames[0], svc.Namespace)
	}

	switch res.verdict {
	case policyWouldDeny:
		// A static would-deny is a PREDICTION, not a fact - the network plugin is
		// the only authority on enforcement (kindnet writes the rule but enforces
		// nothing). So the verb is "would block", and we say up front that the
		// API-proxy check can't confirm it (the proxy bypasses network rules).
		// The reconcile pass upgrades this to a confirmed cause on a live drop, or
		// downgrades it to reassurance when in-cluster traffic actually flowed.
		msg := fmt.Sprintf("A cluster network rule would block traffic to these pods on %s - no rule allows it.", res.port)
		if res.wrongPort {
			msg = fmt.Sprintf("A cluster network rule allows other ports but not %s, so traffic to these pods on %s would be blocked.", res.port, res.port)
		}
		// The in-cluster probe only tests TCP - UDP/SCTP are skipped - so for
		// those protocols the test can never confirm or clear this prediction.
		// Don't promise a verification the tool can't perform.
		action := "Run the in-cluster test to confirm it's enforced, or add an ingress rule that allows this port. Check the rule manually:"
		if res.proto == corev1.ProtocolUDP || res.proto == corev1.ProtocolSCTP {
			action = "Radar can't test this protocol in-cluster, so confirm by checking the rule manually, or add an ingress rule that allows this port:"
		}
		return res, Finding{
			Code:     codePolicyWouldDeny,
			Severity: SeverityWarning,
			Message:  msg,
			Cause:    fmt.Sprintf("A NetworkPolicy (%s) selects these pods and no ingress rule permits %s from any source. Whether it's actually enforced depends on the cluster's network plugin, which Radar can't check from here - and the API-proxy check bypasses network rules.", strings.Join(res.policyNames, ", "), res.port),
			Action:   action,
			Command:  cmd,
		}, true
	case policyAdvisory:
		names := strings.Join(res.policyNames, ", ")
		msg := "A cluster network rule limits who can reach these pods; Radar can't tell from here whether your caller is allowed."
		cause := fmt.Sprintf("A NetworkPolicy (%s) allows this port only from specific sources, so the answer depends on which pod is calling.", names)
		action := "If your client should be allowed, confirm its labels/namespace match the rule's allowed sources. Inspect the rule:"
		switch res.advisory {
		case advisoryHostNetwork:
			msg = "A cluster network rule selects these pods, but they run on the host network - NetworkPolicy is CNI-specific there, so Radar can't tell whether it applies."
			cause = fmt.Sprintf("A NetworkPolicy (%s) selects these host-network pods. Whether NetworkPolicy governs host-network traffic depends on the CNI plugin, which Radar can't check from here.", names)
			action = "Confirm whether your CNI enforces NetworkPolicy for host-network pods. Inspect the rule:"
		case advisoryUnresolvedPort:
			msg = "A cluster network rule selects these pods, but Radar couldn't resolve the rule's named port against the pods, so it can't tell whether this port is allowed."
			cause = fmt.Sprintf("A NetworkPolicy (%s) references a named port that doesn't resolve against the endpoint pods, so the static read can't decide the outcome.", names)
			action = "Check that the rule's named port matches a declared container port. Inspect the rule:"
		case advisoryMixed:
			msg = "A cluster network rule would block some of these pods but not others; Radar can't tell which pod your traffic lands on."
			cause = fmt.Sprintf("A NetworkPolicy (%s) denies this port on some endpoint pods while others are open, so the outcome depends on which pod serves the request.", names)
			action = "Reconcile the rule across the pods so they're consistent, or run the in-cluster test. Inspect the rule:"
		}
		return res, Finding{
			Code:     codePolicyAdvisory,
			Severity: SeverityInfo,
			Message:  msg,
			Cause:    cause,
			Action:   action,
			Command:  cmd,
		}, true
	case policyEgressNote:
		f := Finding{
			Code:     codePolicyEgressNote,
			Severity: SeverityInfo,
			Message:  "This service's pods have an outbound-only network rule. It doesn't change whether the service can be reached.",
			Command:  cmd,
		}
		if res.egress != nil {
			f.Message = res.egress.message()
			f.Chips = res.egress.chips()
			f.ChipsLabel = res.egress.chipsLabel()
			f.ChipNotes = res.egress.chipNotes()
		}
		return res, f, true
	default:
		// notRestricted / notEvaluated → no row.
		return res, Finding{}, false
	}
}

// policyVerdictKey is the stable string form stashed on a Pods hop's Meta so the
// post-probe reconcile can find a static would-deny without re-evaluating.
func policyVerdictKey(v policyVerdict) string {
	switch v {
	case policyWouldDeny:
		return "would-deny"
	case policyAdvisory:
		return "advisory"
	case policyEgressNote:
		return "egress-note"
	case policyNotRestricted:
		return "not-restricted"
	default:
		return ""
	}
}

// reproducerForRef returns the kubectl-shaped one-liner an operator can paste
// to see the raw state behind a finding. The command is intentionally
// read-only - the trace explains, the operator decides.
func reproducerForRef(ref ResourceRef) string {
	ns := ""
	if ref.Namespace != "" {
		ns = " -n " + ref.Namespace
	}
	switch ref.Kind {
	case "Service":
		return "kubectl describe service " + ref.Name + ns
	case "Pod":
		// State-blind fallback: describe explains most pod problems. When the
		// pod's actual container state is known, podDiagnosis overrides this
		// with the state-true command (e.g. logs --previous for a crash loop),
		// so this default only runs when the live state isn't classifiable.
		return "kubectl describe pod " + ref.Name + ns
	case "Pods":
		return "kubectl get pods" + ns
	case "Ingress":
		return "kubectl describe ingress " + ref.Name + ns
	case "HTTPRoute":
		return fmt.Sprintf("kubectl get httproute %s%s -o jsonpath='{.status.parents}'", ref.Name, ns)
	case "GRPCRoute":
		return fmt.Sprintf("kubectl get grpcroute %s%s -o jsonpath='{.status.parents}'", ref.Name, ns)
	case "Gateway":
		return fmt.Sprintf("kubectl get gateway.gateway.networking.k8s.io %s%s -o yaml", ref.Name, ns)
	}
	return ""
}

func selectorReproducer(ref ResourceRef, selector map[string]string) string {
	if len(selector) == 0 || ref.Namespace == "" {
		return ""
	}
	keys := make([]string, 0, len(selector))
	for k := range selector {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+selector[k])
	}
	return "kubectl get pods -n " + ref.Namespace + " -l " + strings.Join(parts, ",")
}
