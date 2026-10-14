package k8s

import (
	"context"
	"reflect"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/skyhook-io/radar/pkg/k8score"
	"github.com/skyhook-io/radar/pkg/resourcecontext"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

func TestTopologyAdapterWebhookContextCacheOnly(t *testing.T) {
	ctx := context.Background()
	gvr := schema.GroupVersionResource{Group: "admissionregistration.k8s.io", Version: "v1", Resource: "mutatingwebhookconfigurations"}
	obj := webhookConfig("MutatingWebhookConfiguration", "hook", metav1.Now(), []any{webhookWithService("hook.example.com", "backend", "admission")})
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{gvr: "MutatingWebhookConfigurationList"}, obj)
	discovery, err := k8score.NewResourceDiscovery(fake.NewClientset().Discovery())
	if err != nil {
		t.Fatal(err)
	}
	discovery.AddAPIResource(APIResource{Group: gvr.Group, Version: gvr.Version, Kind: obj.GetKind(), Name: gvr.Resource, Verbs: []string{"get", "list", "watch"}})
	core, err := k8score.NewDynamicResourceCache(k8score.DynamicCacheConfig{DynamicClient: dyn, Discovery: discovery})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(core.Stop)
	adapter := NewTopologyDynamicProvider(&DynamicResourceCache{core}, &ResourceDiscovery{discovery})
	build := func(name string) *resourcecontext.ResourceContext {
		return resourcecontext.Build(ctx, &corev1.Service{ObjectMeta: metav1.ObjectMeta{Namespace: "backend", Name: name}}, resourcecontext.Options{DynamicProv: adapter})
	}
	cold := resourcecontext.OmittedField{Field: "dependents", Reason: resourcecontext.OmittedCacheCold}
	rc := build("admission")
	if len(rc.Dependents) != 0 || !slices.Contains(rc.Omitted, cold) || len(dyn.Actions()) != 0 {
		t.Fatalf("unwatched scan: %+v actions=%+v", rc, dyn.Actions())
	}
	var lists atomic.Int32
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	dyn.PrependReactor("list", gvr.Resource, func(k8stesting.Action) (bool, runtime.Object, error) {
		if lists.Add(1) > 1 {
			<-release
		}
		return false, nil, nil
	})
	if err := core.EnsureWatching(gvr); err != nil {
		t.Fatal(err)
	}
	rc = build("admission")
	if len(rc.Dependents) != 0 || !slices.Contains(rc.Omitted, cold) {
		t.Fatalf("unsynced scan: %+v", rc)
	}
	unblock()
	if !core.WaitForSync(gvr, 3*time.Second) {
		t.Fatal("cache did not sync")
	}
	wait := func(check func() bool) {
		t.Helper()
		deadline := time.Now().Add(3 * time.Second)
		for !check() {
			if time.Now().After(deadline) {
				t.Fatal("cache did not settle")
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	wait(func() bool {
		for _, a := range dyn.Actions() {
			if a.GetVerb() == "watch" {
				return true
			}
		}
		return false
	})
	baseline := len(dyn.Actions())
	for range 10 {
		rc = build("admission")
		if !reflect.DeepEqual(rc.Dependents, []resourcecontext.ContextRef{{Group: gvr.Group, Kind: obj.GetKind(), Name: obj.GetName()}}) {
			t.Fatalf("synced scan: %+v", rc)
		}
	}
	if len(dyn.Actions()) != baseline {
		t.Fatalf("scan issued API actions: %+v", dyn.Actions()[baseline:])
	}
	updated := obj.DeepCopy()
	updated.Object["webhooks"] = []any{webhookWithService("hook.example.com", "backend", "replacement")}
	if _, err := dyn.Resource(gvr).Update(ctx, updated, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	wait(func() bool {
		return len(build("admission").Dependents) == 0 && len(build("replacement").Dependents) == 1
	})
	if err := dyn.Resource(gvr).Delete(ctx, obj.GetName(), metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	wait(func() bool { return len(build("replacement").Dependents) == 0 })
}
