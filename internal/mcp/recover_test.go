package mcp

import (
	"context"
	"strings"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// A panicking tool fails its own call and leaves Radar's MCP server answering.
func TestToolPanicFailsTheCallNotTheServer(t *testing.T) {
	ctx := context.Background()
	server := newServer(false, false, "")
	calls := 0
	server.AddTool(&mcpsdk.Tool{Name: "boom", InputSchema: map[string]any{"type": "object"}}, func(context.Context, *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
		calls++
		var m map[string]any
		_ = m["pods"].([]map[string]any)
		return nil, nil
	})
	server.AddTool(&mcpsdk.Tool{Name: "ok", InputSchema: map[string]any{"type": "object"}}, func(context.Context, *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
		return &mcpsdk.CallToolResult{Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: "fine"}}}, nil
	})
	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "test", Version: "test"}, nil)
	st, ct := mcpsdk.NewInMemoryTransports()
	ss, err := server.Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	cs, err := client.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close(); _ = ss.Wait() })
	for i := range 2 {
		res, err := cs.CallTool(ctx, &mcpsdk.CallToolParams{Name: "boom"})
		if err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
		text := ""
		if len(res.Content) == 1 {
			if c, ok := res.Content[0].(*mcpsdk.TextContent); ok {
				text = c.Text
			}
		}
		if !res.IsError || !strings.Contains(text, "internal error") {
			t.Fatalf("call %d: result = %+v", i, res)
		}
	}
	if calls != 2 {
		t.Fatalf("tool ran %d times, want 2", calls)
	}
	if res, err := cs.CallTool(ctx, &mcpsdk.CallToolParams{Name: "ok"}); err != nil || res.IsError {
		t.Fatalf("a normal call after the panics: %+v, %v", res, err)
	}
}
