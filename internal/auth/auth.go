// Package auth provides radar-specific authentication middleware and OIDC support.
// Reusable auth primitives (cookie management, RBAC namespace discovery, impersonation)
// live in pkg/auth. This package wraps them with radar-specific HTTP routing and audit logging.
package auth

import pkgauth "github.com/skyhook-io/radar/pkg/auth"

// Re-export types from pkg/auth for backward compatibility.
// All callers can continue to import "internal/auth" without changes.
type Config = pkgauth.Config
type User = pkgauth.User
type UserPermissions = pkgauth.UserPermissions
type PermissionCache = pkgauth.PermissionCache

// Re-export constants from pkg/auth
const DefaultCookieName = pkgauth.DefaultCookieName

// Re-export functions from pkg/auth
var (
	UserFromContext      = pkgauth.UserFromContext
	ContextWithUser      = pkgauth.ContextWithUser
	NewPermissionCache   = pkgauth.NewPermissionCache
	DiscoverNamespaces   = pkgauth.DiscoverNamespaces
	SubjectCanI          = pkgauth.SubjectCanI
	FilterNamespacesForUser = pkgauth.FilterNamespacesForUser
	CreateSessionCookie  = pkgauth.CreateSessionCookie
	ParseSessionCookie   = pkgauth.ParseSessionCookie
	ClearSessionCookie   = pkgauth.ClearSessionCookie
)
