// Package policyreports builds a per-subject lookup of Kyverno PolicyReport
// findings from the dynamic-cache informers. It does not talk to the
// Kubernetes API directly — callers (typically `internal/k8s`) feed it
// `*unstructured.Unstructured` objects fetched from the dynamic cache.
//
// The shape `wgpolicyk8s.io/v1alpha2/PolicyReport` (and its cluster-scoped
// sibling `ClusterPolicyReport`) is the Working Group on Policy reporting
// CRD, populated by Kyverno (and other policy engines). Each report
// contains a `results[]` list of per-rule findings and, for each result,
// either a `resources[]` array of subject refs or an enclosing `scope`
// when the report itself is scoped to a single subject.
package policyreports

// Finding is one entry from a PolicyReport's `results[]`, projected to the
// subject it applies to. It is the minimum useful surface for an agent or a
// human triaging policy violations: which policy/rule triggered, what the
// result was, and the human-readable message.
//
// `Result` follows the upstream reporting API enum:
//   - "pass"  — policy allowed the resource
//   - "fail"  — policy rejected the resource
//   - "warn"  — policy flagged the resource (non-blocking)
//   - "error" — engine error evaluating the policy
//   - "skip"  — policy did not apply (e.g. exclusion match)
//
// `Severity` and `Category` are free-form strings (e.g. "high", "medium",
// "low" for severity; "Pod Security", "Best Practices" for category) — they
// come straight from the report and are not normalized.
type Finding struct {
	Policy   string
	Rule     string
	Result   string
	Severity string
	Category string
	Message  string
}
