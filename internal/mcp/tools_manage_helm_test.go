package mcp

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"github.com/skyhook-io/radar/internal/k8s"
	authv1 "k8s.io/api/authorization/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/skyhook-io/radar/internal/auth"
)

func TestManageHelmReleaseValidationBeforeClusterAccess(t *testing.T) {
	no := false
	for _, input := range []manageHelmReleaseInput{
		{Action: "uninstall", Name: "demo"},
		{Action: "delete", Namespace: "demo", Name: "demo"},
		{Action: "rollback", Namespace: "demo", Name: "demo"},
		{Action: "rollback", Namespace: "demo", Name: "demo", Revision: -1},
		{Action: "rollback", Namespace: "demo", Name: "demo", Revision: 1, KeepHistory: true},
		{Action: "uninstall", Namespace: "demo", Name: "demo", Revision: 1},
		{Action: "uninstall", Namespace: "demo", Name: "demo", DryRun: &no},
	} {
		if _, _, err := handleManageHelmRelease(context.Background(), nil, input); err == nil || strings.Contains(err.Error(), "not initialized") {
			t.Fatalf("invalid input reached cluster: %+v %v", input, err)
		}
	}
}

func TestManageHelmConfirmationIsolation(t *testing.T) {
	ctx := auth.ContextWithUser(context.Background(), &auth.User{Username: "alice"})
	target, err := helmConfirmationTarget("storage", "demo", "snapshot-with-options")
	if err != nil {
		t.Fatal(err)
	}
	token, err := issueMutationConfirmationTarget(ctx, "manage_helm_release", target)
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyMutationConfirmationTarget(ctx, "manage_helm_release", target, token); err != nil {
		t.Fatal(err)
	}
	for _, other := range []string{"another-snapshot", "different-options"} {
		changed, _ := helmConfirmationTarget("storage", "demo", other)
		if err := verifyMutationConfirmationTarget(ctx, "manage_helm_release", changed, token); !errors.Is(err, ErrConfirmationTargetMismatch) {
			t.Fatal("changed snapshot accepted")
		}
	}
	if _, err := verifyMutationConfirmation(ctx, "delete_resource", token); err == nil {
		t.Fatal("Helm confirmation authorized delete_resource")
	}
}

func TestHelmConfirmationDigestAndConsumption(t *testing.T) {
	target, _ := helmConfirmationTarget("storage", "demo", "private-release-fingerprint")
	token, err := issueMutationConfirmationTarget(context.Background(), "manage_helm_release", target)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := base64.RawURLEncoding.DecodeString(strings.Split(token, ".")[0])
	if strings.Contains(string(raw), "private-release-fingerprint") || strings.Contains(string(raw), "storage") {
		t.Fatalf("raw target disclosed: %s", raw)
	}
	for i := 0; i < 2; i++ {
		if err := verifyMutationConfirmationTarget(context.Background(), "manage_helm_release", target, token); err != nil {
			t.Fatal(err)
		}
	}
	fresh, err := issueMutationConfirmationTarget(context.Background(), "manage_helm_release", target)
	if err != nil || fresh == token {
		t.Fatalf("fresh preview reused token: %v", err)
	}
	if err := consumeHelmConfirmation(token); err != nil {
		t.Fatal(err)
	}
	if err := consumeHelmConfirmation(token); err == nil || !strings.Contains(err.Error(), "already used") {
		t.Fatalf("confirmation reused: %v", err)
	}
}

func TestManageHelmWriteGateBeforePreviewAndExecution(t *testing.T) {
	var checks int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/subjectaccessreviews") {
			t.Errorf("unexpected cluster access: %s", r.URL.Path)
			http.Error(w, "unexpected", 500)
			return
		}
		var review authv1.SubjectAccessReview
		json.NewDecoder(r.Body).Decode(&review)
		a := review.Spec.ResourceAttributes
		if a != nil && a.Resource == "secrets" && a.Verb == "create" {
			checks++
			if a.Namespace != "storage" || review.Spec.User != "helm-no-storage-write" {
				t.Errorf("wrong write gate: %+v", review.Spec)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(authv1.SubjectAccessReview{TypeMeta: metav1.TypeMeta{APIVersion: "authorization.k8s.io/v1", Kind: "SubjectAccessReview"}, Status: authv1.SubjectAccessReviewStatus{Allowed: a != nil && a.Resource != "secrets"}})
	}))
	defer srv.Close()
	previous := k8s.SetTestClient(kubernetes.NewForConfigOrDie(&rest.Config{Host: srv.URL, ContentConfig: rest.ContentConfig{ContentType: "application/json"}}))
	defer k8s.SetTestClient(previous)
	ctx := auth.ContextWithUser(context.Background(), &auth.User{Username: "helm-no-storage-write"})
	var output bytes.Buffer
	previousLog := log.Writer()
	log.SetOutput(&output)
	defer log.SetOutput(previousLog)
	no := false
	input := manageHelmReleaseInput{Action: "uninstall", Namespace: "storage", Name: "demo", NoHooks: true, KeepHistory: true}
	for _, execute := range []bool{false, true} {
		if execute {
			input.DryRun = &no
			input.Confirm = "unverified-token"
		}
		result, _, err := handleManageHelmRelease(ctx, nil, input)
		if result != nil || err == nil || !strings.Contains(err.Error(), "RBAC doesn't allow creating Secrets") {
			t.Fatalf("write denial bypassed: %v %+v", err, result)
		}
	}
	if checks == 0 {
		t.Fatal("storage write capability never checked")
	}
	for _, want := range []string{`outcome="preview_failed"`, `outcome="failed"`, "revision=0 no_hooks=true keep_history=true"} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("audit missing %s: %s", want, output.String())
		}
	}
}
