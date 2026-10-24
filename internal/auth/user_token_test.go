package auth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"

	"github.com/skyhook-io/radar/internal/cloud"
	pkgauth "github.com/skyhook-io/radar/pkg/auth"
)

func userTokenMode(t *testing.T) {
	t.Helper()
	t.Setenv("RADAR_CLOUD_MODE", "true")
	pkgauth.SetCredentialMode(pkgauth.CredentialsUserToken)
	t.Cleanup(func() { pkgauth.SetCredentialMode(pkgauth.CredentialsImpersonate) })
}

func serveUserToken(t *testing.T, reviewer func(context.Context, string) (*User, error), user, groups, token string) (*httptest.ResponseRecorder, *User) {
	t.Helper()
	cfg := proxyConfig()
	cfg.TokenReviewer = reviewer
	var got *User
	handler := cloud.AuthenticatedTunnelHandler(Authenticate(cfg)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = UserFromContext(r.Context())
	})))
	req := httptest.NewRequest(http.MethodGet, "/api/topology", nil)
	req.Header.Set(cfg.UserHeader, user)
	req.Header.Set(cfg.GroupsHeader, groups)
	if token != "" {
		req.Header.Set(pkgauth.UserTokenHeader, token)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec, got
}

func reviewerFor(username string, groups ...string) func(context.Context, string) (*User, error) {
	return func(_ context.Context, token string) (*User, error) {
		switch token {
		case "good-token":
		case "flaky-token":
			return nil, errors.New("dial tcp: i/o timeout")
		default:
			return nil, apierrors.NewUnauthorized("token expired")
		}
		return &User{Username: username, Groups: groups, UID: "uid-1", Extra: map[string][]string{"scopes.example.io": {"a"}}}, nil
	}
}

func TestUserToken_TokenIdentityComesFromTheAPIServer(t *testing.T) {
	userTokenMode(t)
	rec, got := serveUserToken(t, reviewerFor("oidc:alice@example.com", "k8s-admins", "system:authenticated"),
		"oidc:alice@example.com", "k8s-admins-forged,radar:member,radar:org:o1,radar:user:u1", "good-token")
	if rec.Code != http.StatusOK || got == nil {
		t.Fatalf("status = %d, user = %v", rec.Code, got)
	}
	if got.Token != "good-token" || got.UID != "uid-1" || got.Extra["scopes.example.io"][0] != "a" {
		t.Fatalf("token identity not carried: %+v", got)
	}
	if !slices.Equal(got.Groups, []string{"k8s-admins", "system:authenticated"}) {
		t.Fatalf("groups = %v, want the API server's groups only", got.Groups)
	}
	if !slices.Equal(got.HubGroups, []string{"radar:member", "radar:org:o1", "radar:user:u1"}) {
		t.Fatalf("hub groups = %v", got.HubGroups)
	}
	ctx := ContextWithUser(context.Background(), got)
	if role := CloudRoleFromContext(ctx); role != RoleMember {
		t.Fatalf("cloud role = %q, want member from the Hub groups", role)
	}
}

func TestUserToken_ForwardedUserMustMatchToken(t *testing.T) {
	userTokenMode(t)
	rec, got := serveUserToken(t, reviewerFor("oidc:bob@example.com"), "oidc:alice@example.com", "radar:owner", "good-token")
	if rec.Code != http.StatusForbidden || got != nil {
		t.Fatalf("status = %d, want 403 without reaching the handler", rec.Code)
	}
}

func TestUserToken_RejectedTokenIs403(t *testing.T) {
	userTokenMode(t)
	rec, got := serveUserToken(t, reviewerFor("oidc:alice@example.com"), "oidc:alice@example.com", "radar:owner", "expired-token")
	if rec.Code != http.StatusForbidden || got != nil {
		t.Fatalf("status = %d, want 403 (a 401 reloads the web client in a loop)", rec.Code)
	}
}

func TestUserToken_ReservedTokenGroupRefused(t *testing.T) {
	userTokenMode(t)
	for _, g := range []string{"radar:system", "radar:ai:reader", "cloud:owner", "system:masters"} {
		rec, got := serveUserToken(t, reviewerFor("oidc:alice@example.com", "shop-admins", g), "oidc:alice@example.com", "radar:member", "good-token")
		if rec.Code != http.StatusForbidden || got != nil {
			t.Errorf("token group %s: status = %d, want 403", g, rec.Code)
		}
	}
}

func TestUserToken_ReviewOutageIs503(t *testing.T) {
	userTokenMode(t)
	rec, got := serveUserToken(t, reviewerFor("oidc:alice@example.com"), "oidc:alice@example.com", "radar:owner", "flaky-token")
	if rec.Code != http.StatusServiceUnavailable || got != nil {
		t.Fatalf("status = %d, want 503 so the person isn't sent to sign in again", rec.Code)
	}
}

func TestUserToken_NoTokenHumanKeepsKubernetesGroupsApartFromHubGroups(t *testing.T) {
	userTokenMode(t)
	rec, got := serveUserToken(t, nil, "oidc:alice@example.com", "k8s-admins,radar:owner,radar:org:o1", "")
	if rec.Code != http.StatusOK || got == nil {
		t.Fatalf("status = %d", rec.Code)
	}
	if got.Token != "" || got.System {
		t.Fatalf("no-token human got %+v", got)
	}
	if !slices.Equal(got.Groups, []string{"k8s-admins"}) {
		t.Fatalf("groups = %v, want radar:* groups excluded from the Kubernetes identity", got.Groups)
	}
	if !slices.Equal(got.HubGroups, []string{"radar:owner", "radar:org:o1"}) {
		t.Fatalf("hub groups = %v", got.HubGroups)
	}
}

func TestUserToken_SystemIdentityAllowList(t *testing.T) {
	userTokenMode(t)
	for _, tc := range []struct {
		user, groups string
		system       bool
	}{
		{"radar:system:hub-timeline-puller", "radar:system", true},
		{"radar:system:alerts:org-1", "radar:system", true},
		{"radar:ai:org-1", "radar:ai:reader", true},
		{"radar:system:alerts:org-1", "radar:system,radar:owner", false},
		{"radar:ai:org-1", "radar:system", false},
		{"oidc:alice@example.com", "radar:system", false},
	} {
		rec, got := serveUserToken(t, nil, tc.user, tc.groups, "")
		if rec.Code != http.StatusOK || got == nil {
			t.Fatalf("%s/%s: status = %d", tc.user, tc.groups, rec.Code)
		}
		if got.System != tc.system {
			t.Errorf("%s/%s: system = %v, want %v", tc.user, tc.groups, got.System, tc.system)
		}
		if tc.system && CloudRoleFromContext(ContextWithUser(context.Background(), got)) != RoleNone {
			t.Errorf("%s: system identity must carry no Hub role", tc.user)
		}
	}
}

func TestUserToken_ReservedForwardedGroupRejected(t *testing.T) {
	userTokenMode(t)
	rec, got := serveUserToken(t, nil, "oidc:alice@example.com", "system:masters,radar:viewer", "")
	if rec.Code != http.StatusForbidden || got != nil {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
}

func TestUserToken_OffByDefaultKeepsCloudVocabularyRule(t *testing.T) {
	t.Setenv("RADAR_CLOUD_MODE", "true")
	rec, got := serveUserToken(t, nil, "oidc:alice@example.com", "k8s-admins,radar:viewer", "good-token")
	if rec.Code != http.StatusForbidden || got != nil {
		t.Fatalf("status = %d, want impersonate mode to keep rejecting non-radar groups", rec.Code)
	}
}
