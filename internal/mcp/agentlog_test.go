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

// TestLogToolCallSanitizesUserControlledFields verifies that BOTH classes
// of log injection are neutralized:
//
//  1. Multi-line injection — `\n`, `\r`, control chars: would otherwise
//     forge a separate log "event" that scrapers parse as its own entry.
//  2. Same-line logfmt field injection — space and `=`: would otherwise
//     introduce new key=value tokens on the SAME line that scrapers
//     parse as legitimate fields (e.g. forging `level=error`).
//
// A tool input of `{"kind": "Pod level=error fake=line"}` is enough to
// inject same-line fields; control chars aren't required.
func TestLogToolCallSanitizesUserControlledFields(t *testing.T) {
	var buf bytes.Buffer
	defer log.SetOutput(log.Writer())
	log.SetOutput(&buf)

	type input struct {
		Kind      string `json:"kind"`
		Namespace string `json:"namespace"`
	}

	wrapped := logToolCall("inject_tool", func(_ context.Context, _ *mcp.CallToolRequest, _ input) (*mcp.CallToolResult, any, error) {
		return &mcp.CallToolResult{}, nil, nil
	})

	_, _, _ = wrapped(context.Background(), &mcp.CallToolRequest{}, input{
		// Newline (multi-line attack) + same-line attack in one payload.
		Kind:      "Pod\nlevel=error fake=line",
		Namespace: "prod\rfake=ns",
	})

	full := buf.String()

	// Isolate the structured log line — the dev log (colored `[MCP]` lines)
	// legitimately contains the raw JSON-marshaled args including
	// `\nlevel=error fake=line`, but those are inside JSON quotes and don't
	// pollute logfmt parsers. The structured line is what scrapers consume.
	var structured string
	structuredCount := 0
	for _, l := range strings.Split(full, "\n") {
		if strings.Contains(l, "component=mcp tool=inject_tool") {
			structured = l
			structuredCount++
		}
	}
	if structuredCount != 1 {
		t.Fatalf("expected exactly 1 structured log line, found %d (multi-line injection succeeded?)\nfull output:\n%s", structuredCount, full)
	}

	// Multi-line attack: structured line must not be broken across newlines.
	// (Already guaranteed by structuredCount == 1 + log.Printf semantics.)

	// Same-line attack: forged "level=error" / "fake=line" / "fake=ns" must
	// NOT appear as standalone kv tokens on the structured line.
	for _, forged := range []string{" level=error", " fake=line", " fake=ns"} {
		if strings.Contains(structured, forged) {
			t.Errorf("same-line logfmt injection reached the structured line (substring %q present)\nstructured: %s", forged, structured)
		}
	}

	// Sanitized form: spaces, `=`, and newlines all collapse to '_'. Values
	// stay visible so operators still see what was attempted.
	if !strings.Contains(structured, "kind=Pod_level_error_fake_line") {
		t.Errorf("expected sanitized kind value with underscore replacement\nstructured: %s", structured)
	}
	if !strings.Contains(structured, "ns=prod_fake_ns") {
		t.Errorf("expected sanitized ns value with underscore replacement\nstructured: %s", structured)
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
