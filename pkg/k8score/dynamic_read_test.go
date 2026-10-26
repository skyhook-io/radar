package k8score

import (
	"context"
	"errors"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ktesting "k8s.io/client-go/testing"
)

func TestReadWithSourceCheckServingInformer(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "example.io", Version: "v1", Resource: "widgets"}
	for _, tc := range []struct {
		name              string
		replace, hold     bool
		wantAuthoritative bool
	}{
		{name: "same synced source", wantAuthoritative: true},
		{name: "replacement synced on both sides", replace: true},
		{name: "replacement still unsynced", replace: true, hold: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			present := &unstructured.Unstructured{Object: map[string]any{
				"apiVersion": "example.io/v1", "kind": "Widget", "metadata": map[string]any{"name": "present", "namespace": "alpha"},
			}}
			dyn := newWidgetDynamicClient(present)
			release := make(chan struct{})
			defer close(release)
			dyn.PrependReactor("list", "widgets", func(a ktesting.Action) (bool, runtime.Object, error) {
				if a.GetNamespace() == "" && tc.hold {
					<-release
				}
				return false, nil, nil
			})
			d, err := NewDynamicResourceCache(DynamicCacheConfig{DynamicClient: dyn})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(d.Stop)
			if err := d.startWatching(gvr, "alpha"); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			if !d.WaitForSyncContext(ctx, gvr, "alpha") {
				t.Fatal("namespace informer did not sync")
			}
			reads := 0
			rows, authoritative, err := ReadWithSourceCheck(d, gvr, "alpha", func() ([]*unstructured.Unstructured, error) {
				reads++
				if tc.replace {
					if err := d.startWatching(gvr, ""); err != nil {
						t.Fatal(err)
					}
					if !tc.hold && !d.WaitForSyncContext(ctx, gvr, "alpha") {
						t.Fatal("replacement did not sync")
					}
				}
				return d.ListWatchedReadOnly(gvr)
			})
			if err != nil || authoritative != tc.wantAuthoritative || reads != 1 {
				t.Fatalf("authoritative=%v err=%v reads=%d", authoritative, err, reads)
			}
			if !tc.hold && len(rows) != 1 {
				t.Fatalf("rows = %d, want 1", len(rows))
			}
		})
	}
}

func TestReadWithSourceCheckFoundBeforeSync(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "example.io", Version: "v1", Resource: "widgets"}
	dyn := newWidgetDynamicClient()
	release := make(chan struct{})
	defer close(release)
	dyn.PrependReactor("list", "widgets", func(ktesting.Action) (bool, runtime.Object, error) { <-release; return false, nil, nil })
	d, err := NewDynamicResourceCache(DynamicCacheConfig{DynamicClient: dyn})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.Stop)
	if err := d.startWatching(gvr, ""); err != nil {
		t.Fatal(err)
	}
	present := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "example.io/v1", "kind": "Widget", "metadata": map[string]any{"name": "present", "namespace": "alpha"},
	}}
	entry := d.readEntries(gvr, "alpha")[0]
	if err := entry.informer.GetIndexer().Add(present); err != nil {
		t.Fatal(err)
	}
	reads := 0
	obj, authoritative, err := ReadWithSourceCheck(d, gvr, "alpha", func() (*unstructured.Unstructured, error) {
		reads++
		return d.Get(gvr, "alpha", "present")
	})
	if authoritative || err != nil || obj == nil || obj.GetName() != "present" || reads != 1 {
		t.Fatalf("found=%v authoritative=%v err=%v reads=%d", obj, authoritative, err, reads)
	}
	miss, authoritative, err := ReadWithSourceCheck(d, gvr, "alpha", func() (*unstructured.Unstructured, error) {
		return d.GetWatched(gvr, "alpha", "missing")
	})
	if miss != nil || authoritative || !errors.Is(err, ErrResourceNotFound) {
		t.Fatalf("miss=%v authoritative=%v err=%v", miss, authoritative, err)
	}
	sentinel := errors.New("read failed")
	value, authoritative, err := ReadWithSourceCheck(d, gvr, "alpha", func() (int, error) { return 7, sentinel })
	if value != 7 || authoritative || err != sentinel {
		t.Fatalf("value=%d authoritative=%v err=%v", value, authoritative, err)
	}
}
