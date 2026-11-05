package mcp

import (
	"context"
	"net/http"
	"net/url"
	"sort"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/servicehttp"
)

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
