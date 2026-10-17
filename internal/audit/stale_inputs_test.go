package audit

import (
	"errors"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/k8score"
)

// A list-only kind whose LIST stopped completing is a frozen snapshot: the
// audit treats it as a missing input rather than judging it.
func TestStaleListOnlyKindsAreMissingInputs(t *testing.T) {
	pvc := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: "data", Namespace: "shop"}}
	svc := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "shop"}}
	client := fake.NewClientset(pvc, svc)
	for _, resource := range []string{"persistentvolumeclaims", "services"} {
		gr := schema.GroupResource{Resource: resource}
		client.PrependWatchReactor(resource, func(k8stesting.Action) (bool, watch.Interface, error) {
			return true, nil, apierrors.NewForbidden(gr, "", errors.New("watch is not allowed"))
		})
	}
	scopes := map[string]k8score.ResourceScope{}
	types := map[string]bool{}
	for _, key := range []string{k8score.PersistentVolumeClaims, k8score.Services, k8score.Pods} {
		scopes[key] = k8score.ResourceScope{Enabled: true}
		types[key] = true
	}
	core, err := k8score.NewResourceCache(k8score.CacheConfig{
		Client:             client,
		ResourceTypes:      types,
		DeferredTypes:      map[string]bool{},
		ResourceScopes:     scopes,
		ListOnlyStaleAfter: time.Nanosecond,
	})
	if err != nil {
		t.Fatalf("NewResourceCache: %v", err)
	}
	t.Cleanup(core.Stop)
	deadline := time.Now().Add(10 * time.Second)
	for !core.KindStale(k8score.PersistentVolumeClaims, "") || !core.KindStale(k8score.Services, "") {
		if time.Now().After(deadline) {
			t.Fatal("kinds never reported stale")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cache := &k8s.ResourceCache{ResourceCache: core}

	if got := collectStorageInput(cache, nil, nil).PersistentVolumeClaims; got != nil {
		t.Fatalf("stale PVCs must be a missing input, got %d", len(got))
	}
	input := CollectTypedInput(cache, nil)
	if input.Services != nil {
		t.Fatalf("stale Services must be a missing input, got %d", len(input.Services))
	}
	if input.Pods == nil {
		t.Fatal("watched Pods must stay an input")
	}
}
