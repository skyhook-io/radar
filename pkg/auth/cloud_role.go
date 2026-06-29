package auth

import (
	"context"
	"strings"
)

// CloudRole captures the user's tier under Radar Cloud's RBAC model.
// Cloud injects one of `cloud:owner` / `cloud:member` / `cloud:viewer`
// into X-Forwarded-Groups; this type extracts that signal.
//
// CloudRole is the **product-side** boundary (decides whether the user
// can attempt an operation in the UI / API). The K8s ClusterRoleBindings
// the chart ships are the **structural** boundary (decides whether the
// K8s API server allows the impersonated request). The two stack:
// CloudRole gates "can I attempt it?", K8s RBAC gates "can it succeed?".
type CloudRole string

const (
	// RoleNone means the request is not from a Cloud-attributed user
	// (OSS deploy, or a Cloud bug stripped the role group). AtLeast
	// returns true for RoleNone so non-Cloud deploys aren't gated.
	RoleNone   CloudRole = ""
	RoleViewer CloudRole = "viewer"
	RoleMember CloudRole = "member"
	RoleOwner  CloudRole = "owner"
)

const cloudGroupPrefix = "cloud:"

// ErrCodeCloudRoleInsufficient is the stable wire value emitted in 403
// response bodies (`error_code` field) when a request is denied by a
// Cloud role gate. frontend + MCP clients branch on this exact string —
// never rename. Hoisted out of internal/helm/handlers.go so the
// canonical value lives next to the role types it pertains to.
const ErrCodeCloudRoleInsufficient = "cloud_role_insufficient"

var tierRank = map[CloudRole]int{
	RoleViewer: 1,
	RoleMember: 2,
	RoleOwner:  3,
}

// CloudRoleFromGroups returns the highest-ranked Cloud tier present in
// the group list. Returns RoleNone when no `cloud:<tier>` group is
// found, which the call sites treat as "not running under Cloud."
func CloudRoleFromGroups(groups []string) CloudRole {
	var best CloudRole
	bestRank := 0
	for _, g := range groups {
		if !strings.HasPrefix(g, cloudGroupPrefix) {
			continue
		}
		role := CloudRole(strings.TrimPrefix(g, cloudGroupPrefix))
		rank, ok := tierRank[role]
		if !ok {
			continue
		}
		if rank > bestRank {
			best = role
			bestRank = rank
		}
	}
	return best
}

// CloudRoleFromContext is a convenience over UserFromContext +
// CloudRoleFromGroups. Returns RoleNone when no user is in context.
func CloudRoleFromContext(ctx context.Context) CloudRole {
	user := UserFromContext(ctx)
	if user == nil {
		return RoleNone
	}
	return CloudRoleFromGroups(user.Groups)
}

// AtLeast reports whether r meets or exceeds min. RoleNone (no Cloud
// role detected) bypasses the gate — non-Cloud deploys must not be
// blocked by Cloud-tier checks.
func (r CloudRole) AtLeast(min CloudRole) bool {
	if r == RoleNone {
		return true
	}
	return tierRank[r] >= tierRank[min]
}

// String returns the role name suitable for log/error messages.
func (r CloudRole) String() string {
	if r == RoleNone {
		return "none"
	}
	return string(r)
}
