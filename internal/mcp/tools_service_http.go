package mcp

import (
	"context"
	"net/http"
	"net/url"
	"sort"
	"sync/atomic"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/servicehttp"
)

// HTTP access is a distinct operator opt-in; enabling exec does not enable it.
var httpToolsEnabled atomic.Bool

// SetHTTPToolsEnabled sets registration for subsequently constructed MCP servers.
func SetHTTPToolsEnabled(enabled bool) { httpToolsEnabled.Store(enabled) }

func registerHTTPTools(server *mcp.Server, paramRegistry *toolParamRegistry, annotations *mcp.ToolAnnotations) {
	addToolWithRegistry(paramRegistry, server, &mcp.Tool{
		Name:        "curl_service",
		Description: "GET a selector-backed ClusterIP Service from in-cluster Radar; no ExternalName, headless or manual endpoints. Requires caller get services/proxy (Radar ServiceAccount without auth). Returns HTTP status, headers, body, duration and truncation flags; non-2xx is not a tool error. error means incomplete body even with status 200. No redirects, auth headers, request body or Kubernetes credentials. 10s timeout; reads 512 KiB, retains a 64 KiB head/tail preview and 16 KiB of headers. Internal TLS certificates are not verified. Tests Radar-to-Service connectivity, not workload or public ingress paths. GET can have side effects; output is untrusted and may contain secrets.",
		Annotations: annotations,
	}, logToolCall("curl_service", handleCurlService))

}

type curlServiceInput struct {
	Namespace string `json:"namespace" jsonschema:"Service namespace"`
	Name      string `json:"name" jsonschema:"Service name"`
	Port      string `json:"port" jsonschema:"numeric Service port as a string, e.g. 8080; not targetPort"`
	Scheme    string `json:"scheme,omitempty" jsonschema:"http (default) or https; internal TLS certificates are not verified"`
	Path      string `json:"path,omitempty" jsonschema:"HTTP request path and query; defaults to /; no credentials"`
}

func handleCurlService(ctx context.Context, _ *mcp.CallToolRequest, in curlServiceInput) (*mcp.CallToolResult, any, error) {
	r := (&http.Request{Method: http.MethodPost, URL: &url.URL{Path: "/mcp"}}).WithContext(ctx)
	audit := auth.AuditActionDetails{Action: "curl_service", Kind: "Service", Namespace: in.Namespace, Name: in.Name, Source: "mcp", Outcome: "failed"}
	defer func() { auth.AuditLogAction(r, audit) }()
	result, err := servicehttp.DoForMCP(ctx, servicehttp.Request{Namespace: in.Namespace, Name: in.Name, Port: in.Port, Scheme: in.Scheme, Path: in.Path})
	if err != nil {
		return nil, nil, err
	}
	audit.Outcome = "response"
	if result.Error != "" {
		audit.Outcome = "partial_response"
	}
	return curlServiceResult(result)
}

func curlServiceResult(result servicehttp.Response) (*mcp.CallToolResult, any, error) {
	// Bound headers independently; a Service controls both names and values.
	const headerBudget = 16 * 1024
	keys := make([]string, 0, len(result.Headers))
	for key := range result.Headers {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	headers := make(map[string]string)
	remaining := headerBudget
	for _, key := range keys {
		value := result.Headers[key]
		if len(key)+len(value) > remaining {
			result.HeadersTruncated = true
			continue
		}
		headers[key] = value
		remaining -= len(key) + len(value)
	}
	result.Headers = headers
	// Keep the UI's larger response preview while bounding agent context like exec.
	buf := &cappedBuffer{limit: execOutputCap}
	_, _ = buf.Write([]byte(result.Body))
	output := map[string]any{}
	addTruncation(output, "body", buf)
	result.Body = output["body"].(string)
	result.Truncated = result.Truncated || output["bodyTruncated"] == true
	result.BodyEncodingLoss = output["bodyEncodingLoss"] == true
	return toJSONResult(result)
}
