package search

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/skyhook-io/radar/internal/readscope"
)

func TestSearchDeniedSecretIsNeverReturned(t *testing.T) {
	// Secret is in the cache; SAR said no — the row must not surface
	// even when the user explicitly types kind:Secret.
	p := &fakeProvider{
		typed: map[string][]runtime.Object{
			"secrets": {
				&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "db-cred"}},
			},
			"pods": {
				&corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "db-cred-loader"},
					Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "c", Image: "redis"}}},
				},
			},
		},
	}
	opts := Options{
		Include: IncludeNone,
		TypedKindAccess: func(ns []string, group, resource string) ([]string, []string, []string) {
			if resource == "secrets" {
				return []string{}, []string{""}, nil
			}
			return ns, nil, nil
		},
	}
	// Default-scan: pods come through, secret excluded.
	res, _ := Search(context.Background(), p, Parse("db-cred"), opts)
	for _, h := range res.Hits {
		if h.Kind == "Secret" {
			t.Fatalf("Secret leaked into default search despite denied access: %+v", h)
		}
	}
	// A named kind remains subject to the same access gate.
	res, _ = Search(context.Background(), p, Parse("kind:Secret db-cred"), opts)
	if len(res.Hits) != 0 {
		t.Fatalf("kind:Secret returned hits despite denied access: %+v", res.Hits)
	}
}

func TestSearchDeniedKindsPreserveReadableKinds(t *testing.T) {
	// Skipping Secret must NOT affect Deployments / Pods scanning.
	p := &fakeProvider{
		typed: map[string][]runtime.Object{
			"secrets":     {&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "redis-cred"}}},
			"deployments": {newDeploy("ns", "redis-cache", "redis:6", nil)},
		},
	}
	res, _ := Search(context.Background(), p, Parse("redis"), Options{
		Include: IncludeNone,
		TypedKindAccess: func(ns []string, group, resource string) ([]string, []string, []string) {
			if resource == "secrets" {
				return []string{}, []string{""}, nil
			}
			return ns, nil, nil
		}, CanReadClusterScoped: func(kind, group, resource string) (bool, bool) {
			return kind != "Node" && kind != "PersistentVolume", true
		},
	})
	var kinds []string
	for _, h := range res.Hits {
		kinds = append(kinds, h.Kind)
	}
	if len(kinds) != 1 || kinds[0] != "Deployment" {
		t.Fatalf("expected only Deployment, got %v", kinds)
	}
}

// recordingProvider wraps fakeProvider to capture the namespaces argument
// each ListTyped call receives. Used to verify per-kind namespace scoping.
type recordingProvider struct {
	*fakeProvider
	listedWith map[string][]string // kind → namespaces last passed
}

func (r *recordingProvider) ListTyped(resource string, namespaces []string) ([]runtime.Object, error) {
	if r.listedWith == nil {
		r.listedWith = make(map[string][]string)
	}
	r.listedWith[resource] = append([]string(nil), namespaces...)
	return r.fakeProvider.ListTyped(resource, namespaces)
}

func TestSearchSecretAccessNarrowsOnlySecrets(t *testing.T) {

	rec := &recordingProvider{
		fakeProvider: &fakeProvider{
			typed: map[string][]runtime.Object{
				"secrets": {
					&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "ns-a", Name: "redis-cred"}},
				},
				"pods": {
					&corev1.Pod{
						ObjectMeta: metav1.ObjectMeta{Namespace: "ns-a", Name: "redis-loader"},
						Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "c", Image: "redis"}}},
					},
				},
			},
		},
	}
	res, _ := Search(context.Background(), rec, Parse("redis"), Options{
		Include:   IncludeNone,
		Selection: readscope.Selection{Namespaces: []string{"ns-a", "ns-b"}},
		TypedKindAccess: func(ns []string, group, resource string) ([]string, []string, []string) {
			if resource == "secrets" {
				return []string{"ns-a"}, []string{"ns-b"}, nil
			}
			return ns, nil, nil
		},
	})

	if got := rec.listedWith["pods"]; len(got) != 2 || got[0] != "ns-a" || got[1] != "ns-b" {
		t.Errorf("pods listed with %v, want [ns-a ns-b]", got)
	}
	if got := rec.listedWith["secrets"]; len(got) != 1 || got[0] != "ns-a" {
		t.Errorf("secrets listed with %v, want [ns-a]", got)
	}
	if len(res.Hits) == 0 {
		t.Error("expected at least one hit")
	}
}
