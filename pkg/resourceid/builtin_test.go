package resourceid

import (
	"strings"
	"testing"
)

func TestBuiltinTableIsConsistent(t *testing.T) {
	kinds := map[string]bool{}
	names := map[string]string{}
	for _, b := range Builtins {
		if b.Kind == "" || b.Resource == "" {
			t.Fatalf("incomplete entry: %+v", b)
		}
		if kinds[b.Kind] {
			t.Errorf("Kind %s listed twice", b.Kind)
		}
		kinds[b.Kind] = true
		if b.Resource != strings.ToLower(b.Resource) {
			t.Errorf("%s: resource %q is not lowercase", b.Kind, b.Resource)
		}
		if b.Version == "" && b.Group != "resource.k8s.io" {
			t.Errorf("%s: only resources whose served version varies may omit Version", b.Kind)
		}
		for _, name := range b.Names() {
			if name != strings.ToLower(name) {
				t.Errorf("%s: name %q is not lowercase", b.Kind, name)
			}
			if other, dup := names[name]; dup && other != b.Kind {
				t.Errorf("name %q addresses both %s and %s", name, other, b.Kind)
			}
			names[name] = b.Kind
		}
	}
}

func TestBuiltinForName(t *testing.T) {
	for name, want := range map[string]GroupKind{
		"deploy":      {Group: "apps", Kind: "Deployment"},
		"Deployments": {Group: "apps", Kind: "Deployment"},
		"deployment":  {Group: "apps", Kind: "Deployment"},
		"hpa":         {Group: "autoscaling", Kind: "HorizontalPodAutoscaler"},
		"ep":          {Group: "", Kind: "Endpoints"},
		"jobs":        {Group: "batch", Kind: "Job"},
	} {
		b, ok := BuiltinForName(name)
		if !ok || b.GroupKind() != want {
			t.Errorf("BuiltinForName(%q) = %v, %v; want %v", name, b.GroupKind(), ok, want)
		}
	}
	if _, ok := BuiltinForName("widgets"); ok {
		t.Error("BuiltinForName resolved a custom resource")
	}
}

func TestBuiltinScope(t *testing.T) {
	for kind, namespaced := range map[string]bool{
		"Pod": true, "Node": false, "Namespace": false, "Role": true, "ClusterRole": false,
		"ResourceClaim": true, "DeviceClass": false,
	} {
		b, ok := BuiltinForKind(kind)
		if !ok || b.Namespaced != namespaced {
			t.Errorf("%s: Namespaced = %v, want %v", kind, b.Namespaced, namespaced)
		}
	}
}

func TestBuiltinAPIVersionOmitsVaryingVersions(t *testing.T) {
	if av, ok := BuiltinAPIVersion("ResourceClaim"); ok {
		t.Fatalf("BuiltinAPIVersion(ResourceClaim) = %q; its served version varies by cluster", av)
	}
	if group, ok := BuiltinGroup("ResourceClaim"); !ok || group != "resource.k8s.io" {
		t.Fatalf("BuiltinGroup(ResourceClaim) = %q, %v", group, ok)
	}
}
