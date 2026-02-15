package mcp

import (
	"context"
	"encoding/json"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"

	aicontext "github.com/skyhook-io/radar/internal/ai/context"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/topology"
)

func registerResources(server *mcp.Server) {
	server.AddResource(
		&mcp.Resource{
			URI:         "cluster://health",
			Name:        "Cluster Health",
			Description: "Cluster health summary including resource counts, problems, and warning events",
			MIMEType:    "application/json",
		},
		handleResourceHealth,
	)

	server.AddResource(
		&mcp.Resource{
			URI:         "cluster://topology",
			Name:        "Cluster Topology",
			Description: "Current topology graph showing relationships between Kubernetes resources",
			MIMEType:    "application/json",
		},
		handleResourceTopology,
	)

	server.AddResource(
		&mcp.Resource{
			URI:         "cluster://events",
			Name:        "Recent Events",
			Description: "Recent Kubernetes warning events, deduplicated and sorted by recency",
			MIMEType:    "application/json",
		},
		handleResourceEvents,
	)
}

func handleResourceHealth(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
	cache := k8s.GetResourceCache()
	if cache == nil {
		return textResource("cluster://health", `{"error":"not connected to cluster"}`), nil
	}

	dashboard := buildDashboard(ctx, cache, "")
	data, _ := json.Marshal(dashboard)

	return textResource("cluster://health", string(data)), nil
}

func handleResourceTopology(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
	opts := topology.DefaultBuildOptions()
	builder := topology.NewBuilder()
	topo, err := builder.Build(opts)
	if err != nil {
		return textResource("cluster://topology", `{"error":"`+err.Error()+`"}`), nil
	}

	data, _ := json.Marshal(topo)
	return textResource("cluster://topology", string(data)), nil
}

func handleResourceEvents(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
	cache := k8s.GetResourceCache()
	if cache == nil {
		return textResource("cluster://events", `{"error":"not connected to cluster"}`), nil
	}

	eventLister := cache.Events()
	if eventLister == nil {
		return textResource("cluster://events", `{"error":"insufficient permissions"}`), nil
	}

	events, err := eventLister.List(labels.Everything())
	if err != nil {
		return textResource("cluster://events", `{"error":"`+err.Error()+`"}`), nil
	}

	// Filter to warning events only
	var warnings []corev1.Event
	for _, e := range events {
		if e.Type == "Warning" {
			warnings = append(warnings, *e)
		}
	}

	deduplicated := aicontext.DeduplicateEvents(warnings)

	// Cap at 50 events for the resource
	if len(deduplicated) > 50 {
		deduplicated = deduplicated[:50]
	}

	data, _ := json.Marshal(deduplicated)
	return textResource("cluster://events", string(data)), nil
}

func textResource(uri, text string) *mcp.ReadResourceResult {
	return &mcp.ReadResourceResult{
		Contents: []*mcp.ResourceContents{
			{
				URI:      uri,
				MIMEType: "application/json",
				Text:     text,
			},
		},
	}
}
