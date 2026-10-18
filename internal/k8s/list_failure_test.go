package k8s

import (
	"context"
	"testing"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"github.com/skyhook-io/radar/pkg/k8score"
)

// Every typed kind the cache watches must have a probe, or a kind whose list
// failed once could only recover through its own store moving.
func TestTypedListProbeCoversEveryCachedKind(t *testing.T) {
	listKinds := map[schema.GroupVersionResource]string{}
	for _, kl := range k8score.AllKindListers() {
		gvr, ok := BuiltinGVRAnyGroup(kl.Kind())
		if !ok || gvr.Group != kl.Group() {
			t.Errorf("%s (group %q): no probe resource, got %v", kl.Kind(), kl.Group(), gvr)
			continue
		}
		listKinds[gvr] = kl.Kind() + "List"
	}
	probe := typedListProbe(dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), listKinds))
	for _, kl := range k8score.AllKindListers() {
		if err := probe(context.Background(), kl.Kind(), "default"); err != nil {
			t.Errorf("probe %s: %v", kl.Kind(), err)
		}
	}
	if err := probe(context.Background(), "NoSuchKind", ""); err == nil {
		t.Error("a kind with no resource passed its probe")
	}
	if err := typedListProbe(nil)(context.Background(), "Pod", ""); err == nil {
		t.Error("a probe without a client passed")
	}
}
