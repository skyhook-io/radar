package auth

import (
	"context"
	"testing"
	"time"

	authv1 "k8s.io/api/authorization/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

// --- FilterNamespacesForUser tests (pure logic, no K8s client) ---

func TestFilterNamespacesForUser_NilUser(t *testing.T) {
	requested := []string{"default", "kube-system"}
	got := FilterNamespacesForUser(requested, nil, nil)
	if len(got) != 2 {
		t.Errorf("nil user should pass through: got %v", got)
	}
}

func TestFilterNamespacesForUser_NilPerms(t *testing.T) {
	requested := []string{"default"}
	user := &User{Username: "alice"}
	got := FilterNamespacesForUser(requested, user, nil)
	if len(got) != 1 || got[0] != "default" {
		t.Errorf("nil perms should pass through: got %v", got)
	}
}

func TestFilterNamespacesForUser_ClusterAdmin(t *testing.T) {
	requested := []string{"default", "prod"}
	user := &User{Username: "admin"}
	perms := &UserPermissions{AllowedNamespaces: nil} // nil = cluster admin

	got := FilterNamespacesForUser(requested, user, perms)
	if len(got) != 2 {
		t.Errorf("cluster admin should see all requested: got %v", got)
	}
}

func TestFilterNamespacesForUser_Intersection(t *testing.T) {
	requested := []string{"default", "prod", "staging"}
	user := &User{Username: "alice"}
	perms := &UserPermissions{AllowedNamespaces: []string{"default", "staging"}}

	got := FilterNamespacesForUser(requested, user, perms)
	if len(got) != 2 {
		t.Errorf("expected 2 namespaces, got %v", got)
	}
	allowed := map[string]bool{}
	for _, ns := range got {
		allowed[ns] = true
	}
	if !allowed["default"] || !allowed["staging"] {
		t.Errorf("expected default and staging, got %v", got)
	}
	if allowed["prod"] {
		t.Error("prod should not be in result")
	}
}

func TestFilterNamespacesForUser_NoOverlap(t *testing.T) {
	requested := []string{"prod"}
	user := &User{Username: "alice"}
	perms := &UserPermissions{AllowedNamespaces: []string{"default", "staging"}}

	got := FilterNamespacesForUser(requested, user, perms)
	if got == nil {
		t.Error("result should be empty slice (not nil) for no access")
	}
	if len(got) != 0 {
		t.Errorf("expected empty result, got %v", got)
	}
}

func TestFilterNamespacesForUser_NoRequestedReturnsAllAllowed(t *testing.T) {
	user := &User{Username: "alice"}
	perms := &UserPermissions{AllowedNamespaces: []string{"dev", "staging"}}

	got := FilterNamespacesForUser(nil, user, perms)
	if len(got) != 2 {
		t.Errorf("empty request should return all allowed: got %v", got)
	}
}

func TestFilterNamespacesForUser_EmptyAllowed(t *testing.T) {
	requested := []string{"default"}
	user := &User{Username: "alice"}
	perms := &UserPermissions{AllowedNamespaces: []string{}} // empty = no access

	got := FilterNamespacesForUser(requested, user, perms)
	if len(got) != 0 {
		t.Errorf("empty allowed should give no access: got %v", got)
	}
}

// --- PermissionCache tests ---

func TestPermissionCache_SetAndGet(t *testing.T) {
	pc := NewPermissionCache()

	perms := &UserPermissions{AllowedNamespaces: []string{"default"}}
	pc.Set("alice", perms)

	got := pc.Get("alice")
	if got == nil {
		t.Fatal("expected cached perms, got nil")
	}
	if len(got.AllowedNamespaces) != 1 || got.AllowedNamespaces[0] != "default" {
		t.Errorf("cached perms = %v, want [default]", got.AllowedNamespaces)
	}
}

func TestPermissionCache_Miss(t *testing.T) {
	pc := NewPermissionCache()

	got := pc.Get("nonexistent")
	if got != nil {
		t.Error("expected nil for cache miss")
	}
}

func TestPermissionCache_Expiry(t *testing.T) {
	pc := &PermissionCache{
		cache: make(map[string]*UserPermissions),
		ttl:   1 * time.Millisecond, // very short TTL
	}

	perms := &UserPermissions{AllowedNamespaces: []string{"default"}}
	pc.Set("alice", perms)

	time.Sleep(5 * time.Millisecond)

	got := pc.Get("alice")
	if got != nil {
		t.Error("expected nil for expired cache entry")
	}
}

func TestPermissionCache_Invalidate(t *testing.T) {
	pc := NewPermissionCache()

	pc.Set("alice", &UserPermissions{AllowedNamespaces: []string{"default"}})
	pc.Set("bob", &UserPermissions{AllowedNamespaces: []string{"staging"}})

	pc.Invalidate()

	if pc.Get("alice") != nil || pc.Get("bob") != nil {
		t.Error("Invalidate should clear all entries")
	}
}

// --- DiscoverNamespaces tests (with fake K8s client) ---

// fakeClientWithSAR creates a fake K8s client that responds to SubjectAccessReview
// based on the allowFunc. allowFunc receives (username, namespace, resource, verb)
// and returns whether the action is allowed.
func fakeClientWithSAR(allowFunc func(username, namespace, resource, verb string) bool) *fake.Clientset {
	client := fake.NewClientset()
	client.PrependReactor("create", "subjectaccessreviews", func(action k8stesting.Action) (bool, runtime.Object, error) {
		sar := action.(k8stesting.CreateAction).GetObject().(*authv1.SubjectAccessReview)
		spec := sar.Spec
		ns := ""
		resource := ""
		verb := ""
		if spec.ResourceAttributes != nil {
			ns = spec.ResourceAttributes.Namespace
			resource = spec.ResourceAttributes.Resource
			verb = spec.ResourceAttributes.Verb
		}

		allowed := allowFunc(spec.User, ns, resource, verb)

		return true, &authv1.SubjectAccessReview{
			ObjectMeta: metav1.ObjectMeta{Name: "test"},
			Status:     authv1.SubjectAccessReviewStatus{Allowed: allowed},
		}, nil
	})
	return client
}

func TestDiscoverNamespaces_ClusterAdmin(t *testing.T) {
	client := fakeClientWithSAR(func(username, namespace, resource, verb string) bool {
		return true // allow everything
	})

	ns, err := DiscoverNamespaces(context.Background(), client, "admin", nil, []string{"default", "kube-system"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ns != nil {
		t.Errorf("cluster admin should get nil (all namespaces), got %v", ns)
	}
}

func TestDiscoverNamespaces_NamespaceScoped(t *testing.T) {
	client := fakeClientWithSAR(func(username, namespace, resource, verb string) bool {
		// Deny cluster-wide, allow only in "dev" namespace
		if namespace == "" {
			return false
		}
		return namespace == "dev"
	})

	ns, err := DiscoverNamespaces(context.Background(), client, "alice", nil, []string{"dev", "prod", "staging"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ns == nil {
		t.Fatal("expected namespace list, got nil (cluster admin)")
	}
	if len(ns) != 1 || ns[0] != "dev" {
		t.Errorf("expected [dev], got %v", ns)
	}
}

func TestDiscoverNamespaces_MultipleNamespaces(t *testing.T) {
	client := fakeClientWithSAR(func(username, namespace, resource, verb string) bool {
		if namespace == "" {
			return false
		}
		return namespace == "dev" || namespace == "staging"
	})

	ns, err := DiscoverNamespaces(context.Background(), client, "alice", nil, []string{"dev", "prod", "staging"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(ns) != 2 {
		t.Errorf("expected 2 namespaces, got %v", ns)
	}
	allowed := map[string]bool{}
	for _, n := range ns {
		allowed[n] = true
	}
	if !allowed["dev"] || !allowed["staging"] {
		t.Errorf("expected dev and staging, got %v", ns)
	}
}

func TestDiscoverNamespaces_NoAccess(t *testing.T) {
	client := fakeClientWithSAR(func(username, namespace, resource, verb string) bool {
		return false // deny everything
	})

	ns, err := DiscoverNamespaces(context.Background(), client, "nobody", nil, []string{"default", "kube-system"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ns == nil {
		t.Fatal("expected empty slice (not nil) for no access")
	}
	if len(ns) != 0 {
		t.Errorf("expected empty slice, got %v", ns)
	}
}

func TestDiscoverNamespaces_DeploymentFallback(t *testing.T) {
	// User can't list pods but CAN list deployments in "prod"
	client := fakeClientWithSAR(func(username, namespace, resource, verb string) bool {
		if namespace == "" {
			return false
		}
		return namespace == "prod" && resource == "deployments"
	})

	ns, err := DiscoverNamespaces(context.Background(), client, "alice", nil, []string{"dev", "prod"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(ns) != 1 || ns[0] != "prod" {
		t.Errorf("expected [prod] via deployment fallback, got %v", ns)
	}
}

func TestDiscoverNamespaces_EmptyNamespaceList(t *testing.T) {
	client := fakeClientWithSAR(func(username, namespace, resource, verb string) bool {
		return false
	})

	ns, err := DiscoverNamespaces(context.Background(), client, "alice", nil, []string{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// With no namespaces to check, cluster-wide check fails → empty result
	if ns == nil {
		t.Fatal("expected empty slice, got nil")
	}
	if len(ns) != 0 {
		t.Errorf("expected empty slice, got %v", ns)
	}
}

// --- SubjectCanI tests ---

func TestSubjectCanI_Allowed(t *testing.T) {
	client := fakeClientWithSAR(func(username, namespace, resource, verb string) bool {
		return username == "alice" && resource == "pods" && verb == "list"
	})

	allowed, err := SubjectCanI(context.Background(), client, "alice", nil, "default", "", "pods", "list")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !allowed {
		t.Error("expected allowed=true")
	}
}

func TestSubjectCanI_Denied(t *testing.T) {
	client := fakeClientWithSAR(func(username, namespace, resource, verb string) bool {
		return false
	})

	allowed, err := SubjectCanI(context.Background(), client, "alice", nil, "default", "", "pods", "list")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if allowed {
		t.Error("expected allowed=false")
	}
}

func TestSubjectCanI_WithGroups(t *testing.T) {
	var capturedGroups []string
	client := fake.NewClientset()
	client.PrependReactor("create", "subjectaccessreviews", func(action k8stesting.Action) (bool, runtime.Object, error) {
		sar := action.(k8stesting.CreateAction).GetObject().(*authv1.SubjectAccessReview)
		capturedGroups = sar.Spec.Groups
		return true, &authv1.SubjectAccessReview{
			Status: authv1.SubjectAccessReviewStatus{Allowed: true},
		}, nil
	})

	groups := []string{"devs", "admins"}
	SubjectCanI(context.Background(), client, "alice", groups, "default", "", "pods", "list")

	if len(capturedGroups) != 2 || capturedGroups[0] != "devs" || capturedGroups[1] != "admins" {
		t.Errorf("groups not passed to SAR: got %v", capturedGroups)
	}
}
