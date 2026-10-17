package issues

import (
	"errors"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/issuesapi"
	"github.com/skyhook-io/radar/pkg/k8score"
)

func TestAbsenceUncheckedSources(t *testing.T) {
	client := fake.NewClientset()
	client.PrependWatchReactor("secrets", func(k8stesting.Action) (bool, watch.Interface, error) {
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: "secrets"}, "", errors.New("watch is not allowed"))
	})
	scopes := map[string]k8score.ResourceScope{
		k8score.Secrets:    {Enabled: true},
		k8score.ConfigMaps: {Enabled: true},
	}
	core, err := k8score.NewResourceCache(k8score.CacheConfig{
		Client:         client,
		ResourceTypes:  map[string]bool{k8score.Secrets: true, k8score.ConfigMaps: true},
		DeferredTypes:  map[string]bool{},
		ResourceScopes: scopes,
	})
	if err != nil {
		t.Fatalf("NewResourceCache: %v", err)
	}
	t.Cleanup(core.Stop)
	cache := &k8s.ResourceCache{ResourceCache: core}
	deadline := time.Now().Add(10 * time.Second)
	for !core.KindListOnly(k8score.Secrets, "") {
		if time.Now().After(deadline) {
			t.Fatal("Secrets informer never reported list-only")
		}
		time.Sleep(20 * time.Millisecond)
	}

	want := issuesapi.UncheckedSource{Source: issuesapi.UncheckedSourceAbsence, Reason: issuesapi.UncheckedReasonListOnly, Resource: "secrets"}
	for _, namespaces := range [][]string{nil, {"shop"}} {
		got := AbsenceUncheckedSources(cache, namespaces)
		if len(got) != 1 || got[0] != want {
			t.Fatalf("namespaces %v: got %+v, want only %+v", namespaces, got, want)
		}
	}
	if got := AbsenceUncheckedSources(cache, []string{}); got != nil {
		t.Fatalf("no namespace access: want nothing, got %+v", got)
	}
}

// A route in namespace a can reference a Service in namespace b, so a
// list-only Services informer in b is reported to a caller scoped to a.
func TestAbsenceUncheckedSources_CrossNamespaceTarget(t *testing.T) {
	client := fake.NewClientset()
	client.PrependWatchReactor("services", func(action k8stesting.Action) (bool, watch.Interface, error) {
		if action.GetNamespace() != "b" {
			return false, nil, nil
		}
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: "services"}, "", errors.New("watch is not allowed"))
	})
	core, err := k8score.NewResourceCache(k8score.CacheConfig{
		Client:                  client,
		ResourceTypes:           map[string]bool{k8score.Services: true},
		DeferredTypes:           map[string]bool{},
		ResourceScopes:          map[string]k8score.ResourceScope{k8score.Services: {Enabled: true, Namespace: "a"}},
		ResourceScopeNamespaces: map[string][]string{k8score.Services: {"a", "b"}},
	})
	if err != nil {
		t.Fatalf("NewResourceCache: %v", err)
	}
	t.Cleanup(core.Stop)
	deadline := time.Now().Add(10 * time.Second)
	for !core.KindListOnly(k8score.Services, "b") {
		if time.Now().After(deadline) {
			t.Fatal("Services informer in b never reported list-only")
		}
		time.Sleep(20 * time.Millisecond)
	}
	got := AbsenceUncheckedSources(&k8s.ResourceCache{ResourceCache: core}, []string{"a"})
	if len(got) != 1 || got[0].Resource != "services" {
		t.Fatalf("want services reported to a caller scoped to a, got %+v", got)
	}
}
