package helm

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/skyhook-io/radar/internal/k8s"
	pkgauth "github.com/skyhook-io/radar/pkg/auth"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// The release list tells a rejected credential apart from an RBAC denial:
// granting Secret access can't fix a token the cluster won't accept. Neither
// may come back as 401, which the UI reads as an expired Radar session.
// userTokenRequired is the refusal of a live call made without a user token
// (internal/k8s/user_config.go). It never reached the caller's RBAC.
const userTokenRequired = "Live actions need a user token, which only single sign-on sessions carry; this request has none."

func TestHandleListReleasesSeparatesRejectedCredentialsFromDenial(t *testing.T) {
	for name, tc := range map[string]struct {
		upstream   int
		wantStatus int
		wantError  string
		userToken  bool
	}{
		"expired credentials":   {http.StatusUnauthorized, http.StatusBadGateway, UnauthorizedListMessage(), false},
		"rbac denial":           {http.StatusForbidden, http.StatusForbidden, ReleaseListDeniedMessage, false},
		"impersonation refused": {http.StatusForbidden, http.StatusInternalServerError, ImpersonationDeniedListMessage, false},
		"user token required":   {http.StatusForbidden, http.StatusForbidden, userTokenRequired, false},
		"user token denied":     {http.StatusForbidden, http.StatusForbidden, ReleaseListDeniedMessage, true},
	} {
		t.Run(name, func(t *testing.T) {
			if tc.userToken {
				pkgauth.SetCredentialMode(pkgauth.CredentialsUserToken)
				t.Cleanup(func() { pkgauth.SetCredentialMode(pkgauth.CredentialsImpersonate) })
			}
			storage := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.upstream)
				reason := map[int]string{http.StatusUnauthorized: "Unauthorized", http.StatusForbidden: "Forbidden"}[tc.upstream]
				msg := "Unauthorized"
				switch {
				case tc.wantError == userTokenRequired:
					msg = userTokenRequired
				case tc.wantError == ImpersonationDeniedListMessage:
					msg = `users "dana" is forbidden: User "system:serviceaccount:radar:radar" cannot impersonate resource "users" in API group "" at the cluster scope`
				case tc.upstream == http.StatusForbidden:
					msg = `secrets is forbidden: User "someone" cannot list resource "secrets" in API group "" in the namespace "shop"`
				}
				_, _ = io.WriteString(w, `{"apiVersion":"v1","kind":"Status","status":"Failure","message":`+strconv.Quote(msg)+`,"reason":"`+reason+`","code":`+strconv.Itoa(tc.upstream)+`}`)
			}))
			t.Cleanup(storage.Close)
			config := &rest.Config{Host: storage.URL}
			client, err := kubernetes.NewForConfig(config)
			if err != nil {
				t.Fatal(err)
			}
			previous := k8s.SetTestClient(client)
			ResetClient()
			if err := InitializeWithRESTConfig(config); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				ResetClient()
				k8s.SetTestClient(previous)
			})

			h := NewHandlers(func(*http.Request) ([]string, bool) { return []string{"shop"}, true })
			recorder := httptest.NewRecorder()
			h.handleListReleases(recorder, httptest.NewRequest(http.MethodGet, "/api/helm/releases?namespace=shop", nil))

			if recorder.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d (body %s)", recorder.Code, tc.wantStatus, recorder.Body.String())
			}
			var body struct {
				Error       string `json:"error"`
				ErrorCode   string `json:"error_code"`
				Reason      string `json:"reason"`
				Credentials string `json:"kubernetes_credentials"`
			}
			if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(body.Error, tc.wantError) {
				t.Fatalf("error = %q, want %q", body.Error, tc.wantError)
			}
			if denied := body.ErrorCode == ReleaseListDeniedCode; denied != (tc.wantError == ReleaseListDeniedMessage) {
				t.Fatalf("error_code = %q for %q", body.ErrorCode, name)
			}
			wantCredentials := map[bool]string{false: "impersonate", true: "user-token"}[tc.userToken]
			if body.ErrorCode == ReleaseListDeniedCode && body.Credentials != wantCredentials {
				t.Fatalf("kubernetes_credentials = %q, want %q", body.Credentials, wantCredentials)
			}
			if tc.wantError == userTokenRequired && body.Reason != pkgauth.UserTokenRequiredReason {
				t.Fatalf("reason = %q, want %q", body.Reason, pkgauth.UserTokenRequiredReason)
			}
		})
	}
}

func TestUnauthorizedListMessageNamesTheRejectedCredential(t *testing.T) {
	k8s.ForceInCluster = true
	t.Cleanup(func() { k8s.ForceInCluster = false })
	if msg := UnauthorizedListMessage(); !strings.Contains(msg, "restart its pod") {
		t.Fatalf("in-cluster: %q", msg)
	}
	pkgauth.SetCredentialMode(pkgauth.CredentialsUserToken)
	t.Cleanup(func() { pkgauth.SetCredentialMode(pkgauth.CredentialsImpersonate) })
	if msg := UnauthorizedListMessage(); !strings.Contains(msg, "sign in to Radar again") || strings.Contains(msg, "kubeconfig") {
		t.Fatalf("user-token mode: %q", msg)
	}
}

func TestIsUnauthorizedErrorTrustsTheStatusCode(t *testing.T) {
	forbidden := apierrors.NewForbidden(schema.GroupResource{Resource: "secrets"}, "", errors.New(`cannot list resource "secrets" in the namespace "unauthorized-sandbox"`))
	if IsUnauthorizedError(forbidden) {
		t.Fatal("a 403 that mentions 'unauthorized' was read as rejected credentials")
	}
	if !IsForbiddenError(forbidden) {
		t.Fatal("the 403 should still read as forbidden")
	}
	if !IsUnauthorizedError(apierrors.NewUnauthorized("Unauthorized")) {
		t.Fatal("a typed 401 should read as rejected credentials")
	}
	if !IsUnauthorizedError(fmt.Errorf("query: failed to query with labels: %w", errors.New("Unauthorized"))) {
		t.Fatal("an untyped 'Unauthorized' should still read as rejected credentials")
	}
}

func TestIsImpersonationDeniedIsNotASecretsDenial(t *testing.T) {
	impersonation := apierrors.NewForbidden(schema.GroupResource{Resource: "users"}, "dana", errors.New(`User "system:serviceaccount:radar:radar" cannot impersonate resource "users" in API group "" at the cluster scope`))
	if !IsImpersonationDenied(impersonation) {
		t.Fatal("an impersonation denial should be recognised")
	}
	secrets := apierrors.NewForbidden(schema.GroupResource{Resource: "secrets"}, "", errors.New(`User "dana" cannot list resource "secrets" in API group "" in the namespace "shop"`))
	if IsImpersonationDenied(secrets) {
		t.Fatal("a Secrets denial was read as an impersonation denial")
	}
}
