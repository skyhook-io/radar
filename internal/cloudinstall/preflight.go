package cloudinstall

import authv1 "k8s.io/api/authorization/v1"

// PreflightResult splits blocking failures (the caller cannot perform the
// planned Kubernetes mutation) from non-fatal limitations and notes.
type PreflightResult struct {
	// Blocking lists denied permissions, admission failures, and incompatible
	// live state — a hard stop; do not mint a token.
	Blocking []string
	// Advisory lists non-fatal permission probes or exact checks Kubernetes cannot
	// perform yet (for example, admission inside a not-yet-created namespace).
	Advisory []string
}

// OK reports whether the install may proceed (no blocking denials).
func (r PreflightResult) OK() bool { return len(r.Blocking) == 0 }

type preflightCheck struct {
	desc     string
	blocking bool
	attrs    authv1.ResourceAttributes
}
