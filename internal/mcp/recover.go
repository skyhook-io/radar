package mcp

import (
	"context"
	"log"
	"runtime/debug"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// recoverMiddleware turns a panic in a request handler into a failed request.
// The SDK runs handlers in their own goroutines without recovering, so a panic
// in any tool would otherwise end the whole Radar process; the REST routes
// recover theirs. A tool call reports the failure as a tool error, which is
// what an agent can act on; other methods return a JSON-RPC internal error.
// Goroutines a handler starts itself are outside this and must recover their own.
func recoverMiddleware(next mcpsdk.MethodHandler) mcpsdk.MethodHandler {
	return func(ctx context.Context, method string, req mcpsdk.Request) (result mcpsdk.Result, err error) {
		defer func() {
			rec := recover()
			if rec == nil {
				return
			}
			name := method
			if call, ok := req.(*mcpsdk.CallToolRequest); ok && call.Params != nil {
				name = method + " " + call.Params.Name
			}
			log.Printf("[mcp] panic in %s: %v\n%s", name, rec, debug.Stack())
			if method == "tools/call" {
				result = &mcpsdk.CallToolResult{
					IsError: true,
					Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: "Radar hit an internal error answering this tool call; details are in Radar's log. If the call could change resources, check their state before retrying."}},
				}
				err = nil
				return
			}
			result, err = nil, &jsonrpc.Error{Code: jsonrpc.CodeInternalError, Message: "internal error handling " + method}
		}()
		return next(ctx, method, req)
	}
}
