package helm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/skyhook-io/radar/internal/k8s"
	pkgauth "github.com/skyhook-io/radar/pkg/auth"
	"github.com/skyhook-io/radar/pkg/issuesapi"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// userTokenRequiredError is the error a live call returns for a person
// without a user token: Radar refuses it before reaching the API server.
func userTokenRequiredError(t *testing.T) error {
	t.Helper()
	prev := pkgauth.CurrentCredentialMode()
	pkgauth.SetCredentialMode(pkgauth.CredentialsUserToken)
	t.Cleanup(func() { pkgauth.SetCredentialMode(prev) })
	cfg := k8s.UserRESTConfig(&rest.Config{Host: "http://127.0.0.1:1"}, &pkgauth.User{Username: "oidc:alice"})
	client, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.CoreV1().Secrets("default").List(context.Background(), metav1.ListOptions{})
	if err == nil {
		t.Fatal("live call without a user token succeeded")
	}
	return err
}

func TestUserTokenRequiredIsNotTheCallersDenial(t *testing.T) {
	err := userTokenRequiredError(t)
	if ReleaseReadDeniedForCaller(err) {
		t.Fatal("missing user token read as the caller's own RBAC denial")
	}
	got, unchecked := IssueReadUnchecked(context.Background(), err)
	if !unchecked || got.Source != issuesapi.UncheckedSourceHelm {
		t.Fatalf("unchecked = %v %+v, want Helm reported unchecked", unchecked, got)
	}
}

func TestWriteErrorReportsUserTokenRequired(t *testing.T) {
	err := userTokenRequiredError(t)
	w := httptest.NewRecorder()
	writeError(w, http.StatusInternalServerError, "failed to list releases: "+err.Error())
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", w.Code)
	}
	var body map[string]string
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body["reason"] != pkgauth.UserTokenRequiredReason {
		t.Fatalf("body = %v, want reason %s", body, pkgauth.UserTokenRequiredReason)
	}
}
