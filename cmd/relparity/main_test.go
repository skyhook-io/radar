package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/k8score"
	"github.com/skyhook-io/radar/pkg/topology"
)

func TestRestrictedComparisonSkipsDisabledNamespaceLister(t *testing.T) {
	cache := &k8s.ResourceCache{ResourceCache: &k8score.ResourceCache{}}
	if cache.Namespaces() != nil {
		t.Fatal("Namespace listing should be disabled")
	}
	provider := k8s.NewTopologyResourceProvider(cache)
	namespaces, reason := comparisonNamespaces(provider.(topology.NamespaceProvider))
	if len(namespaces) != 0 || !strings.Contains(reason, "namespaces not available") {
		t.Fatalf("namespaces %v, skipped reason %q", namespaces, reason)
	}
	data, err := json.Marshal(report{RestrictedSkippedReason: reason})
	if err != nil || !strings.Contains(string(data), `"restrictedSkippedReason":"cannot enumerate namespaces:`) {
		t.Fatalf("report %s: %v", data, err)
	}
}
