package k8s

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/skyhook-io/radar/pkg/k8score"
	"github.com/skyhook-io/radar/pkg/ownership"
	"github.com/skyhook-io/radar/pkg/ownership/cachesource"
	"github.com/skyhook-io/radar/pkg/relations"
	"github.com/skyhook-io/radar/pkg/resourcecontext"
	"github.com/skyhook-io/radar/pkg/resourceid"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

func TestServiceAccountInventoryStateReachesSecretContext(t *testing.T) {
	for _, state := range []string{"pending", "failed", "not_collected", "ready_empty", "ready_match"} {
		t.Run(state, func(t *testing.T) {
			enabled := map[string]bool{
				k8score.Pods: true, k8score.Deployments: true, k8score.StatefulSets: true,
				k8score.DaemonSets: true, k8score.ReplicaSets: true, k8score.Jobs: true, k8score.CronJobs: true,
				k8score.Ingresses: true, k8score.Secrets: true, k8score.ServiceAccounts: state != "not_collected",
			}
			release := make(chan struct{})
			cache := newAdapterCache(t, enabled, map[string]bool{k8score.ServiceAccounts: state == "pending" || state == "failed"}, 100*time.Millisecond, func(c *fake.Clientset) {
				// The Secret is in the cache: a subject the cache lacks would
				// leave every answer about it incomplete, whatever the inventory.
				if _, err := c.CoreV1().Secrets("team").Create(context.Background(), &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "credentials", Namespace: "team"}}, metav1.CreateOptions{}); err != nil {
					t.Fatal(err)
				}
				switch state {
				case "pending":
					c.PrependReactor("list", "serviceaccounts", func(k8stesting.Action) (bool, runtime.Object, error) {
						<-release
						return true, &corev1.ServiceAccountList{}, nil
					})
				case "failed":
					c.PrependReactor("list", "serviceaccounts", func(k8stesting.Action) (bool, runtime.Object, error) {
						return true, nil, fmt.Errorf("fixture source failed")
					})
				case "ready_match":
					_, err := c.CoreV1().ServiceAccounts("team").Create(context.Background(), &corev1.ServiceAccount{
						ObjectMeta: metav1.ObjectMeta{Name: "runner", Namespace: "team"},
						Secrets:    []corev1.ObjectReference{{Name: "credentials"}},
					}, metav1.CreateOptions{})
					if err != nil {
						t.Fatal(err)
					}
				}
			})
			t.Cleanup(func() { close(release) })
			if state == "pending" && cache.ServiceAccounts() == nil {
				t.Fatal("pending fixture must expose a non-nil unsynced lister")
			}
			if state == "failed" {
				deadline := time.Now().Add(2 * time.Second)
				for cache.KindReadinessFor(k8score.ServiceAccounts) != k8score.KindFailed && time.Now().Before(deadline) {
					time.Sleep(10 * time.Millisecond)
				}
				if cache.KindReadinessFor(k8score.ServiceAccounts) != k8score.KindFailed {
					t.Fatal("fixture did not reach failed inventory state")
				}
			}
			got := serviceAccountSecretContext(t, cache, "team")
			want := resourcecontext.OmittedReason("")
			if state == "pending" {
				want = resourcecontext.OmittedCacheCold
			} else if state == "failed" || state == "not_collected" {
				want = resourcecontext.OmittedUnavailable
			}
			if want != "" {
				if got.ReferencedBy != nil || !slices.Contains(got.Omitted, resourcecontext.OmittedField{Field: "referencedBy", Reason: want}) {
					t.Fatalf("inventory state lost at context boundary: %+v", got)
				}
			} else {
				if slices.ContainsFunc(got.Omitted, func(field resourcecontext.OmittedField) bool { return field.Field == "referencedBy" }) {
					t.Fatalf("ready inventory reported incomplete: %+v", got.Omitted)
				}
				if state == "ready_empty" && got.ReferencedBy != nil {
					t.Fatalf("empty inventory invented a consumer: %+v", got.ReferencedBy)
				}
				if state == "ready_match" && (got.ReferencedBy == nil || got.ReferencedBy.Total != 1 || got.ReferencedBy.Items[0].Kind != "ServiceAccount") {
					t.Fatalf("ready declaring account missing: %+v", got.ReferencedBy)
				}
			}
		})
	}
}

func TestServiceAccountInventoryOutsideCollectedNamespaces(t *testing.T) {
	scopes := map[string]k8score.ResourceScope{}
	for _, kind := range []string{k8score.Pods, k8score.Deployments, k8score.StatefulSets, k8score.DaemonSets, k8score.ReplicaSets, k8score.Jobs, k8score.CronJobs, k8score.Ingresses, k8score.Secrets} {
		scopes[kind] = k8score.ResourceScope{Enabled: true}
	}
	scopes[k8score.ServiceAccounts] = k8score.ResourceScope{Enabled: true, Namespace: "collected"}
	client := fake.NewClientset(&corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{Name: "runner", Namespace: "uncollected"},
		Secrets:    []corev1.ObjectReference{{Name: "credentials"}},
	},
		// The Secrets are in the cache, as a real drawer's subject is.
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "credentials", Namespace: "uncollected"}},
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "credentials", Namespace: "collected"}},
	)
	core, err := k8score.NewResourceCache(k8score.CacheConfig{
		Client:                  client,
		ResourceScopes:          scopes,
		ResourceScopeNamespaces: map[string][]string{k8score.ServiceAccounts: {"collected"}},
		DeferredTypes:           map[string]bool{},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(core.Stop)
	cache := &ResourceCache{ResourceCache: core}
	deadline := time.Now().Add(2 * time.Second)
	for cache.KindReadinessFor(k8score.ServiceAccounts) != k8score.KindReady && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	unavailable := resourcecontext.OmittedField{Field: "referencedBy", Reason: resourcecontext.OmittedUnavailable}

	outside := serviceAccountSecretContext(t, cache, "uncollected")
	if outside.ReferencedBy != nil || !slices.Contains(outside.Omitted, unavailable) {
		t.Fatalf("uncollected ServiceAccount namespace read as complete: %+v", outside)
	}
	inside := serviceAccountSecretContext(t, cache, "collected")
	if slices.ContainsFunc(inside.Omitted, func(field resourcecontext.OmittedField) bool { return field.Field == "referencedBy" }) {
		t.Fatalf("collected namespace reported incomplete: %+v", inside.Omitted)
	}
}

func serviceAccountSecretContext(t *testing.T, cache *ResourceCache, namespace string) *resourcecontext.ResourceContext {
	t.Helper()
	discovery := buildFakeDiscoveryCore(t)
	kinds := []resourceid.GroupKind{{Kind: "Secret"}}
	for _, source := range relations.ConfigReferences.Sources {
		kinds = append(kinds, source.Kind)
	}
	for _, kind := range kinds {
		builtin, ok := resourceid.BuiltinForKind(kind.Kind)
		if !ok || builtin.GroupKind() != kind {
			continue
		}
		discovery.AddAPIResource(k8score.APIResource{
			Group: builtin.Group, Version: builtin.Version, Kind: builtin.Kind, Name: builtin.Resource,
			Namespaced: builtin.Namespaced, Verbs: metav1.Verbs{"get", "list", "watch", "delete"},
		})
	}
	previous := ownershipSource
	ownershipSource = cachesource.New(func() cachesource.Caches {
		return cachesource.Caches{Typed: cache.ResourceCache, Discovery: discovery}
	})
	t.Cleanup(func() { ownershipSource = previous })
	secret := &corev1.Secret{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"},
		ObjectMeta: metav1.ObjectMeta{Name: "credentials", Namespace: namespace},
	}
	access := func(*ownership.Catalog) ownership.Access { return ownership.Unrestricted() }
	rel, _, extras := AnswerResourceContextRelationships(cache, secret, access, access)
	return resourcecontext.Build(context.Background(), secret, resourcecontext.Options{Relationships: rel, RelationshipExtras: extras})
}
