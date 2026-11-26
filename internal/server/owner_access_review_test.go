package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"

	authv1 "k8s.io/api/authorization/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	clienttesting "k8s.io/client-go/testing"
	"sigs.k8s.io/yaml"

	"github.com/skyhook-io/radar/internal/auth"
	pkgauth "github.com/skyhook-io/radar/pkg/auth"
)

func TestOwnerAccessReviewMatchesChart(t *testing.T) {
	raw, err := os.ReadFile("../../deploy/helm/radar/files/integration-write-baseline.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var policy struct {
		Entries []struct {
			Capability string
			Group      string
			Resources  []string
			Verbs      []string
		}
	}
	if err := yaml.Unmarshal(raw, &policy); err != nil {
		t.Fatal(err)
	}
	expected, checked := []string{}, []string{}
	for _, entry := range policy.Entries {
		for _, resource := range entry.Resources {
			for _, verb := range entry.Verbs {
				expected = append(expected, strings.Join([]string{entry.Capability, entry.Group, resource, verb}, ":"))
			}
		}
	}
	for id, grant := range ownerAccessReviewPermissions {
		for _, permission := range grant.permissions {
			resource := permission.resource
			if permission.subresource != "" {
				resource += "/" + permission.subresource
			}
			for _, verb := range permission.verbs {
				checked = append(checked, strings.Join([]string{id, grant.group, resource, verb}, ":"))
			}
		}
	}
	slices.Sort(expected)
	slices.Sort(checked)
	if !slices.Equal(checked, expected) {
		t.Fatalf("Owner grant review checks %v, chart grants %v", checked, expected)
	}
}

func TestOwnerAccessReviewReportsEffectInEachScope(t *testing.T) {
	const argoGroup = pkgauth.OwnerAccessGroupPrefix + "argocd"
	client := fake.NewSimpleClientset()
	client.PrependReactor("create", "selfsubjectaccessreviews", func(action clienttesting.Action) (bool, runtime.Object, error) {
		review := action.(clienttesting.CreateAction).GetObject().(*authv1.SelfSubjectAccessReview)
		attrs := review.Spec.ResourceAttributes
		review.Status.Allowed = attrs.Verb == "impersonate" && attrs.Resource == "groups" && attrs.Name == argoGroup
		return true, review, nil
	})
	client.PrependReactor("create", "subjectaccessreviews", func(action clienttesting.Action) (bool, runtime.Object, error) {
		review := action.(clienttesting.CreateAction).GetObject().(*authv1.SubjectAccessReview)
		attrs := review.Spec.ResourceAttributes
		if review.Spec.User != "owner" || attrs.Group != "argoproj.io" || attrs.Resource != "applications" ||
			!slices.Contains(review.Spec.Groups, "system:authenticated") || !slices.Contains(review.Spec.Groups, "radar:owner") {
			return true, nil, errors.New("unexpected identity or resource")
		}
		switch attrs.Namespace {
		case "custom-role":
			review.Status.Allowed = true
		case "works":
			review.Status.Allowed = slices.Contains(review.Spec.Groups, argoGroup)
		case "missing":
			review.Status.Allowed = attrs.Verb == "get"
		case "unknown":
			review.Status.EvaluationError = "authorizer unavailable"
		}
		return true, review, nil
	})

	owner := &auth.User{Username: "owner", Groups: []string{"radar:owner", argoGroup}}
	result := reviewOwnerAccess(context.Background(), client, owner, []string{"argocd", "cnpg", "velero", "unrecognized"}, []string{"custom-role", "missing", "unknown", "works"}, &metav1.APIGroupList{
		Groups: []metav1.APIGroup{{Name: "argoproj.io"}, {Name: "velero.io"}},
	})
	if !result.NamespaceScoped || result.ScopeCount != 4 || result.CheckedScopeCount != 4 {
		t.Fatalf("scope = %+v", result)
	}
	if got, want := result.Grants["argocd"], (ownerAccessReviewResult{Added: 1, Already: 1, Incomplete: 1, Unknown: 1}); got != want {
		t.Errorf("Argo grant = %+v, want %+v", got, want)
	}
	if got := result.Grants["cnpg"]; !got.APINotServed {
		t.Errorf("CNPG group was not served: %+v", got)
	}
	if got := result.Grants["velero"]; !got.CannotImpersonate {
		t.Errorf("Radar cannot impersonate the Velero group: %+v", got)
	}
	if got, want := result.Grants["unrecognized"], (ownerAccessReviewResult{Unknown: 4}); got != want {
		t.Errorf("unrecognized grant = %+v, want %+v", got, want)
	}
}

func TestOwnerAccessReviewBoundsNamespaceSample(t *testing.T) {
	namespaces := []string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j"}
	client := fake.NewSimpleClientset()
	client.PrependReactor("create", "selfsubjectaccessreviews", func(action clienttesting.Action) (bool, runtime.Object, error) {
		review := action.(clienttesting.CreateAction).GetObject().(*authv1.SelfSubjectAccessReview)
		review.Status.Allowed = true
		return true, review, nil
	})
	client.PrependReactor("create", "subjectaccessreviews", func(action clienttesting.Action) (bool, runtime.Object, error) {
		review := action.(clienttesting.CreateAction).GetObject().(*authv1.SubjectAccessReview)
		review.Status.Allowed = true
		return true, review, nil
	})
	owner := &auth.User{Username: "owner", Groups: []string{"radar:owner"}}
	result := reviewOwnerAccess(context.Background(), client, owner, []string{"argocd"}, namespaces, nil)
	if result.ScopeCount != 10 || result.CheckedScopeCount != 8 || result.Grants["argocd"] != (ownerAccessReviewResult{Already: 8}) {
		t.Fatalf("expected eight verified scopes out of ten configured: %+v", result)
	}
}

func TestOwnerAccessReviewRejectsNonOwner(t *testing.T) {
	t.Setenv("RADAR_CLOUD_MODE", "true")
	t.Setenv("RADAR_CLOUD_OWNER_ACCESS", "argocd")
	r := httptest.NewRequest(http.MethodGet, "/api/cloud/owner-access/review", nil)
	r = r.WithContext(auth.ContextWithUser(r.Context(), &auth.User{Username: "member", Groups: []string{"radar:member"}}))
	w := httptest.NewRecorder()
	(&Server{}).handleOwnerAccessReview(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", w.Code)
	}
}
