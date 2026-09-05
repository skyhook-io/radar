package mcp

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/skyhook-io/radar/pkg/topology"
)

func TestBuildTopologySummaryPreservesCoverageMetadata(t *testing.T) {
	topo := &topology.Topology{
		Nodes:                   []topology.Node{},
		Edges:                   []topology.Edge{},
		Warnings:                []string{"Cluster too large for all-namespace topology."},
		LargeCluster:            true,
		HiddenKinds:             []string{"ConfigMap", "PersistentVolumeClaim"},
		RequiresNamespaceFilter: true,
		CRDDiscoveryStatus:      "discovering",
		EstimatedNodes:          2400,
		SummaryMode:             true,
	}

	got := buildTopologySummary(topo)
	if !slices.Equal(got.Warnings, topo.Warnings) {
		t.Fatalf("warnings = %v, want %v", got.Warnings, topo.Warnings)
	}
	if !got.LargeCluster || !got.RequiresNamespaceFilter || !got.SummaryMode {
		t.Fatalf("boolean coverage metadata was dropped: %+v", got)
	}
	if !slices.Equal(got.HiddenKinds, topo.HiddenKinds) {
		t.Fatalf("hiddenKinds = %v, want %v", got.HiddenKinds, topo.HiddenKinds)
	}
	if got.CRDDiscoveryStatus != topo.CRDDiscoveryStatus {
		t.Fatalf("crdDiscoveryStatus = %q, want %q", got.CRDDiscoveryStatus, topo.CRDDiscoveryStatus)
	}
	if got.EstimatedNodes != topo.EstimatedNodes {
		t.Fatalf("estimatedNodes = %d, want %d", got.EstimatedNodes, topo.EstimatedNodes)
	}
	if got.Namespaces == nil {
		t.Fatal("empty summary namespaces must be [] rather than null")
	}

	payload, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal summary: %v", err)
	}
	if !strings.Contains(string(payload), `"namespaces":[]`) {
		t.Fatalf("empty summary namespaces did not marshal as an array: %s", payload)
	}
	var wire map[string]json.RawMessage
	if err := json.Unmarshal(payload, &wire); err != nil {
		t.Fatalf("unmarshal summary wire payload: %v", err)
	}
	for _, field := range []string{
		"warnings",
		"largeCluster",
		"hiddenKinds",
		"requiresNamespaceFilter",
		"crdDiscoveryStatus",
		"estimatedNodes",
		"summaryMode",
	} {
		if _, ok := wire[field]; !ok {
			t.Errorf("summary wire payload dropped %q: %s", field, payload)
		}
	}
}
