package server

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.ate.dev/substrate/api/ateapi/v1"
	authv1 "k8s.io/api/authentication/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/k8s"
	substrateapi "github.com/skyhook-io/radar/internal/substrate"
	pkgauth "github.com/skyhook-io/radar/pkg/auth"
)

// mintRecorder stands in for the requesting user's Kubernetes client: it
// records whom each mint was made as and answers TokenRequests for
// ate-system/ate-client.
type mintRecorder struct {
	t     *testing.T
	mu    sync.Mutex
	asked []string // users whose client was asked for
	mints []string // users whose client minted
	// deny is the users whose access cannot mint.
	deny       map[string]bool
	expires    time.Time
	firstToken string
}

func (m *mintRecorder) count() (asked, minted int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.asked), len(m.mints)
}

func (m *mintRecorder) install(t *testing.T) {
	prev := substrateCallerClient
	substrateCallerClient = func(_ *Server, r *http.Request) kubernetes.Interface {
		user := auth.UserFromContext(r.Context()).Username
		m.mu.Lock()
		m.asked = append(m.asked, user)
		m.mu.Unlock()
		c := fake.NewClientset()
		c.PrependReactor("create", "serviceaccounts", func(a k8stesting.Action) (bool, runtime.Object, error) {
			create := a.(k8stesting.CreateActionImpl)
			req := create.GetObject().(*authv1.TokenRequest)
			if a.GetSubresource() != "token" || a.GetNamespace() != "ate-system" || create.Name != "ate-client" || len(req.Spec.Audiences) != 1 || req.Spec.Audiences[0] != substrateapi.APIServerName {
				m.t.Errorf("unexpected mint: %s %s/%s %+v", a.GetSubresource(), a.GetNamespace(), create.Name, req.Spec)
			}
			if m.deny[user] {
				return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: "serviceaccounts/token"}, "ate-client", nil)
			}
			m.mu.Lock()
			token := fullToken
			if len(m.mints) == 0 && m.firstToken != "" {
				token = m.firstToken
			}
			m.mints = append(m.mints, user)
			m.mu.Unlock()
			return true, &authv1.TokenRequest{Status: authv1.TokenRequestStatus{Token: token, ExpirationTimestamp: metav1.NewTime(m.expires)}}, nil
		})
		return c
	}
	t.Cleanup(func() { substrateCallerClient = prev })
}

// cloudSubstrate is Radar in Radar Hub with a fake Substrate the owner's
// minted token reaches.
func cloudSubstrate(t *testing.T) (*Server, *mintRecorder) {
	t.Helper()
	s := substrateTestServer(t, substrateapi.Kube{})
	t.Setenv("RADAR_CLOUD_MODE", "true")
	endpoint, caFile := startFakeSubstrate(t, defaultFake())
	s.substrate.CallerDial = substrateapi.Settings{Endpoint: endpoint, CAFile: caFile}
	m := &mintRecorder{t: t, expires: time.Now().Add(time.Hour)}
	m.install(t)
	return s, m
}

func hubUser(name, role string) *auth.User {
	return &auth.User{Username: name, Groups: []string{"radar:" + role}}
}

// substrateAPIRoutes are every route that reads Substrate's API.
func substrateAPIRoutes(s *Server) map[string]struct {
	h      http.HandlerFunc
	params map[string]string
} {
	actor := map[string]string{"atespace": "research", "name": "lit-0201"}
	return map[string]struct {
		h      http.HandlerFunc
		params map[string]string
	}{
		"actors":    {s.handleSubstrateActors, nil},
		"actor":     {s.handleSubstrateActor, actor},
		"logs":      {s.handleSubstrateActorLogs, actor},
		"workers":   {s.handleSubstrateWorkers, nil},
		"templates": {s.handleSubstrateTemplates, nil},
		"template":  {s.handleSubstrateTemplate, map[string]string{"atespace": "research", "name": "lang"}},
		"pool":      {s.handleSubstratePool, map[string]string{"namespace": "agents", "name": "agent-pool"}},
	}
}

// An org owner in Radar Hub reads Substrate as the shared identity, minted
// with the owner's own Kubernetes access and cached for the next owners.
func TestSubstrateHubOwnerReadsAsTheSharedIdentityMintedAsThemselves(t *testing.T) {
	s, mints := cloudSubstrate(t)
	clock := time.Now()
	var clockMu sync.Mutex
	s.substrate.Now = func() time.Time { clockMu.Lock(); defer clockMu.Unlock(); return clock }
	alice, bob := hubUser("alice", "owner"), hubUser("bob", "owner")

	var ws substrateWorkspaceResponse
	call(t, s, s.handleSubstrateWorkspace, substrateCall{path: "/api/substrate/workspace", user: alice}, &ws)
	if ws.APITier.State != substrateapi.TierConnected || ws.APITier.Identity == nil || ws.APITier.Identity.Mode != substrateapi.IdentitySharedAsCaller || ws.APITier.Identity.Principal != substrateapi.SharedPrincipal {
		t.Fatalf("owner tier = %+v", ws.APITier)
	}
	if _, minted := mints.count(); minted != 1 || mints.mints[0] != "alice" {
		t.Fatalf("minted as %v", mints.mints)
	}

	var actors substrateActorsResponse
	if rec := call(t, s, s.handleSubstrateActors, substrateCall{path: "/api/substrate/actors", user: bob}, &actors); rec.Code != http.StatusOK || actors.Total == nil {
		t.Fatalf("second owner = %d %s", rec.Code, rec.Body)
	}
	// bob proves his own access once; then his requests reuse the proof
	// and the shared token.
	call(t, s, s.handleSubstrateActors, substrateCall{path: "/api/substrate/actors", user: bob}, &actors)
	if _, minted := mints.count(); minted != 2 || mints.mints[1] != "bob" {
		t.Errorf("each owner proves their own access once: %v", mints.mints)
	}

	// Ten minutes before expiry, the next owner's request mints again, as
	// that owner.
	clockMu.Lock()
	clock = mints.expires.Add(-9 * time.Minute)
	clockMu.Unlock()
	if rec := call(t, s, s.handleSubstrateActors, substrateCall{path: "/api/substrate/actors", user: bob}, &actors); rec.Code != http.StatusOK {
		t.Fatalf("after re-mint = %d %s", rec.Code, rec.Body)
	}
	if _, minted := mints.count(); minted != 3 || mints.mints[2] != "bob" {
		t.Errorf("re-minted near expiry as the requesting owner: %v", mints.mints)
	}

	var status substrateStatusResponse
	call(t, s, s.handleSubstrateStatus, substrateCall{path: "/api/substrate/status", user: alice}, &status)
	if status.CanConfigure || status.APITier.State != substrateapi.TierConnected {
		t.Errorf("owner status = %+v", status)
	}
	for _, h := range []http.HandlerFunc{s.handlePutSubstrateConnection} {
		if rec := call(t, s, h, substrateCall{path: "/api/substrate/connection", user: alice}, nil); rec.Code != http.StatusForbidden || substrateErrorCode(t, rec) != "operator_managed" {
			t.Errorf("Hub's connection is its operator's, as every integration's: %d %s", rec.Code, rec.Body)
		}
	}
}

func TestSubstrateAnySignedInUserCanProveTheirOwnAccess(t *testing.T) {
	s, mints := cloudSubstrate(t)
	for _, mode := range []string{"cloud", "proxy", "oidc"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("RADAR_CLOUD_MODE", fmt.Sprint(mode == "cloud"))
			if mode != "cloud" {
				s.authConfig.Mode = mode
			}
			for _, role := range []string{"owner", "member", "viewer", "none"} {
				rec := call(t, s, s.handleSubstrateActors, substrateCall{path: "/api/substrate/actors", user: hubUser(mode+role, role)}, nil)
				if rec.Code != http.StatusOK {
					t.Errorf("%s: %d %s", role, rec.Code, rec.Body)
				}
			}
		})
	}
	if _, minted := mints.count(); minted != 12 {
		t.Errorf("each user must mint once: %d", minted)
	}
}

// An owner whose Kubernetes access cannot mint the token gets a readable
// reason, and is not served, even while another owner's token is valid.
func TestSubstrateHubOwnerWhoCannotMint(t *testing.T) {
	s, mints := cloudSubstrate(t)
	mints.deny = map[string]bool{"o": true}
	// Another owner's token is cached and valid; o still needs their own.
	if rec := call(t, s, s.handleSubstrateActors, substrateCall{path: "/api/substrate/actors", user: hubUser("a", "owner")}, nil); rec.Code != http.StatusOK {
		t.Fatalf("owner a = %d %s", rec.Code, rec.Body)
	}
	var ws substrateWorkspaceResponse
	call(t, s, s.handleSubstrateWorkspace, substrateCall{path: "/api/substrate/workspace", user: hubUser("o", "owner")}, &ws)
	if ws.APITier.State != substrateapi.TierFailed || ws.APITier.Code != substrateapi.CodeMintDenied || !strings.Contains(ws.APITier.Reason, "create serviceaccounts/token in ate-system") ||
		ws.APITier.Identity == nil || ws.APITier.Identity.Mode != substrateapi.IdentitySharedAsCaller {
		t.Errorf("tier = %+v", ws.APITier)
	}
	rec := call(t, s, s.handleSubstrateActors, substrateCall{path: "/api/substrate/actors", user: hubUser("o", "owner")}, nil)
	if rec.Code != http.StatusForbidden || substrateErrorCode(t, rec) != substrateapi.CodeMintDenied {
		t.Errorf("actors = %d %s", rec.Code, rec.Body)
	}
	if rec := call(t, s, s.handleSubstrateActors, substrateCall{path: "/api/substrate/actors", user: hubUser("a", "owner")}, nil); rec.Code != http.StatusOK {
		t.Errorf("owner a still reads = %d %s", rec.Code, rec.Body)
	}
}

// A Hub owner's failed tier still names the identity, whatever failed, so
// the UI never offers settings the owner does not have.
func TestSubstrateHubOwnerFailuresNameTheIdentity(t *testing.T) {
	s, _ := cloudSubstrate(t)
	s.substrate.CallerDial = substrateapi.Settings{Endpoint: "127.0.0.1:1"}
	var ws substrateWorkspaceResponse
	call(t, s, s.handleSubstrateWorkspace, substrateCall{path: "/api/substrate/workspace", user: hubUser("o", "owner")}, &ws)
	if ws.APITier.State != substrateapi.TierFailed || ws.APITier.Identity == nil || ws.APITier.Identity.Mode != substrateapi.IdentitySharedAsCaller {
		t.Errorf("unreachable owner tier = %+v", ws.APITier)
	}
	prev := substrateCallerClient
	substrateCallerClient = func(*Server, *http.Request) kubernetes.Interface { return nil }
	t.Cleanup(func() { substrateCallerClient = prev })
	call(t, s, s.handleSubstrateWorkspace, substrateCall{path: "/api/substrate/workspace", user: hubUser("o", "owner")}, &ws)
	if ws.APITier.Code != substrateapi.CodeMintDenied || ws.APITier.Identity == nil || ws.APITier.Identity.Mode != substrateapi.IdentitySharedAsCaller {
		t.Errorf("no user client = %+v", ws.APITier)
	}
}

func TestSubstrateUserTokenRequiredWithWarmProof(t *testing.T) {
	s, mints := cloudSubstrate(t)
	prev := pkgauth.CurrentCredentialMode()
	pkgauth.SetCredentialMode(pkgauth.CredentialsUserToken)
	t.Cleanup(func() { pkgauth.SetCredentialMode(prev) })

	user := &auth.User{Username: "alice", Groups: []string{"system:authenticated"}, Token: "user-token"}
	req := substrateCall{path: "/api/substrate/actors", user: user}
	if rec := call(t, s, s.handleSubstrateActors, req, nil); rec.Code != http.StatusOK {
		t.Fatalf("warm proof: %d %s", rec.Code, rec.Body)
	}

	user.Token = ""
	rec := call(t, s, s.handleSubstrateActors, req, nil)
	if rec.Code != http.StatusForbidden || substrateErrorCode(t, rec) != substrateapi.CodeMintDenied ||
		!strings.Contains(rec.Body.String(), k8s.UserTokenRequiredMessage) {
		t.Errorf("request missing its token: %d %s", rec.Code, rec.Body)
	}
	if asked, minted := mints.count(); asked != 1 || minted != 1 {
		t.Errorf("refused request reached the Kubernetes client: %d client lookups, %d mints", asked, minted)
	}

	system := &auth.User{Username: "radar:system:test", Groups: []string{"radar:system"}, System: true}
	ctx := auth.ContextWithUser(t.Context(), system)
	if _, tier := s.substrateGate((&http.Request{}).WithContext(ctx)); tier != nil {
		t.Errorf("system request gate: %+v", tier)
	}
}

func TestSubstrateCallerRemintsAfterRemoteRejection(t *testing.T) {
	for name, read := range map[string]func(*substrateapi.Manager, context.Context) string{
		"snapshot": func(m *substrateapi.Manager, ctx context.Context) string {
			_, tier := m.Snapshot(ctx)
			return tier.Code
		},
		"scoped actors": func(m *substrateapi.Manager, ctx context.Context) string {
			_, _, tier := m.AtespaceActors(ctx, "research")
			if tier != nil {
				return tier.Code
			}
			return ""
		},
		"detail RPC": func(m *substrateapi.Manager, ctx context.Context) string {
			tier, code, _ := m.Call(ctx, func(ctx context.Context, client ateapi.ControlClient) error {
				_, err := client.ListAtespaces(ctx, &ateapi.ListAtespacesRequest{})
				return err
			})
			if tier != nil {
				return tier.Code
			}
			return code
		},
	} {
		t.Run(name, func(t *testing.T) {
			s, mints := cloudSubstrate(t)
			mints.firstToken = "rejected-token"
			m := s.substrateManager()
			now := time.Now()
			var elapsed atomic.Int64
			m.Now = func() time.Time { return now.Add(time.Duration(elapsed.Load())) }
			ctx := auth.ContextWithUser(t.Context(), hubUser("alice", "owner"))
			req, tier := s.substrateGate((&http.Request{}).WithContext(ctx))
			if tier != nil {
				t.Fatalf("gate: %+v", tier)
			}
			if code := read(m, req.Context()); code != substrateapi.CodeUnauthenticated {
				t.Fatalf("first request = %q, want unauthenticated", code)
			}
			// The next request mints, even if the failed snapshot is still
			// cached. The recovered token has the same expiry as the old one.
			code := read(m, req.Context())
			if _, minted := mints.count(); minted != 2 {
				t.Fatalf("next request minted %d total tokens, want 2", minted)
			}
			if name == "snapshot" {
				elapsed.Store(int64(substrateapi.Freshness + time.Second))
				code = read(m, req.Context())
			}
			if code != "" {
				t.Errorf("request after mint = %q, want success", code)
			}
		})
	}
}
