package mcp

import (
	"context"
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// requestScopedCancellationVersion is the first MCP protocol version on which
// one streamable HTTP POST carries a whole call, so the client closing that
// request is how it cancels the call. On older versions a disconnect is not a
// cancellation, and the client's notifications/cancelled arrives on a separate
// stateless request that Radar can't match to the call.
const requestScopedCancellationVersion = "2026-07-28"

type httpRequestContextKey struct{}

// withHTTPRequestContext keeps the HTTP request's context reachable from tool
// handlers. The SDK gives a handler a context that carries the request's
// values but never ends with it, so a tool that should stop when its caller
// leaves reads the request's own context from here.
func withHTTPRequestContext(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := context.WithValue(r.Context(), httpRequestContextKey{}, r.Context())
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// stopsWhenCallerLeaves ends handler's context when the HTTP request that
// carried the call ends, for clients on protocol 2026-07-28 or later. Calls
// from older clients and over stdio keep the context they had.
//
// Use it only on read-only tools. A write tool stopped part-way can leave a
// half-applied manifest or a half-drained node, and the caller that left
// would never see the result that says so.
func stopsWhenCallerLeaves[In any](
	handler func(context.Context, *mcp.CallToolRequest, In) (*mcp.CallToolResult, any, error),
) func(context.Context, *mcp.CallToolRequest, In) (*mcp.CallToolResult, any, error) {
	return func(ctx context.Context, req *mcp.CallToolRequest, input In) (*mcp.CallToolResult, any, error) {
		httpCtx, ok := ctx.Value(httpRequestContextKey{}).(context.Context)
		if !ok || req.ProtocolVersion() < requestScopedCancellationVersion {
			return handler(ctx, req, input)
		}
		ctx, cancel := context.WithCancel(ctx)
		defer cancel()
		stop := context.AfterFunc(httpCtx, cancel)
		defer stop()
		return handler(ctx, req, input)
	}
}
