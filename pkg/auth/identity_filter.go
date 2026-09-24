package auth

import "strings"

// Identity filtering runs wherever Radar accepts an identity it will later
// impersonate: proxy headers, OIDC login, and session cookies. The SA's
// `impersonate` grant is cluster-scoped and K8s RBAC can't restrict it to a
// prefix, so Radar enforces the constraint where it accepts identities. It
// does not constrain anyone holding the SA's token directly.
//
// Every mode rejects Kubernetes-reserved principals. Under Radar Cloud the
// control plane is the asserting party over the tunnel, so Radar additionally
// requires the Cloud identity vocabulary.

// reservedPrincipalPrefixes are Kubernetes-reserved identity namespaces an
// impersonated identity must never assert. Blocking them removes the
// worst-case escalation ceiling (system:masters, the kubelet/node identity,
// any ServiceAccount). It does NOT contain an identity source that asserts an
// ordinary user or group bound to powerful RBAC, and is deliberately not sold
// as such.
var reservedPrincipalPrefixes = []string{
	"system:", // system:masters, system:authenticated, system:nodes, system:serviceaccounts, …
}

// IsReservedPrincipal reports whether a username or group falls in a reserved
// Kubernetes namespace. Matched as an exact leading prefix so Radar's own
// namespaced synthetic principals (e.g. "radar:system:alerts:…") are
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

// ForwardedIdentityAllowed decides whether an identity may be impersonated. It
// enforces:
//
//   - reserved-principal rejection (every mode): username and every group must
//     be outside the reserved K8s namespaces (blocks system:masters etc.);
//   - vocabulary allowlist (Cloud only): username must be a Cloud identity and
//     every group must be radar:*/cloud:*.
//
// Returns false to reject the whole identity — never silently drops values,
// which would quietly change what the user is authorized to do.
func ForwardedIdentityAllowed(username string, groups []string, cloudMode bool) bool {
	if IsReservedPrincipal(username) {
		return false
	}
	for _, g := range groups {
		if IsReservedPrincipal(g) {
			return false
		}
	}
	if !cloudMode {
		return true
	}
	if !isCloudUsername(username) {
		return false
	}
	for _, g := range groups {
		if !IsRadarVocabularyGroup(g) {
			return false
		}
	}
	return true
}

// isCloudUsername reports whether a username is one the Radar Cloud control
// plane legitimately injects: the opaque per-user id the hub sets as
// X-Forwarded-User, or an internal synthetic principal namespaced under
// "radar:system:". The hub never forwards a K8s-reserved username, so
// rejecting reserved is the meaningful check; the opaque WorkOS id has no
// fixed prefix to match beyond "non-empty and not reserved".
func isCloudUsername(username string) bool {
	return username != "" && !IsReservedPrincipal(username)
}
