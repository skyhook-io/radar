package mcp

import (
	"context"
	"encoding/json"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func registerResources(server *mcp.Server) {
	server.AddResource(
		&mcp.Resource{
			URI:         "cluster://health",
			Name:        "Cluster Health",
			Description: "Cluster health summary: resource counts, problems, warning events and Helm status, as get_dashboard with no namespace. With authentication, requires cluster-wide list pods permission. If you lack that permission, call get_dashboard with an accessible namespace",
			MIMEType:    "application/json",
		},
		handleResourceHealth,
	)

	server.AddResource(
		&mcp.Resource{
			URI:         "cluster://topology",
			Name:        "Cluster Topology",
			Description: "Topology graph of the cluster's resources and how they connect, the same as get_topology with no namespace. For a caller limited to some namespaces it covers only those, and partialScope plus scopeNamespaces say which",
			MIMEType:    "application/json",
		},
		handleResourceTopology,
	)

	server.AddResource(
		&mcp.Resource{
			URI:         "cluster://events",
			Name:        "Recent Events",
			Description: "Recent Warning events, deduplicated and sorted by recency, as {\"events\": [...]}: the same as get_events with type=Warning and limit=50. For a caller limited to some namespaces it covers only those, and partialScope plus scopeNamespaces say which. narrowHint appears when more than 50 groups exist; call get_events to narrow by namespace, kind or name",
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

// The cluster:// resources take no input, so each one answers through the
// tool that asks the same question with no namespace. That keeps one set of
// access rules: a caller who reads only some namespaces gets the same answer,
// marker or refusal from the resource as from the tool.

func handleResourceHealth(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
	result, _, err := handleGetDashboard(ctx, nil, dashboardInput{})
	return resourceFromTool("cluster://health", result, err), nil
}

func handleResourceTopology(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
	result, _, err := handleGetTopology(ctx, nil, topologyInput{})
	return resourceFromTool("cluster://topology", result, err), nil
}

// clusterEventsResourceLimit is how many deduplicated Warning groups
// cluster://events returns; get_events caps its own limit at 100.
const clusterEventsResourceLimit = 50

func handleResourceEvents(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
	result, _, err := handleGetEvents(ctx, nil, eventsInput{Type: "Warning", Limit: clusterEventsResourceLimit})
	return resourceFromTool("cluster://events", result, err), nil
}

// resourceFromTool turns a tool handler's answer into resource contents. A
// coded read error keeps the body tools send (error, error_code, next), so a
// client tells "Radar couldn't check your access" from a denial the same way
// on both; any other error becomes {"error": "..."}.
func resourceFromTool(uri string, result *mcp.CallToolResult, err error) *mcp.ReadResourceResult {
	if err != nil {
		if structured, ok := readErrorResult(err); ok {
			if text, ok := singleText(structured); ok {
				return textResource(uri, text)
			}
		}
		return jsonErrorResource(uri, err.Error())
	}
	text, ok := singleText(result)
	if !ok {
		return jsonErrorResource(uri, "internal error")
	}
	return textResource(uri, text)
}

func singleText(result *mcp.CallToolResult) (string, bool) {
	if result == nil || len(result.Content) != 1 {
		return "", false
	}
	content, ok := result.Content[0].(*mcp.TextContent)
	if !ok {
		return "", false
	}
	return content.Text, true
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
