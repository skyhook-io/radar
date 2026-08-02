package auth

import "strings"

// Forwarded-identity filtering applies ONLY under Radar Cloud. In OSS proxy /
// OIDC modes the operator owns the entire identity chain (their proxy or IdP
// is the configured trust root), so Radar forwards whatever they assert. Under
// Cloud, the control plane is the asserting party over the tunnel, so Radar
// constrains what it will impersonate — the SA's `impersonate` grant is
// cluster-scoped and K8s RBAC can't restrict it to a prefix, making this
// acceptance point the only place the constraint can live.

// reservedPrincipalPrefixes are Kubernetes-reserved identity namespaces a
// Cloud-forwarded identity must never assert. Blocking them removes the
// worst-case escalation ceiling (system:masters, the kubelet/node identity,
// any ServiceAccount that may hold powers beyond the owner tier). It does NOT
// contain a control plane that asserts an ordinary radar: tier — that's
// bounded by the tier's own RBAC — and is deliberately not sold as such.
var reservedPrincipalPrefixes = []string{
	"system:", // system:masters, system:authenticated, system:nodes, system:serviceaccounts, …
}

// IsReservedPrincipal reports whether a username or group falls in a reserved
// Kubernetes namespace. Matched as an exact leading prefix so Radar's own
// namespaced synthetic principals (e.g. "cloud:system:alerts:…") are
// unaffected — only leading "system:" is reserved.
func IsReservedPrincipal(principal string) bool {
	for _, p := range reservedPrincipalPrefixes {
		if strings.HasPrefix(principal, p) {
			return true
		}
	}
	return false
}

// radarVocabularyPrefixes are the group namespaces the Radar Cloud control
// plane legitimately injects. A compromised hub can still assert an ordinary
// radar: tier, but cannot introduce identities outside the vocabulary the
// chart binds.
var radarVocabularyPrefixes = []string{"radar:", "cloud:"}

// IsRadarVocabularyGroup reports whether a group is in the Radar Cloud group
// vocabulary (radar:* canonical or cloud:* legacy).
func IsRadarVocabularyGroup(group string) bool {
	for _, p := range radarVocabularyPrefixes {
		if strings.HasPrefix(group, p) {
			return true
		}
	}
	return false
}

// ForwardedIdentityAllowed decides whether a forwarded (username, groups)
// identity may be impersonated. Outside Cloud it always returns true — the
// operator owns the identity chain. Under Cloud it enforces both:
//
//   - reserved-principal rejection: username and every group must be outside
//     the reserved K8s namespaces (blocks system:masters etc.);
//   - vocabulary allowlist: username must be a Cloud identity and every group
//     must be radar:*/cloud:*.
//
// Returns false to reject the whole request (never silently drops values — a
// reserved or out-of-vocabulary principal from the tunnel is an anomaly).
func ForwardedIdentityAllowed(username string, groups []string, cloudMode bool) bool {
	if !cloudMode {
		return true
	}
	if IsReservedPrincipal(username) || !isCloudUsername(username) {
		return false
	}
	for _, g := range groups {
		if IsReservedPrincipal(g) || !IsRadarVocabularyGroup(g) {
			return false
		}
	}
	return true
}

// isCloudUsername reports whether a username is one the Radar Cloud control
// plane legitimately injects: the opaque per-user id the hub sets as
// X-Forwarded-User, or an internal synthetic principal namespaced under
// "cloud:system:". The hub never forwards a K8s-reserved username, so
// rejecting reserved is the meaningful check; the opaque WorkOS id has no
// fixed prefix to match beyond "non-empty and not reserved".
func isCloudUsername(username string) bool {
	return username != "" && !IsReservedPrincipal(username)
}
