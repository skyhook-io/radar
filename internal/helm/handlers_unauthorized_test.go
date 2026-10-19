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
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// The release list tells a rejected credential apart from an RBAC denial:
// granting Secret access can't fix a token the cluster won't accept. Neither
// may come back as 401, which the UI reads as an expired Radar session.
func TestHandleListReleasesSeparatesRejectedCredentialsFromDenial(t *testing.T) {
	for name, tc := range map[string]struct {
		upstream   int
		wantStatus int
		wantError  string
	}{
		"expired credentials": {http.StatusUnauthorized, http.StatusBadGateway, UnauthorizedListMessage},
		"rbac denial":         {http.StatusForbidden, http.StatusForbidden, "insufficient permissions to list Helm releases"},
	} {
		t.Run(name, func(t *testing.T) {
			storage := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.upstream)
				reason := map[int]string{http.StatusUnauthorized: "Unauthorized", http.StatusForbidden: "Forbidden"}[tc.upstream]
				msg := "Unauthorized"
				if tc.upstream == http.StatusForbidden {
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
				Error string `json:"error"`
			}
			if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(body.Error, tc.wantError) {
				t.Fatalf("error = %q, want %q", body.Error, tc.wantError)
			}
		})
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
