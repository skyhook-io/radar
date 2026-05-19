// Package auth provides radar-specific authentication middleware and OIDC support.
// Reusable auth primitives (cookie management, RBAC namespace discovery, impersonation)
// live in pkg/auth. This package wraps them with radar-specific HTTP routing and audit logging.
package auth

import pkgauth "github.com/skyhook-io/radar/pkg/auth"

// Re-export types from pkg/auth for backward compatibility.
// All callers can continue to import "internal/auth" without changes.
type Config = pkgauth.Config
type User = pkgauth.User
type Session = pkgauth.Session
type SessionRevoker = pkgauth.SessionRevoker
type UserPermissions = pkgauth.UserPermissions
type PermissionCache = pkgauth.PermissionCache
type CloudRole = pkgauth.CloudRole
type APIKeyStore = pkgauth.APIKeyStore
type APIKey = pkgauth.APIKey

// Re-export constants from pkg/auth
const DefaultCookieName = pkgauth.DefaultCookieName

// Cloud role constants — re-exported from pkg/auth.
const (
	RoleNone   = pkgauth.RoleNone
	RoleViewer = pkgauth.RoleViewer
	RoleMember = pkgauth.RoleMember
	RoleOwner  = pkgauth.RoleOwner
)

// ErrCodeCloudRoleInsufficient is re-exported from pkg/auth so handlers
// don't have to import the package directly to write the wire value.
const ErrCodeCloudRoleInsufficient = pkgauth.ErrCodeCloudRoleInsufficient

// Re-export functions from pkg/auth
var (
	UserFromContext          = pkgauth.UserFromContext
	ContextWithUser          = pkgauth.ContextWithUser
	NewPermissionCache       = pkgauth.NewPermissionCache
	DiscoverNamespaces       = pkgauth.DiscoverNamespaces
	SubjectCanI              = pkgauth.SubjectCanI
	FilterNamespacesForUser  = pkgauth.FilterNamespacesForUser
	CreateSessionCookie      = pkgauth.CreateSessionCookie
	NewSessionID             = pkgauth.NewSessionID
	ParseSessionCookie       = pkgauth.ParseSessionCookie
	ClearSessionCookie       = pkgauth.ClearSessionCookie
	CloudRoleFromGroups      = pkgauth.CloudRoleFromGroups
	CloudRoleFromContext     = pkgauth.CloudRoleFromContext
	NewAPIKeyStore           = pkgauth.NewAPIKeyStore
)
