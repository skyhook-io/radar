package policyreports

import "strings"

// Engine is the normalized policy/security engine that produced a Finding.
//
// It exists because a PolicyReport's `results[].source` is a *producer
// type*, not an engine. One engine emits several source values — a single
// Kyverno 1.18 cluster writes `kyverno` (legacy Policy/ClusterPolicy),
// `KyvernoValidatingPolicy`, and `KyvernoGeneratingPolicy` into the same
// report stream. Filtering on the raw string would fragment one engine
// into as many buckets as it has policy types, and every new upstream
// policy kind would silently create another bucket.
//
// Consumers that ask "what does Kyverno say about this resource" filter on
// Engine; consumers that need to name the exact producer keep reading
// Finding.Source.
type Engine string

const (
	// EngineKyverno — Kyverno, via either the legacy kyverno.io family or
	// the modern policies.kyverno.io CEL family.
	EngineKyverno Engine = "kyverno"
	// EngineVAP — Kubernetes-native ValidatingAdmissionPolicy /
	// MutatingAdmissionPolicy. Kyverno's reports controller can evaluate
	// VAPs and write the results into PolicyReports, but the evaluating
	// engine is the apiserver, so these are attributed to VAP rather than
	// to whichever controller happened to write the report.
	EngineVAP Engine = "vap"
	// EngineTrivy — Trivy Operator (vulnerability, config-audit, secret,
	// and SBOM reports).
	EngineTrivy Engine = "trivy"
	// EngineFalco — Falco runtime security, via a PolicyReport adapter.
	EngineFalco Engine = "falco"
	// EngineOther — the report named a producer we don't have a mapping
	// for. Distinct from EngineUnknown: the data is attributable, we just
	// don't recognize the name. Consumers should show it rather than drop
	// it, and the raw Source is the thing worth displaying.
	EngineOther Engine = "other"
	// EngineUnknown — the report carried no `source` at all. Older engines
	// and hand-written reports omit it. Never conflate this with
	// EngineOther: "nobody said" and "somebody we don't know" lead to
	// different operator conclusions.
	EngineUnknown Engine = "unknown"
)

// EnginesAttributableToKyverno is the engine set that may legitimately fill a
// Kyverno-labelled rollup.
//
// EngineUnknown is included deliberately. `results[].source` is optional and
// older Kyverno builds omit it, so those findings normalize to EngineUnknown.
// Excluding them would empty the Kyverno summary on exactly the legacy
// clusters this integration still has to serve, so an unattributed finding
// keeps the historical assumption that Kyverno wrote it.
//
// EngineVAP is deliberately NOT included, even though Kyverno's reports
// controller is what writes VAP results into PolicyReports: the evaluating
// engine is the apiserver, and a rollup labelled "kyverno" must not count
// enforcement Kyverno did not perform.
var EnginesAttributableToKyverno = []Engine{EngineKyverno, EngineUnknown}

// EngineForSource normalizes a raw PolicyReport `results[].source` value to
// the engine that produced it. Matching is case-insensitive and
// whitespace-trimmed because the field is free-form — nothing upstream
// validates it.
//
// Observed source values this maps (Kyverno 1.18, Trivy Operator 0.2x,
// falcosidekick's PolicyReport output):
//
//	kyverno, KyvernoValidatingPolicy, KyvernoImageValidatingPolicy,
//	KyvernoMutatingPolicy, KyvernoGeneratingPolicy      → kyverno
//	ValidatingAdmissionPolicy, MutatingAdmissionPolicy  → vap
//	Trivy Vulnerability, trivy-operator                 → trivy
//	falco, falcosidekick                                → falco
//
// Unrecognized non-empty values map to EngineOther; an empty source maps
// to EngineUnknown.
func EngineForSource(source string) Engine {
	s := strings.ToLower(strings.TrimSpace(source))
	if s == "" {
		return EngineUnknown
	}

	switch {
	// Prefix, not equality: the modern family appends the policy type
	// (KyvernoValidatingPolicy, KyvernoImageValidatingPolicy, ...) and
	// upstream adds new ones every release. Matching the prefix means a
	// policy kind Radar has never heard of still attributes correctly.
	case strings.HasPrefix(s, "kyverno"):
		return EngineKyverno
	// Suffix covers both ValidatingAdmissionPolicy and
	// MutatingAdmissionPolicy without enumerating them. Ordered after the
	// kyverno prefix so a hypothetical "KyvernoValidatingAdmissionPolicy"
	// attributes to the controller that owns the policy object.
	case strings.HasSuffix(s, "admissionpolicy"):
		return EngineVAP
	case strings.Contains(s, "trivy"):
		return EngineTrivy
	case strings.Contains(s, "falco"):
		return EngineFalco
	}
	return EngineOther
}
