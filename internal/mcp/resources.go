package mcp

import (
	"context"
	"encoding/json"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"

	"github.com/skyhook-io/radar/internal/k8s"
	aicontext "github.com/skyhook-io/radar/pkg/ai/context"
	topology "github.com/skyhook-io/radar/pkg/topology"
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

// jsonErrorResource builds a structured `{"error": "..."}` payload. Avoids
// concatenating err.Error() into a JSON string — admission webhook errors
// often contain quotes that would break a string-concat response.
func jsonErrorResource(uri, message string) *mcp.ReadResourceResult {
	body, err := json.Marshal(struct {
		Error string `json:"error"`
	}{Error: message})
	if err != nil {
		body = []byte(`{"error":"internal error"}`)
	}
	return textResource(uri, string(body))
}

// requireClusterAdminAccess refuses MCP resource reads (cluster://*) unless
// the caller can list namespaces cluster-wide — these resources have no
// namespace input, so anything less risks leaking cluster-scoped objects
// (events, nodes, topology edges) the user has no right to see. Restricted
// users are redirected to the equivalent tools, which take an explicit
// namespace and apply per-user filtering.
func requireClusterAdminAccess(ctx context.Context, uri string) (*mcp.ReadResourceResult, bool) {
	if !canReadClusterScopedKind(ctx, "namespaces", "", "list") {
		return jsonErrorResource(uri, "forbidden: this resource requires cluster-wide list-namespaces permission; use the equivalent MCP tool with an explicit namespace"), true
	}
	return nil, false
}

func handleResourceHealth(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
	if blocked, ok := requireClusterAdminAccess(ctx, "cluster://health"); ok {
		return blocked, nil
	}
	cache := k8s.GetResourceCache()
	if cache == nil {
		return jsonErrorResource("cluster://health", "not connected to cluster"), nil
	}

	dashboard := buildDashboard(ctx, cache, "", canReadClusterScopedKind(ctx, "nodes", "", "list"), canReadClusterScopedKind(ctx, "namespaces", "", "list"))
	data, _ := json.Marshal(dashboard)

	return textResource("cluster://health", string(data)), nil
}

func handleResourceTopology(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
	if blocked, ok := requireClusterAdminAccess(ctx, "cluster://topology"); ok {
		return blocked, nil
	}
	opts := topology.DefaultBuildOptions()
	builder := topology.NewBuilder(k8s.NewTopologyResourceProvider(k8s.GetResourceCache())).WithDynamic(k8s.NewTopologyDynamicProvider(k8s.GetDynamicResourceCache(), k8s.GetResourceDiscovery()))
	topo, err := builder.Build(opts)
	if err != nil {
		return jsonErrorResource("cluster://topology", err.Error()), nil
	}

	data, _ := json.Marshal(topo)
	return textResource("cluster://topology", string(data)), nil
}

func handleResourceEvents(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
	if blocked, ok := requireClusterAdminAccess(ctx, "cluster://events"); ok {
		return blocked, nil
	}
	cache := k8s.GetResourceCache()
	if cache == nil {
		return jsonErrorResource("cluster://events", "not connected to cluster"), nil
	}

	eventLister := cache.Events()
	if eventLister == nil {
		return jsonErrorResource("cluster://events", "insufficient permissions"), nil
	}

	events, err := eventLister.List(labels.Everything())
	if err != nil {
		return jsonErrorResource("cluster://events", err.Error()), nil
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
