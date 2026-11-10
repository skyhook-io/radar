package auth

import (
	"fmt"
	"strings"
	"sync/atomic"
)

// CredentialMode selects how Radar authenticates the Kubernetes calls it
// makes for a user.
type CredentialMode string

const (
	// CredentialsImpersonate sends Radar's ServiceAccount token with
	// Impersonate-User/Group headers. The default.
	CredentialsImpersonate CredentialMode = "impersonate"
	// CredentialsUserToken sends the user's own token, received on each
	// request, and never impersonates. Requests without a token may read
	// Radar's cache but make no live calls, except the system identities
	// accepted by IsSystemIdentity.
	CredentialsUserToken CredentialMode = "user-token"
)

// UserTokenHeader carries the user's Kubernetes bearer token from the
// trusted upstream to Radar in user-token mode.
const UserTokenHeader = "X-Forwarded-Kubernetes-Token"

// UserTokenRequiredReason marks a live call refused because the request
// carried no user token.
const UserTokenRequiredReason = "user_token_required"

var credentialMode atomic.Value

// ParseCredentialMode validates a configured credential mode. Empty means
// impersonate.
func ParseCredentialMode(v string) (CredentialMode, error) {
	switch CredentialMode(strings.TrimSpace(v)) {
	case "", CredentialsImpersonate:
		return CredentialsImpersonate, nil
	case CredentialsUserToken:
		return CredentialsUserToken, nil
	}
	return "", fmt.Errorf("invalid Kubernetes credential mode %q (want %q or %q)", v, CredentialsImpersonate, CredentialsUserToken)
}

// SetCredentialMode sets the process-wide credential mode. Called once at
// startup.
func SetCredentialMode(m CredentialMode) { credentialMode.Store(m) }

// CurrentCredentialMode returns the process-wide credential mode.
func CurrentCredentialMode() CredentialMode {
	if m, ok := credentialMode.Load().(CredentialMode); ok {
		return m
	}
	return CredentialsImpersonate
}

// UserTokenMode reports whether Radar runs in user-token mode.
func UserTokenMode() bool { return CurrentCredentialMode() == CredentialsUserToken }

const (
	systemUserPrefix = "radar:system:"
	systemGroup      = "radar:system"
	aiUserPrefix     = "radar:ai:"
	aiReaderGroup    = "radar:ai:reader"
)

// IsSystemIdentity reports whether an identity is exactly one of the
// background service identities a trusted upstream asserts without a user
// token: radar:system:<job> in group radar:system, or radar:ai:<org> in group
// radar:ai:reader. Any extra group disqualifies it, so a human identity can't
// pass for one by adding a group.
func IsSystemIdentity(username string, groups []string) bool {
	if len(groups) != 1 {
		return false
	}
	switch {
	case strings.HasPrefix(username, systemUserPrefix) && len(username) > len(systemUserPrefix):
		return groups[0] == systemGroup
	case strings.HasPrefix(username, aiUserPrefix) && len(username) > len(aiUserPrefix) && !strings.HasPrefix(username, systemUserPrefix):
		return groups[0] == aiReaderGroup
	}
	return false
}
