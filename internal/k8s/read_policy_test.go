package k8s

import (
	"k8s.io/apimachinery/pkg/runtime/schema"
	"testing"
)

func TestRequiresExactNamespaceAccess(t *testing.T) {
	for _, tc := range []struct {
		group, version, resource string
		want                     bool
	}{
		{"", "v1", "secrets", true},
		{"", "v1", "limitranges", true},
		{"aquasecurity.github.io", "v1alpha1", "vulnerabilityreports", true},
		{"aquasecurity.github.io", "v1alpha1", "exposedsecretreports", true},
		{"aquasecurity.github.io", "v1", "newreports", true},
		{"", "v1", "pods", false},
		{"", "v1", "secret", false},
		{"example.com", "v1", "secrets", false},
		{"example.com", "v1", "limitranges", false},
		{"example.com", "v1", "roles", false},
		{"example.com", "v1", "rolebindings", false},
		{"rbac.authorization.k8s.io", "v1", "clusterroles", false},
		{"rbac.authorization.k8s.io", "v1", "roles", true},
		{"rbac.authorization.k8s.io", "v1", "rolebindings", true},
		{"example.com", "v1", "vulnerabilityreports", false},
		{"", "", "", false},
	} {
		gvr := schema.GroupVersionResource{Group: tc.group, Version: tc.version, Resource: tc.resource}
		if got := RequiresExactNamespaceAccess(gvr); got != tc.want {
			t.Errorf("%s = %t, want %t", gvr, got, tc.want)
		}
	}
}

func TestExactNamespaceAccessResource(t *testing.T) {
	for _, tc := range []struct{ kind, group, wantGroup, wantResource string }{
		{"secrets", "", "", "secrets"},
		{"secret", "", "", "secrets"},
		{"limitranges", "", "", "limitranges"},
		{"limitrange", "", "", "limitranges"},
		{"roles", "", "rbac.authorization.k8s.io", "roles"},
		{"rolebindings", "rbac.authorization.k8s.io", "rbac.authorization.k8s.io", "rolebindings"},
		{"roles", "example.com", "", ""},
		{"pods", "", "", ""},
		{"secrets", "example.com", "", ""},
		{"widgets", "aquasecurity.github.io", "aquasecurity.github.io", "widgets"},
	} {
		group, resource := ExactNamespaceAccessResource(tc.kind, tc.group)
		if group != tc.wantGroup || resource != tc.wantResource {
			t.Errorf("%s/%s = %s/%s, want %s/%s", tc.group, tc.kind, group, resource, tc.wantGroup, tc.wantResource)
		}
	}
}
