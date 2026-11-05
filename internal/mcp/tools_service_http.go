package mcp

import (
	"context"
	"net/http"
	"net/url"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/servicehttp"
)

type curlServiceInput struct {
	Namespace string `json:"namespace" jsonschema:"Service namespace"`
	Name      string `json:"name" jsonschema:"Service name"`
	Port      string `json:"port" jsonschema:"numeric Service port, not targetPort"`
	Scheme    string `json:"scheme,omitempty" jsonschema:"http (default) or https; internal TLS certificates are not verified"`
	Path      string `json:"path,omitempty" jsonschema:"HTTP request path and query; defaults to /; no credentials"`
}

func handleCurlService(ctx context.Context, _ *mcp.CallToolRequest, in curlServiceInput) (*mcp.CallToolResult, any, error) {
	r := (&http.Request{Method: http.MethodPost, URL: &url.URL{Path: "/mcp"}}).WithContext(ctx)
	audit := auth.AuditActionDetails{Action: "curl_service", Kind: "Service", Namespace: in.Namespace, Name: in.Name, Source: "mcp", Outcome: "failed"}
	defer func() { auth.AuditLogAction(r, audit) }()
	result, err := servicehttp.Do(ctx, servicehttp.Request{Namespace: in.Namespace, Name: in.Name, Port: in.Port, Scheme: in.Scheme, Path: in.Path})
	if err != nil {
		return nil, nil, err
	}
	audit.Outcome = "response"
	if result.Error != "" {
		audit.Outcome = "partial_response"
	}
	// Keep the UI's larger response preview while bounding agent context like exec.
	buf := &cappedBuffer{limit: execOutputCap}
	_, _ = buf.Write([]byte(result.Body))
	output := map[string]any{}
	addTruncation(output, "body", buf)
	result.Body = output["body"].(string)
	result.Truncated = result.Truncated || output["bodyTruncated"] == true
	return toJSONResult(result)
}
