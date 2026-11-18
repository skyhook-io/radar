package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/k8s"
	substrateapi "github.com/skyhook-io/radar/internal/substrate"
	pkgauth "github.com/skyhook-io/radar/pkg/auth"
	authv1 "k8s.io/api/authentication/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

func TestSubstrateGateMatrix(t *testing.T) {
	for _, tc := range []struct {
		name, mode, role, code           string
		noUser, noClient, deny, disabled bool
	}{
		{name: "local private", mode: "local"},
		{name: "local shared", mode: "shared", code: substrateapi.CodeSharedRadar},
		{name: "no-auth in-cluster", mode: "in-cluster", code: substrateapi.CodeSharedRadar},
		{name: "OSS proxy", mode: "proxy", role: "member"},
		{name: "OSS OIDC", mode: "oidc", role: "viewer"},
		{name: "OSS mint denied", mode: "proxy", deny: true, code: substrateapi.CodeMintDenied},
		{name: "OSS no client", mode: "proxy", noClient: true, code: substrateapi.CodeMintDenied},
		{name: "OSS disabled", mode: "proxy", disabled: true, code: substrateapi.CodeAPIDisabled},
		{name: "Cloud owner", mode: "cloud", role: "owner"},
		{name: "Cloud member", mode: "cloud", role: "member"},
		{name: "Cloud viewer", mode: "cloud", role: "viewer"},
		{name: "Cloud no user", mode: "cloud", noUser: true, code: substrateapi.CodeMintDenied},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := substrateTestServer(t, substrateapi.Kube{})
			f := defaultFake()
			connectFake(t, s, f, fullToken)
			// A warm local snapshot must not bypass the managed caller proof.
			if _, tier := s.substrate.Snapshot(context.Background()); tier.State != substrateapi.TierConnected {
				t.Fatalf("warm: %+v", tier)
			}
			k, _ := s.substrate.Kube()
			saved, _, _ := s.substrate.Store.Get(k.Binding)
			s.substrate.CallerDial = saved.Settings
			switch tc.mode {
			case "shared":
				s.listenAddress = "0.0.0.0"
			case "in-cluster":
				s.listenAddress = "0.0.0.0"
				t.Setenv("KUBERNETES_SERVICE_HOST", "10.0.0.1")
			case "proxy", "oidc":
				s.authConfig.Mode = tc.mode
			case "cloud":
				t.Setenv("RADAR_CLOUD_MODE", "true")
			}
			if tc.disabled {
				t.Setenv("RADAR_SUBSTRATE_API", "disabled")
			}
			s.substrateInstallation()
			mints := &mintRecorder{t: t, expires: time.Now().Add(time.Hour), deny: map[string]bool{"caller": tc.deny}}
			mints.install(t)
			if tc.noClient {
				substrateCallerClient = func(*Server, *http.Request) kubernetes.Interface { return nil }
			}
			earlyRefusal := tc.code != "" && !tc.deny
			if earlyRefusal {
				s.substrate.Kube = func() (substrateapi.Kube, error) {
					t.Error("refused caller reached manager (file/dial/cache)")
					return substrateapi.Kube{}, errors.New("must not run")
				}
			}
			var user *auth.User
			if !tc.noUser && tc.mode != "local" && tc.mode != "shared" && tc.mode != "in-cluster" {
				user = hubUser("caller", tc.role)
			}
			rec := call(t, s, s.handleSubstrateActors, substrateCall{path: "/api/substrate/actors", user: user}, nil)
			if tc.code == "" {
				if rec.Code != 200 {
					t.Fatalf("allowed: %d %s", rec.Code, rec.Body)
				}
			} else if rec.Code != 403 || errorCode(t, rec) != tc.code {
				t.Fatalf("denied: %d %s", rec.Code, rec.Body)
			}
			asked, minted := mints.count()
			if earlyRefusal || tc.mode == "local" {
				if asked != 0 || minted != 0 {
					t.Errorf("early refusal/local minted: %d %d", asked, minted)
				}
			} else if tc.deny {
				if minted != 0 || asked != 1 {
					t.Errorf("denied proof: %d %d", asked, minted)
				}
			} else if minted != 1 {
				t.Errorf("caller not proven: %d", minted)
			}
			if tc.code != "" {
				var view substrateConnectionView
				rec = call(t, s, s.handleGetSubstrateConnection, substrateCall{path: "/api/substrate/connection", user: user}, &view)
				if rec.Code != 200 || view.View != "managed" || view.Access.Code != tc.code || view.Managed.Atespaces != nil {
					t.Fatalf("managed refusal: %d %s", rec.Code, rec.Body)
				}
				for _, field := range []string{`"settings":`, `"target":`, `"revision":`, `"saved":`, `"atespaces":[`} {
					if strings.Contains(rec.Body.String(), field) {
						t.Errorf("disclosed %s: %s", field, rec.Body)
					}
				}
			}
		})
	}
}

func TestSubstrateManagedViewAndInvalidInstallation(t *testing.T) {
	s, mints := cloudSubstrate(t)
	c := s.substrateInstallation()
	c.Settings = s.substrate.CallerDial
	c.Settings.Atespaces = []string{"research"}
	s.substrateInstallationConfig = c
	user := hubUser("member", "member")
	call(t, s, s.handleSubstrateActors, substrateCall{path: "/api/substrate/actors", user: user}, nil)
	var view substrateConnectionView
	rec := call(t, s, s.handleGetSubstrateConnection, substrateCall{path: "/api/substrate/connection", user: user}, &view)
	if rec.Code != 200 || view.View != "managed" || view.Access.State != substrateapi.TierConnected || view.Managed.Atespaces == nil || len(*view.Managed.Atespaces) == 0 || view.Managed.Trust != "caFile" || view.Managed.ReadsAs != "eachUser" {
		t.Fatalf("view: %d %s", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), c.Settings.CAFile) {
		t.Fatal("CA path disclosed")
	}
	before, _ := mints.count()
	s.substrateInstallationConfig.Err = errors.New("invalid installation settings")
	view = substrateConnectionView{}
	rec = call(t, s, s.handleGetSubstrateConnection, substrateCall{path: "/api/substrate/connection", user: user}, &view)
	if view.Access.Code != substrateapi.CodeInvalidSettings || view.Managed.Atespaces != nil {
		t.Fatalf("invalid: %s", rec.Body)
	}
	if after, _ := mints.count(); after != before {
		t.Fatal("invalid settings reached caller client")
	}
}

func TestSubstrateConnectionRevisionReconfirmAndConsent(t *testing.T) {
	s := substrateTestServer(t, substrateapi.Kube{})
	initial := fakeConnection(t, fullToken)
	var first substrateConnectionView
	if rec := putConnection(t, s, initial, &first); rec.Code != 200 {
		t.Fatalf("initial: %d %s", rec.Code, rec.Body)
	}
	send := func(view substrateConnectionView, settings map[string]any, action string, confirm bool) *httptest.ResponseRecorder {
		data, _ := json.Marshal(map[string]any{"target": view.Target, "revision": view.Revision, "settings": settings, "action": action, "confirmDestination": confirm})
		return call(t, s, s.handlePutSubstrateConnection, substrateCall{method: http.MethodPut, path: "/api/substrate/connection", body: string(data)}, nil)
	}
	// An unrelated process save invalidates the reviewed revision before a probe.
	saved, _, _ := s.substrate.Store.Get(first.Target.Binding)
	saved.Context = "changed by Desktop"
	if _, err := s.substrate.Store.Update(context.Background(), first.Revision, first.Target.Binding, &saved); err != nil {
		t.Fatal(err)
	}
	if rec := send(first, initial, "save", false); rec.Code != 409 || errorCode(t, rec) != substrateapi.CodeConflict {
		t.Fatalf("stale: %d %s", rec.Code, rec.Body)
	}
	call(t, s, s.handleGetSubstrateConnection, substrateCall{path: "/api/substrate/connection"}, &first)
	// A different authenticated destination must see no credential before consent.
	f := defaultFake()
	endpoint, ca := startFakeSubstrate(t, f)
	candidate := map[string]any{"identity": "tokenFile", "tokenFile": initial["tokenFile"], "endpoint": endpoint, "caFile": ca}
	if rec := send(first, candidate, "save", false); rec.Code != 409 || errorCode(t, rec) != substrateapi.CodeConfirmDestination {
		t.Fatalf("consent: %d %s", rec.Code, rec.Body)
	}
	if f.atespaceCalls.Load() != 0 {
		t.Fatal("token-bearing RPC sent before destination consent")
	}
	if rec := send(first, candidate, "save", true); rec.Code != 200 {
		t.Fatalf("consented: %d %s", rec.Code, rec.Body)
	}
	if f.atespaceCalls.Load() == 0 {
		t.Fatal("consented destination not probed")
	}
	useTestCluster(t, "https://changed-cluster")
	var changed substrateConnectionView
	call(t, s, s.handleGetSubstrateConnection, substrateCall{path: "/api/substrate/connection"}, &changed)
	if !changed.TargetChanged || changed.Access.Code != substrateapi.CodeTargetChanged {
		t.Fatalf("changed: %+v", changed)
	}
	if rec := send(changed, candidate, "save", false); rec.Code != 409 || errorCode(t, rec) != substrateapi.CodeTargetChanged {
		t.Fatalf("must reconfirm: %d %s", rec.Code, rec.Body)
	}
	if rec := send(changed, candidate, "reconfirm", false); rec.Code != 200 {
		t.Fatalf("reconfirm: %d %s", rec.Code, rec.Body)
	}
	got, _, _ := s.substrate.Store.Get(changed.Target.Binding)
	if got.Target != changed.Target.Fingerprint || got.Identity != changed.Target.Identity {
		t.Fatalf("target identity not saved: %+v", got)
	}
}

func TestSubstrateCallerUsesOSSImpersonationAndUserToken(t *testing.T) {
	for _, mode := range []string{"proxy", "oidc", "user-token"} {
		t.Run(mode, func(t *testing.T) {
			s := substrateTestServer(t, substrateapi.Kube{})
			s.authConfig.Mode = "proxy"
			calls := 0
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				w.Header().Set("Content-Type", "application/json")
				if r.Header.Get("Impersonate-User") != "alice" || r.Header.Get("Authorization") != "Bearer radar-sa" {
					t.Errorf("wrong caller headers: %v", r.Header)
				}
				if r.URL.Path != "/api/v1/namespaces/ate-system/serviceaccounts/ate-client/token" {
					t.Errorf("wrong mint: %s", r.URL)
				}
				json.NewEncoder(w).Encode(&authv1.TokenRequest{TypeMeta: metav1.TypeMeta{APIVersion: "authentication.k8s.io/v1", Kind: "TokenRequest"}, Status: authv1.TokenRequestStatus{Token: fullToken, ExpirationTimestamp: metav1.NewTime(time.Now().Add(time.Hour))}})
			}))
			defer api.Close()
			old := k8s.SetTestConfig(&rest.Config{Host: api.URL, BearerToken: "radar-sa"})
			defer k8s.SetTestConfig(old)
			if mode == "user-token" {
				previous := pkgauth.CurrentCredentialMode()
				pkgauth.SetCredentialMode(pkgauth.CredentialsUserToken)
				t.Cleanup(func() { pkgauth.SetCredentialMode(previous) })
			} else {
				s.authConfig.Mode = mode
				previous := pkgauth.CurrentCredentialMode()
				pkgauth.SetCredentialMode(pkgauth.CredentialsImpersonate)
				t.Cleanup(func() { pkgauth.SetCredentialMode(previous) })
			}
			r := httptest.NewRequest("GET", "/api/substrate/status", nil)
			r = r.WithContext(auth.ContextWithUser(r.Context(), &auth.User{Username: "alice", Groups: []string{"team"}}))
			gated, denied := s.substrateGate(r)
			if denied != nil {
				t.Fatalf("gate: %+v", denied)
			}
			// Minting alone must use this user's client, without needing a Substrate dial.
			client := s.getClientForRequest(gated)
			_, err := client.CoreV1().ServiceAccounts("ate-system").CreateToken(gated.Context(), "ate-client", &authv1.TokenRequest{}, metav1.CreateOptions{})
			if mode == "user-token" {
				if err == nil || calls != 0 {
					t.Fatalf("missing user token reached SA: calls=%d err=%v", calls, err)
				}
			} else if err != nil || calls != 1 {
				t.Fatalf("impersonated mint: calls=%d err=%v", calls, err)
			}
		})
	}
}

func TestSubstrateOversizedConnectionRequest(t *testing.T) {
	s := substrateTestServer(t, substrateapi.Kube{})
	rec := call(t, s, s.handlePutSubstrateConnection, substrateCall{method: http.MethodPut, path: "/api/substrate/connection", body: `{"settings":"` + strings.Repeat("x", 17*1024) + `"}`}, nil)
	if rec.Code != 413 {
		t.Fatalf("oversized: %d %s", rec.Code, rec.Body)
	}
}

func TestSubstrateEveryCredentialDestinationChangeNeedsConsent(t *testing.T) {
	for _, identity := range []string{substrateapi.IdentityTokenFile, substrateapi.IdentityShared} {
		for _, field := range []string{"endpoint", "caFile", "serverName"} {
			t.Run(identity+"/"+field, func(t *testing.T) {
				s := substrateTestServer(t, substrateapi.Kube{})
				original := fakeConnection(t, fullToken)
				old := substrateapi.Settings{Identity: identity, Endpoint: original["endpoint"].(string), CAFile: original["caFile"].(string)}
				if identity == substrateapi.IdentityTokenFile {
					old.TokenFile = original["tokenFile"].(string)
				}
				saveTestSettings(t, s, &old)
				target, err := k8s.CurrentProfileTarget()
				if err != nil {
					t.Fatal(err)
				}
				_, _, revision, err := s.substrate.Store.GetRevision(target.Binding)
				if err != nil {
					t.Fatal(err)
				}
				next := old
				switch field {
				case "endpoint":
					next.Endpoint = "different.example:443"
				case "caFile":
					next.CAFile = "/different/ca.pem"
				case "serverName":
					next.ServerName = "different.example"
				}
				s.substrate.Kube = func() (substrateapi.Kube, error) {
					t.Fatal("unconfirmed destination reached probe/mint/dial")
					return substrateapi.Kube{}, nil
				}
				body, _ := json.Marshal(map[string]any{"target": target, "revision": revision, "settings": next})
				rec := call(t, s, s.handlePutSubstrateConnection, substrateCall{method: http.MethodPut, path: "/api/substrate/connection", body: string(body)}, nil)
				if rec.Code != 409 || errorCode(t, rec) != substrateapi.CodeConfirmDestination || !strings.Contains(rec.Body.String(), "→") {
					t.Fatalf("destination change: %d %s", rec.Code, rec.Body)
				}
			})
		}
	}
}

func TestSubstrateManagedRuntimeCAFailureNeverDisclosesPath(t *testing.T) {
	s, _ := cloudSubstrate(t)
	c := s.substrateInstallation()
	c.Settings = s.substrate.CallerDial
	s.substrateInstallationConfig = c
	if err := os.WriteFile(c.Settings.CAFile, []byte("rotated file without a certificate"), 0600); err != nil {
		t.Fatal(err)
	}
	user := hubUser("reader", "member")
	rec := call(t, s, s.handleSubstrateActors, substrateCall{path: "/api/substrate/actors", user: user}, nil)
	if rec.Code != http.StatusServiceUnavailable || errorCode(t, rec) != substrateapi.CodeTLS {
		t.Fatalf("CA failure: %d %s", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), c.Settings.CAFile) {
		t.Fatal("API error disclosed operator CA path")
	}
	var view substrateConnectionView
	rec = call(t, s, s.handleGetSubstrateConnection, substrateCall{path: "/api/substrate/connection", user: user}, &view)
	if view.Access.Code != substrateapi.CodeTLS || view.Managed.Trust != "caFile" || view.Managed.Atespaces != nil || strings.Contains(rec.Body.String(), c.Settings.CAFile) {
		t.Fatalf("managed CA failure: %s", rec.Body)
	}
}

func TestDestinationChangesNamesOnlyWhatMoved(t *testing.T) {
	from := substrateapi.Settings{Endpoint: "127.0.0.1:18443", CAFile: "/ca.pem", ServerName: "api.ate-system.svc"}
	to := from
	to.ServerName = "api.example.test"
	if got := destinationChanges(from, to); got != "server name api.ate-system.svc → api.example.test" {
		t.Errorf("one change: %q", got)
	}
	to = substrateapi.Settings{Endpoint: "api.example.test:443"}
	got := destinationChanges(substrateapi.Settings{}, to)
	if got != "endpoint port-forward to ate-system/api → api.example.test:443" {
		t.Errorf("an empty value names its default: %q", got)
	}
}
