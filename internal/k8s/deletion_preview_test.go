package k8s

import (
	"context"
	"github.com/skyhook-io/radar/pkg/ownership"
	"github.com/skyhook-io/radar/pkg/readreason"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"testing"
)

func TestDeletionPreviewRefusesChangedOperationBeforeCacheReplacement(t *testing.T) {
	operation, cancel := context.WithCancel(context.Background())
	cancel()
	obj := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "ConfigMap", "metadata": map[string]any{"name": "old", "namespace": "demo", "uid": "old"}}}
	p := DeletionPreview(context.Background(), obj, operation, func(*ownership.Catalog) ownership.Access { return ownership.Unrestricted() })
	if p.RootResolved || p.UnavailableReason != string(readreason.ConnectionChanged) || len(p.Dependents) != 0 {
		t.Fatalf("stale operation preview: %+v", p)
	}
}
