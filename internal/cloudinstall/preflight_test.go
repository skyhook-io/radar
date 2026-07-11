package cloudinstall

import (
	"context"
	"errors"
	"testing"

	authv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

// fakeWithSSAR returns a fake clientset whose SelfSubjectAccessReview creates are
// answered by allow(); everything else denied.
func fakeWithSSAR(allow func(authv1.ResourceAttributes) bool) *fake.Clientset {
	cs := fake.NewSimpleClientset()
	cs.PrependReactor("create", "selfsubjectaccessreviews", func(action k8stesting.Action) (bool, runtime.Object, error) {
		ssar := action.(k8stesting.CreateAction).GetObject().(*authv1.SelfSubjectAccessReview)
		out := ssar.DeepCopy()
		out.Status.Allowed = allow(*ssar.Spec.ResourceAttributes)
		return true, out, nil
	})
	return cs
}

func TestInstallPreflight_ClusterAdmin(t *testing.T) {
	cs := fakeWithSSAR(func(authv1.ResourceAttributes) bool { return true })
	res, err := InstallPreflight(context.Background(), cs, "radar")
	if err != nil {
		t.Fatal(err)
	}
	if !res.OK() || len(res.Blocking) != 0 || len(res.Advisory) != 0 {
		t.Fatalf("cluster-admin should pass clean, got %+v", res)
	}
}

func TestInstallPreflight_PlainUser(t *testing.T) {
	// A namespace-scoped editor: can create namespaced objects, but no
	// cluster-scoped RBAC and no escalate/bind.
	cs := fakeWithSSAR(func(a authv1.ResourceAttributes) bool {
		return a.Namespace != "" && a.Verb == "create"
	})
	res, err := InstallPreflight(context.Background(), cs, "radar")
	if err != nil {
		t.Fatal(err)
	}
	if res.OK() {
		t.Fatal("plain user must be blocked (can't create cluster RBAC)")
	}
	// ClusterRole + ClusterRoleBinding create are the blocking denials.
	if !containsSubstr(res.Blocking, "create ClusterRoles") || !containsSubstr(res.Blocking, "create ClusterRoleBindings") {
		t.Errorf("expected cluster-RBAC create in Blocking, got %v", res.Blocking)
	}
	// escalate/bind land in Advisory, never Blocking.
	if len(res.Advisory) == 0 {
		t.Errorf("expected escalate/bind in Advisory, got none")
	}
	for _, b := range res.Blocking {
		if b == "escalate ClusterRoles (for the impersonation role)" {
			t.Errorf("escalate must be advisory, not blocking")
		}
	}
}

func TestInstallPreflight_CanCreateButCannotEscalate(t *testing.T) {
	// Edge case: caller can create every object (incl. cluster RBAC) but lacks
	// escalate/bind. Not blocked (the install attempt is the authoritative gate);
	// the missing escalate/bind surface as advisory.
	cs := fakeWithSSAR(func(a authv1.ResourceAttributes) bool {
		return a.Verb == "create" || a.Verb == "update"
	})
	res, err := InstallPreflight(context.Background(), cs, "radar")
	if err != nil {
		t.Fatal(err)
	}
	if !res.OK() {
		t.Fatalf("create-capable caller must not be blocked, got Blocking=%v", res.Blocking)
	}
	if len(res.Advisory) != 7 { // 2 escalate + generated ClusterRole/Role + 3 tier binds
		t.Errorf("expected 7 advisory escalation/bind checks, got %v", res.Advisory)
	}
}

func TestInstallPreflight_SecretUpdateIsBlocking(t *testing.T) {
	cs := fakeWithSSAR(func(a authv1.ResourceAttributes) bool {
		return a.Verb == "create" || (a.Verb == "update" && a.Resource != "secrets")
	})
	res, err := InstallPreflight(context.Background(), cs, "radar")
	if err != nil {
		t.Fatal(err)
	}
	if res.OK() || !containsSubstr(res.Blocking, "update Secrets (for Helm release metadata)") {
		t.Fatalf("update Secrets must block before token mint, got %+v", res)
	}
}

func TestInstallPreflight_NamespaceCreateSuppressedWhenExists(t *testing.T) {
	// Caller can create namespaced objects + cluster RBAC but NOT namespaces.
	allow := func(a authv1.ResourceAttributes) bool {
		return !(a.Resource == "namespaces")
	}
	// No namespace object → nsExists=false → namespace-create is required → blocked.
	missing := fakeWithSSAR(allow)
	res, err := InstallPreflight(context.Background(), missing, "radar")
	if err != nil {
		t.Fatal(err)
	}
	if !containsSubstr(res.Blocking, "create the target Namespace") {
		t.Errorf("missing namespace should require create, blocking=%v", res.Blocking)
	}

	// Namespace already present → nsExists=true → the check is skipped → clean.
	present := fakeWithSSAR(allow)
	present.CoreV1().Namespaces().Create(context.Background(),
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "radar"}}, metav1.CreateOptions{})
	res, err = InstallPreflight(context.Background(), present, "radar")
	if err != nil {
		t.Fatal(err)
	}
	if !res.OK() {
		t.Errorf("existing namespace should skip the create check, blocking=%v", res.Blocking)
	}
}

func TestInstallPreflight_NilClient(t *testing.T) {
	if _, err := InstallPreflight(context.Background(), nil, "radar"); err == nil {
		t.Fatal("expected error on nil client")
	}
}

func TestInstallPreflight_NamespaceGetErrorIsFatal(t *testing.T) {
	cs := fakeWithSSAR(func(authv1.ResourceAttributes) bool { return true })
	want := errors.New("temporary apiserver failure")
	cs.PrependReactor("get", "namespaces", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, want
	})

	_, err := InstallPreflight(context.Background(), cs, "radar")
	if !errors.Is(err, want) {
		t.Fatalf("expected namespace error to propagate, got %v", err)
	}
}

func TestInstallPreflight_NamespaceForbiddenIsFatal(t *testing.T) {
	cs := fakeWithSSAR(func(authv1.ResourceAttributes) bool { return true })
	cs.PrependReactor("get", "namespaces", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: "namespaces"}, "radar", errors.New("denied"))
	})

	if _, err := InstallPreflight(context.Background(), cs, "radar"); err == nil {
		t.Fatal("expected forbidden namespace inspection to fail preflight")
	}
}

func containsSubstr(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
