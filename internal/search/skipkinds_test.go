package search

import (
	"context"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// SkipKinds gates kinds the calling user's RBAC excludes — even when
// the underlying SA-driven cache holds the objects. The handler
// populates SkipKinds via SARs; these tests pin the walker contract
// that consumes the map.

func TestSearch_SkipKinds_DropsKindEntirely(t *testing.T) {
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
		Include:   IncludeNone,
		SkipKinds: map[string]bool{"Secret": true},
	}
	// Default-scan: pods come through, secret excluded.
	res, _ := Search(context.Background(), p, Parse("db-cred"), opts)
	for _, h := range res.Hits {
		if h.Kind == "Secret" {
			t.Fatalf("Secret leaked into default search despite SkipKinds: %+v", h)
		}
	}
	// Explicit kind:Secret request: still zero (silent — same as RBAC
	// forbidden on the lister today).
	res, _ = Search(context.Background(), p, Parse("kind:Secret db-cred"), opts)
	if len(res.Hits) != 0 {
		t.Fatalf("kind:Secret returned hits despite SkipKinds: %+v", res.Hits)
	}
}

func TestSearch_SkipKinds_NilMapIsNoOp(t *testing.T) {
	// Empty/nil SkipKinds preserves the default scan — backward
	// compat for auth-mode=none / non-cloud installs.
	p := &fakeProvider{
		typed: map[string][]runtime.Object{
			"secrets": {
				&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "db-cred"}},
			},
		},
	}
	res, _ := Search(context.Background(), p, Parse("db-cred"), Options{Include: IncludeNone, SkipKinds: nil})
	if len(res.Hits) == 0 {
		t.Fatal("nil SkipKinds should not gate Secrets")
	}
}

func TestSearch_SkipKinds_PreservesOtherKinds(t *testing.T) {
	// Skipping Secret must NOT affect Deployments / Pods scanning.
	p := &fakeProvider{
		typed: map[string][]runtime.Object{
			"secrets":     {&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "redis-cred"}}},
			"deployments": {newDeploy("ns", "redis-cache", "redis:6", nil)},
		},
	}
	res, _ := Search(context.Background(), p, Parse("redis"), Options{
		Include:   IncludeNone,
		SkipKinds: map[string]bool{"Secret": true, "Node": true, "PersistentVolume": true},
	})
	var kinds []string
	for _, h := range res.Hits {
		kinds = append(kinds, h.Kind)
	}
	if len(kinds) != 1 || kinds[0] != "Deployment" {
		t.Fatalf("expected only Deployment, got %v", kinds)
	}
}

// Keep appsv1 referenced so future test cases retain the import without
// linter churn.
var _ = appsv1.SchemeGroupVersion

// recordingProvider wraps fakeProvider to capture the namespaces argument
// each ListTyped call receives. Used to verify per-kind namespace scoping.
type recordingProvider struct {
	*fakeProvider
	listedWith map[string][]string // kind → namespaces last passed
}

func (r *recordingProvider) ListTyped(kind string, namespaces []string) ([]runtime.Object, error) {
	if r.listedWith == nil {
		r.listedWith = make(map[string][]string)
	}
	r.listedWith[kind] = append([]string(nil), namespaces...)
	return r.fakeProvider.ListTyped(kind, namespaces)
}

func TestSearch_NamespacesByKind_OverridesPerKind(t *testing.T) {
	// Per-kind namespace override. When Secrets get a tighter scope than the
	// global Options.Namespaces (e.g. user has list pods in ns-a+ns-b but
	// list secrets only in ns-a), Secrets must list only the override set
	// while other kinds keep using Options.Namespaces.
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
		Include:          IncludeNone,
		Namespaces:       []string{"ns-a", "ns-b"},
		NamespacesByKind: map[string][]string{"Secret": {"ns-a"}},
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

func TestSearch_NamespacesByKind_NilFallsThroughToNamespaces(t *testing.T) {
	rec := &recordingProvider{
		fakeProvider: &fakeProvider{
			typed: map[string][]runtime.Object{
				"secrets": {&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "ns-a", Name: "redis-cred"}}},
			},
		},
	}
	_, _ = Search(context.Background(), rec, Parse("redis"), Options{
		Include:          IncludeNone,
		Namespaces:       []string{"ns-a"},
		NamespacesByKind: nil,
	})

	if got := rec.listedWith["secrets"]; len(got) != 1 || got[0] != "ns-a" {
		t.Errorf("secrets listed with %v, want [ns-a] (Options.Namespaces fallback)", got)
	}
}

func TestSearch_NamespacesByKind_NilEntryDoesNotBypass(t *testing.T) {
	// A nil entry in the per-kind map must fall back to Options.Namespaces,
	// not become a cluster-wide list. Pin the doc/code agreement so a future
	// caller passing map[string][]string{"Secret": nil} can't silently widen
	// scope past the user's RBAC-allowed namespaces.
	rec := &recordingProvider{
		fakeProvider: &fakeProvider{
			typed: map[string][]runtime.Object{
				"secrets": {&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "ns-a", Name: "redis-cred"}}},
			},
		},
	}
	_, _ = Search(context.Background(), rec, Parse("redis"), Options{
		Include:          IncludeNone,
		Namespaces:       []string{"ns-a"},
		NamespacesByKind: map[string][]string{"Secret": nil},
	})

	if got := rec.listedWith["secrets"]; len(got) != 1 || got[0] != "ns-a" {
		t.Errorf("secrets listed with %v, want [ns-a] (nil override should fall back, not go cluster-wide)", got)
	}
}
