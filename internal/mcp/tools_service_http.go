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
	result, err := servicehttp.Do(ctx, servicehttp.Request{Namespace: in.Namespace, Name: in.Name, Port: in.Port, Scheme: in.Scheme, Path: in.Path})
	if err != nil {
		return nil, nil, err
	}
	r := (&http.Request{Method: http.MethodPost, URL: &url.URL{Path: "/mcp"}}).WithContext(ctx)
	auth.AuditLog(r, in.Namespace, in.Name)
	return toJSONResult(result)
}
