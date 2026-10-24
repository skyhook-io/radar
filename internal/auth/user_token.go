package auth

import (
	"encoding/json"
	"log"
	"net/http"

	apierrors "k8s.io/apimachinery/pkg/api/errors"

	pkgauth "github.com/skyhook-io/radar/pkg/auth"
)

// userTokenIdentity resolves the identity of a tunneled request in
// user-token mode. A request carrying a token is the identity the API server
// reports for that token; the forwarded username must match it. Without a
// token, an allow-listed system identity is accepted as is, and anyone else
// keeps the forwarded identity for cached reads only (see k8s.UserRESTConfig).
// radar:* and cloud:* groups never become part of a Kubernetes identity: they
// carry the Hub role and nothing else.
func userTokenIdentity(w http.ResponseWriter, r *http.Request, cfg Config, username string, groups []string) (*User, bool) {
	if !ForwardedIdentityAllowed(username, groups, false) {
		rejectDisallowedIdentity(w, username, true)
		return nil, false
	}
	hubGroups := []string{}
	var kubeGroups []string
	for _, g := range groups {
		if pkgauth.IsRadarVocabularyGroup(g) || (pkgauth.IsReservedPrincipal(g) && g != "system:authenticated") {
			hubGroups = append(hubGroups, g)
		} else {
			kubeGroups = append(kubeGroups, g)
		}
	}

	token := r.Header.Get(pkgauth.UserTokenHeader)
	if token == "" {
		if pkgauth.IsSystemIdentity(username, groups) {
			return &User{Username: username, Groups: groups, System: true, HubGroups: []string{}}, true
		}
		return &User{Username: username, Groups: kubeGroups, HubGroups: hubGroups}, true
	}

	if cfg.TokenReviewer == nil {
		writeUserTokenError(w, http.StatusServiceUnavailable, "token_review_unavailable", "Radar cannot verify Kubernetes tokens", cfg.Mode)
		return nil, false
	}
	reviewed, err := cfg.TokenReviewer(r.Context(), token)
	if apierrors.IsUnauthorized(err) {
		// 403, not 401: a 401 makes the web client reload, and a token the
		// API server doesn't trust (wrong issuer or audience) stays rejected
		// however often the person signs in.
		log.Printf("[auth] Kubernetes rejected the token for %q: %v", username, err)
		writeUserTokenError(w, http.StatusForbidden, "token_rejected", "Your Kubernetes API server does not accept your sign-in token. Check that it trusts this identity provider and client.", cfg.Mode)
		return nil, false
	}
	if err != nil {
		// Timeouts, 5xx, a server without SelfSubjectReview: the token may be
		// fine, so signing in again would not help.
		log.Printf("[auth] could not review the token for %q: %v", username, err)
		writeUserTokenError(w, http.StatusServiceUnavailable, "token_review_unavailable", "Radar could not check your sign-in token with the Kubernetes API server", cfg.Mode)
		return nil, false
	}
	if reviewed.Username != username {
		log.Printf("[auth] token identity %q does not match forwarded user %q", reviewed.Username, username)
		writeUserTokenError(w, http.StatusForbidden, "token_identity_mismatch", "Your sign-in token belongs to a different Kubernetes user ("+reviewed.Username+") than the one forwarded ("+username+")", cfg.Mode)
		return nil, false
	}
	for _, g := range reviewed.Groups {
		// The API server applies these groups to the token directly, so one
		// named like a Radar group would match the chart's radar:system or
		// radar:ai:reader bindings, and an IdP group named system:masters
		// would be cluster-admin. Only system:authenticated is expected here.
		if pkgauth.IsRadarVocabularyGroup(g) || (pkgauth.IsReservedPrincipal(g) && g != "system:authenticated") {
			log.Printf("[auth] token for %q carries reserved group %q", reviewed.Username, g)
			writeUserTokenError(w, http.StatusForbidden, "token_reserved_group", "Your identity provider puts you in group "+g+"; groups starting with radar:, cloud: or system: are reserved. Rename the group or set a groups prefix on your API server.", cfg.Mode)
			return nil, false
		}
	}
	user := *reviewed
	user.Token = token
	user.HubGroups = hubGroups
	return &user, true
}

func writeUserTokenError(w http.ResponseWriter, status int, reason, message, mode string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{
		"error":    message,
		"reason":   reason,
		"authMode": mode,
	})
}
