package mcp

import (
	"bytes"
	"context"
	"errors"
	"log"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestEstimateTokens(t *testing.T) {
	// English JSON heuristic: 4 chars/token. Cheap and deterministic.
	cases := []struct {
		bytes int
		want  int
	}{
		{0, 0},
		{1, 0},
		{4, 1},
		{2156, 539},
	}
	for _, c := range cases {
		if got := estimateTokens(c.bytes); got != c.want {
			t.Errorf("estimateTokens(%d) = %d, want %d", c.bytes, got, c.want)
		}
	}
}

func TestResultBytesTextContent(t *testing.T) {
	res := &mcp.CallToolResult{
		Content: []mcp.Content{
			&mcp.TextContent{Text: "hello"},
			&mcp.TextContent{Text: "world!"},
		},
	}
	got := resultBytes(res)
	want := len("hello") + len("world!")
	if got != want {
		t.Errorf("resultBytes = %d, want %d", got, want)
	}
}

func TestResultBytesNil(t *testing.T) {
	if got := resultBytes(nil); got != 0 {
		t.Errorf("resultBytes(nil) = %d, want 0", got)
	}
}

func TestExtractKindNamespace(t *testing.T) {
	type input struct {
		Kind      string `json:"kind"`
		Namespace string `json:"namespace"`
		Name      string `json:"name"`
	}
	in := input{Kind: "Pod", Namespace: "prod", Name: "x"}
	kind, ns := extractKindNamespace(in)
	if kind != "Pod" || ns != "prod" {
		t.Errorf("extractKindNamespace = %q,%q; want Pod,prod", kind, ns)
	}
}

func TestExtractKindNamespaceNsField(t *testing.T) {
	type input struct {
		Kind string `json:"kind"`
		NS   string `json:"ns"`
	}
	kind, ns := extractKindNamespace(input{Kind: "Pod", NS: "kube-system"})
	if kind != "Pod" || ns != "kube-system" {
		t.Errorf("extractKindNamespace = %q,%q; want Pod,kube-system", kind, ns)
	}
}

func TestExtractKindNamespaceEmpty(t *testing.T) {
	type input struct {
		Other string `json:"other"`
	}
	kind, ns := extractKindNamespace(input{Other: "foo"})
	if kind != "" || ns != "" {
		t.Errorf("extractKindNamespace = %q,%q; want empty", kind, ns)
	}
}

// TestWrapToolCallEmitsStructuredLog verifies the wrap point produces the
// exact agent-log log line shape that downstream scrapers parse.
// Field renames or reordering must be caught here, not in production.
func TestWrapToolCallEmitsStructuredLog(t *testing.T) {
	var buf bytes.Buffer
	defer log.SetOutput(log.Writer())
	log.SetOutput(&buf)

	type input struct {
		Kind      string `json:"kind"`
		Namespace string `json:"namespace"`
		Name      string `json:"name"`
	}

	wrapped := logToolCall("test_tool", func(_ context.Context, _ *mcp.CallToolRequest, _ input) (*mcp.CallToolResult, any, error) {
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: "hello"}},
		}, nil, nil
	})

	_, _, err := wrapped(context.Background(), &mcp.CallToolRequest{}, input{Kind: "Pod", Namespace: "prod", Name: "x"})
	if err != nil {
		t.Fatalf("wrapped handler returned unexpected error: %v", err)
	}

	line := buf.String()
	wants := []string{
		"level=info",
		"component=mcp",
		"tool=test_tool",
		"truncated=false",
		"omitted=0",
		"context_tier=none",
		"kind=Pod",
		"ns=prod",
		"bytes=5",
		"est_tokens=1",
	}
	for _, w := range wants {
		if !strings.Contains(line, w) {
			t.Errorf("log output missing %q\nfull output:\n%s", w, line)
		}
	}
}

// TestWrapToolCallErrorChangesLevel verifies that a handler returning an error
// flips the structured line's level field from info to error, so scrapers can
// distinguish failures without parsing the colored dev log.
func TestWrapToolCallErrorChangesLevel(t *testing.T) {
	var buf bytes.Buffer
	defer log.SetOutput(log.Writer())
	log.SetOutput(&buf)

	type input struct{}

	wrapped := logToolCall("err_tool", func(_ context.Context, _ *mcp.CallToolRequest, _ input) (*mcp.CallToolResult, any, error) {
		return nil, nil, errors.New("boom")
	})

	_, _, _ = wrapped(context.Background(), &mcp.CallToolRequest{}, input{})

	line := buf.String()
	// Must find the structured line specifically (the dev log also includes
	// "ERROR" but as a colored prefix, not the level= field).
	if !strings.Contains(line, "level=error component=mcp tool=err_tool") {
		t.Errorf("expected level=error on the structured line for failed tool call\nfull output:\n%s", line)
	}
}
