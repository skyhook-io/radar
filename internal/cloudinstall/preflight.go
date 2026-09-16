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
	// Denied and Unverifiable are subsets of Blocking, kept so a presenter can
	// say what kind of stop this is without parsing the lines. Denied is the
	// caller's identity lacking a permission (an access review said no, or the
	// dry run came back 403). Unverifiable is Radar unable to prove the exact
	// mutation (the prepared chart hid a Secret). Everything else in Blocking
	// is the cluster refusing the change itself: an admission webhook, an
	// object already owned by something else, an API the cluster does not
	// serve.
	Denied       []string
	Unverifiable []string
}

// OK reports whether the install may proceed (no blocking denials).
func (r PreflightResult) OK() bool { return len(r.Blocking) == 0 }

// BlockCause is the one-word answer to "why can't this install proceed",
// chosen so the person is told the thing that would actually unblock them.
type BlockCause string

const (
	// BlockCausePermissions: every blocker is a permission the caller lacks.
	// Someone with broader access can do this exact install.
	BlockCausePermissions BlockCause = "permissions"
	// BlockCauseCluster: at least one blocker is the cluster refusing the
	// change. More permission would not help; something has to be resolved.
	BlockCauseCluster BlockCause = "cluster"
	// BlockCauseVerification: nothing was refused, but Radar could not prove
	// what the install would do, so it declines to do it blind.
	BlockCauseVerification BlockCause = "verification"
)

// Cause classifies a blocked result. A cluster refusal outranks a denial
// because permission alone would not clear it; a denial outranks
// verification because the denial is the actionable one.
func (r PreflightResult) Cause() BlockCause {
	if r.OK() {
		return ""
	}
	if len(r.Blocking) > len(r.Denied)+len(r.Unverifiable) {
		return BlockCauseCluster
	}
	if len(r.Denied) > 0 {
		return BlockCausePermissions
	}
	return BlockCauseVerification
}

func (r *PreflightResult) blockRefused(line string) { r.Blocking = append(r.Blocking, line) }

func (r *PreflightResult) blockDenied(line string) {
	r.Blocking = append(r.Blocking, line)
	r.Denied = append(r.Denied, line)
}

func (r *PreflightResult) blockUnverifiable(line string) {
	r.Blocking = append(r.Blocking, line)
	r.Unverifiable = append(r.Unverifiable, line)
}

type preflightCheck struct {
	desc     string
	blocking bool
	attrs    authv1.ResourceAttributes
}
