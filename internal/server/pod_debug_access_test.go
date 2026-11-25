package server

import (
	"context"
	"errors"
	"reflect"
	"testing"

	authv1 "k8s.io/api/authorization/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stest "k8s.io/client-go/testing"
)

func TestNamedPodDebugAccess(t *testing.T) {
	for _, tc := range []struct {
		name         string
		exec, update authv1.SubjectAccessReviewStatus
		updateErr    error
		want         string
		checks       int
	}{
		{"allowed", authv1.SubjectAccessReviewStatus{Allowed: true}, authv1.SubjectAccessReviewStatus{Allowed: true}, nil, "allowed", 2},
		{"denied", authv1.SubjectAccessReviewStatus{Allowed: true}, authv1.SubjectAccessReviewStatus{}, nil, "denied", 2},
		{"partial", authv1.SubjectAccessReviewStatus{Allowed: true}, authv1.SubjectAccessReviewStatus{EvaluationError: "timeout"}, nil, "unknown", 2},
		{"explicit deny", authv1.SubjectAccessReviewStatus{Allowed: true}, authv1.SubjectAccessReviewStatus{Denied: true, EvaluationError: "timeout"}, nil, "denied", 2},
		{"review failed", authv1.SubjectAccessReviewStatus{Allowed: true}, authv1.SubjectAccessReviewStatus{}, errors.New("offline"), "unknown", 2},
		{"exec denied", authv1.SubjectAccessReviewStatus{}, authv1.SubjectAccessReviewStatus{}, nil, "unavailable", 1},
		{"exec unresolved", authv1.SubjectAccessReviewStatus{EvaluationError: "timeout"}, authv1.SubjectAccessReviewStatus{}, nil, "unknown", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := fake.NewSimpleClientset()
			var reviews []authv1.ResourceAttributes
			client.PrependReactor("create", "selfsubjectaccessreviews", func(action k8stest.Action) (bool, runtime.Object, error) {
				review := action.(k8stest.CreateAction).GetObject().(*authv1.SelfSubjectAccessReview)
				attrs := *review.Spec.ResourceAttributes
				reviews = append(reviews, attrs)
				review.Status = tc.exec
				if attrs.Subresource == "ephemeralcontainers" {
					review.Status = tc.update
					return true, review, tc.updateErr
				}
				return true, review, nil
			})
			if got := reviewPodDebugAccess(context.Background(), client, "payments", "checkout"); got != tc.want {
				t.Fatalf("state=%s, want %s", got, tc.want)
			}
			want := []authv1.ResourceAttributes{{Namespace: "payments", Name: "checkout", Verb: "create", Resource: "pods", Subresource: "exec"}, {Namespace: "payments", Name: "checkout", Verb: "update", Resource: "pods", Subresource: "ephemeralcontainers"}}
			if !reflect.DeepEqual(reviews, want[:tc.checks]) {
				t.Fatalf("requests=%#v", reviews)
			}
		})
	}
}
