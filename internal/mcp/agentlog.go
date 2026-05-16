package mcp

import (
	"context"
	"encoding/json"
	"log"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/skyhook-io/radar/internal/logsafe"
)

// agentLogFields is the fixed shape of the agent-context log line emitted
// after every MCP tool call. Field names are stable and parsed by downstream
// tooling.
//
// `Truncated`, `Omitted`, and `ContextTier` are reserved fields that future
// agent-context enrichment work will populate; today they are emitted as
// zero / false / "none" so the line shape stays stable across releases.
type agentLogFields struct {
	Tool        string
	DurationMS  int64
	Bytes       int
	EstTokens   int
	Truncated   bool
	Omitted     int
	ContextTier string // "none" | "basic" | "diagnostic"
	Kind        string
	Namespace   string
	Err         error
}

// emitAgentLog writes a single logfmt-style line summarizing one MCP tool
// call. Format is intentionally flat and parser-friendly:
//
//	level=info component=mcp tool=get_resource duration_ms=42 bytes=2156 \
//	  est_tokens=539 truncated=false omitted=0 context_tier=none kind=Pod ns=prod
func emitAgentLog(f agentLogFields) {
	level := "info"
	if f.Err != nil {
		level = "error"
	}
	tier := f.ContextTier
	if tier == "" {
		tier = "none"
	}
	log.Printf(
		"level=%s component=mcp tool=%s duration_ms=%d bytes=%d est_tokens=%d truncated=%t omitted=%d context_tier=%s kind=%s ns=%s",
		level, f.Tool, f.DurationMS, f.Bytes, f.EstTokens,
		f.Truncated, f.Omitted, tier, logsafe.Sanitize(f.Kind), logsafe.Sanitize(f.Namespace),
	)
}

// resultBytes computes the wire-payload size of a CallToolResult. We sum the
// text length of every TextContent (the only content type radar tools emit
// today) plus a marshaled view of any StructuredContent. JSON-marshaling the
// whole result would double-count, so we approximate the body size instead.
func resultBytes(result *mcp.CallToolResult) int {
	if result == nil {
		return 0
	}
	total := 0
	for _, c := range result.Content {
		switch tc := c.(type) {
		case *mcp.TextContent:
			total += len(tc.Text)
		default:
			// For non-text content types (image/audio/embedded), fall back to
			// JSON marshal length. Not exact but stable.
			if b, err := json.Marshal(c); err == nil {
				total += len(b)
			}
		}
	}
	if result.StructuredContent != nil {
		if b, err := json.Marshal(result.StructuredContent); err == nil {
			total += len(b)
		}
	}
	return total
}

// extractKindNamespace pulls "kind" and "namespace" (or "ns") fields out of
// the marshaled tool input. Tools that don't carry these fields produce
// empty strings, which is fine — the log line shape stays consistent.
//
// We go through json.Marshal so we get whatever the tool author's `json:` tag
// names are, rather than reflecting over field names directly.
func extractKindNamespace(input any) (kind string, ns string) {
	if input == nil {
		return "", ""
	}
	b, err := json.Marshal(input)
	if err != nil {
		return "", ""
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return "", ""
	}
	if v, ok := m["kind"].(string); ok {
		kind = v
	}
	if v, ok := m["namespace"].(string); ok {
		ns = v
	} else if v, ok := m["ns"].(string); ok {
		ns = v
	}
	return kind, ns
}

// logToolCall wraps an MCP tool handler with both the existing human-readable
// dev log AND a structured agent-log line. Every `mcp.AddTool` registration
// goes through this single wrap point so the log surface is uniform.
//
// The dev log preserves the colored, terminal-friendly behavior radar already
// had; the structured line is the machine-readable companion that downstream
// tooling parses for response-size and latency data.
func logToolCall[In any](
	name string,
	handler func(context.Context, *mcp.CallToolRequest, In) (*mcp.CallToolResult, any, error),
) func(context.Context, *mcp.CallToolRequest, In) (*mcp.CallToolResult, any, error) {
	return func(ctx context.Context, req *mcp.CallToolRequest, input In) (*mcp.CallToolResult, any, error) {
		args, _ := json.Marshal(input)
		log.Printf("\033[1;35m[MCP]\033[0m \033[1m%s\033[0m %s", name, string(args))

		start := time.Now()
		result, extra, err := handler(ctx, req, input)
		dur := time.Since(start)

		if err != nil {
			log.Printf("\033[1;35m[MCP]\033[0m \033[1m%s\033[0m \033[31mERROR\033[0m (%s) %v", name, dur.Round(time.Millisecond), err)
		} else {
			log.Printf("\033[1;35m[MCP]\033[0m \033[1m%s\033[0m \033[32mOK\033[0m (%s)", name, dur.Round(time.Millisecond))
		}

		bytes := resultBytes(result)
		kind, ns := extractKindNamespace(input)
		emitAgentLog(agentLogFields{
			Tool:        name,
			DurationMS:  dur.Milliseconds(),
			Bytes:       bytes,
			EstTokens:   logsafe.EstimateTokens(bytes),
			ContextTier: "none",
			Kind:        kind,
			Namespace:   ns,
			Err:         err,
		})

		return result, extra, err
	}
}
