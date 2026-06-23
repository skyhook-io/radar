package server

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func obj(labels, ann map[string]string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{Object: map[string]any{}}
	if labels != nil {
		u.SetLabels(labels)
	}
	if ann != nil {
		u.SetAnnotations(ann)
	}
	return u
}

func TestManagedByFromMeta(t *testing.T) {
	cases := []struct {
		name   string
		obj    *unstructured.Unstructured
		want   string
	}{
		{"argo label", obj(map[string]string{"argocd.argoproj.io/instance": "app"}, nil), "Argo CD"},
		{"argo annotation", obj(nil, map[string]string{"argocd.argoproj.io/tracking-id": "app:apps/Deployment"}), "Argo CD"},
		{"flux kustomize", obj(map[string]string{"kustomize.toolkit.fluxcd.io/name": "ks"}, nil), "Flux"},
		{"flux helm", obj(map[string]string{"helm.toolkit.fluxcd.io/name": "hr"}, nil), "Flux"},
		{"helm", obj(map[string]string{"app.kubernetes.io/managed-by": "Helm"}, map[string]string{"meta.helm.sh/release-name": "r"}), "Helm"},
		{"flux owns helm-installed → Flux wins", obj(map[string]string{"helm.toolkit.fluxcd.io/name": "hr", "app.kubernetes.io/managed-by": "Helm"}, nil), "Flux"},
		{"unmanaged", obj(map[string]string{"app": "x"}, nil), ""},
	}
	for _, c := range cases {
		if got := managedByFromMeta(c.obj); got != c.want {
			t.Errorf("%s: managedByFromMeta = %q, want %q", c.name, got, c.want)
		}
	}
}
