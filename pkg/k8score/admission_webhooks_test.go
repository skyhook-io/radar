package k8score

import (
	"context"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestAdmissionWebhookConsumerIndexWarmupUpdatesAndHotReads(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "admissionregistration.k8s.io", Version: "v1", Resource: "mutatingwebhookconfigurations"}
	obj := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "admissionregistration.k8s.io/v1", "kind": "MutatingWebhookConfiguration", "metadata": map[string]any{"name": "hook"}, "webhooks": []any{map[string]any{"clientConfig": map[string]any{"service": map[string]any{"namespace": "backend", "name": "admission"}}}, map[string]any{"clientConfig": map[string]any{"service": map[string]any{"namespace": "backend", "name": "admission"}}}}}}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{gvr: "MutatingWebhookConfigurationList"}, obj)
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
	disc := &ResourceDiscovery{resources: []APIResource{{Group: gvr.Group, Version: gvr.Version, Kind: "MutatingWebhookConfiguration", Name: gvr.Resource, Namespaced: false, Verbs: []string{"get", "list", "watch"}}}, resourceMap: map[string]APIResource{}, gvrMap: map[string]schema.GroupVersionResource{}, lastRefresh: time.Now(), cacheTTL: time.Hour}
	d, err := NewDynamicResourceCache(DynamicCacheConfig{DynamicClient: dyn, Discovery: disc})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Stop()
	refs, complete, err := d.AdmissionWebhookConsumers(gvr, "backend", "admission")
	if err != nil || complete || len(refs) != 0 {
		t.Fatalf("unsynced index: refs=%+v complete=%v err=%v", refs, complete, err)
	}
	unblock()
	wait := func(check func() bool) {
		t.Helper()
		deadline := time.Now().Add(3 * time.Second)
		for !check() {
			if time.Now().After(deadline) {
				t.Fatal("informer index did not settle")
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	wait(func() bool {
		r, c, e := d.AdmissionWebhookConsumers(gvr, "backend", "admission")
		if e != nil {
			t.Fatal(e)
		}
		return c && len(r) == 1
	})
	wait(func() bool {
		for _, a := range dyn.Actions() {
			if a.GetVerb() == "watch" {
				return true
			}
		}
		return false
	})
	baseline := len(dyn.Actions())
	for i := 0; i < 100; i++ {
		r, c, e := d.AdmissionWebhookConsumers(gvr, "backend", "admission")
		if e != nil || !c || len(r) != 1 {
			t.Fatalf("hot lookup: %+v %v %v", r, c, e)
		}
	}
	if len(dyn.Actions()) != baseline {
		t.Fatalf("hot lookup issued API actions: before %d after %d", baseline, len(dyn.Actions()))
	}
	refs, complete, err = d.AdmissionWebhookConsumers(gvr, "other", "admission")
	if err != nil || !complete || len(refs) != 0 {
		t.Fatalf("namespace collision: %+v %v %v", refs, complete, err)
	}
	updated := obj.DeepCopy()
	updated.Object["webhooks"] = []any{map[string]any{"clientConfig": map[string]any{"service": map[string]any{"namespace": "backend", "name": "replacement"}}}}
	if _, err := dyn.Resource(gvr).Update(context.Background(), updated, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	wait(func() bool {
		old, _, e := d.AdmissionWebhookConsumers(gvr, "backend", "admission")
		new, _, e2 := d.AdmissionWebhookConsumers(gvr, "backend", "replacement")
		if e != nil || e2 != nil {
			t.Fatal(e, e2)
		}
		return len(old) == 0 && len(new) == 1
	})
	if err := dyn.Resource(gvr).Delete(context.Background(), obj.GetName(), metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	wait(func() bool {
		r, c, e := d.AdmissionWebhookConsumers(gvr, "backend", "replacement")
		if e != nil {
			t.Fatal(e)
		}
		return c && len(r) == 0
	})
	wrong := gvr
	wrong.Group = "custom.example"
	if _, _, err := d.AdmissionWebhookConsumers(wrong, "backend", "admission"); err == nil {
		t.Fatal("custom group accepted")
	}
}
