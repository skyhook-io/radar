package mcp

import (
	"encoding/json"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/skyhook-io/radar/internal/servicehttp"
	"strings"
	"testing"
)

func TestCurlServiceResultBoundsHeadersAndBody(t *testing.T) {
	result, _, err := curlServiceResult(servicehttp.Response{Status: 503, Headers: map[string]string{"X-Large": strings.Repeat("x", 2<<20), "Content-Type": "text/plain"}, Body: strings.Repeat("z", 512<<10), BodyBytes: 512 << 10})
	if err != nil {
		t.Fatal(err)
	}
	encoded := []byte(result.Content[0].(*mcp.TextContent).Text)
	var got servicehttp.Response
	if err := json.Unmarshal(encoded, &got); err != nil {
		t.Fatal(err)
	}
	if got.Status != 503 || !got.Truncated || !got.HeadersTruncated || got.Headers["X-Large"] != "" || got.Headers["Content-Type"] != "text/plain" || len(got.Body) > execOutputCap || got.BodyBytes != 512<<10 {
		t.Fatalf("bad bounded response: %d bytes, truncated=%v", len(encoded), got.Truncated)
	}
}
func TestCurlServiceResultMarksInvalidUTF8(t *testing.T) {
	result, _, err := curlServiceResult(servicehttp.Response{Body: string([]byte{0xff, 'x'}), BodyBytes: 2})
	if err != nil {
		t.Fatal(err)
	}
	encoded := []byte(result.Content[0].(*mcp.TextContent).Text)
	var got servicehttp.Response
	json.Unmarshal(encoded, &got)
	if !got.BodyEncodingLoss || got.BodyBytes != 2 {
		t.Fatalf("lost binary metadata: %s", encoded)
	}
}

func TestCurlServiceHeaderTruncationDoesNotMarkCompleteBody(t *testing.T) {
	result, _, err := curlServiceResult(servicehttp.Response{Body: "complete", Headers: map[string]string{"X-Large": strings.Repeat("x", 32<<10)}})
	if err != nil {
		t.Fatal(err)
	}
	var got servicehttp.Response
	json.Unmarshal([]byte(result.Content[0].(*mcp.TextContent).Text), &got)
	if !got.HeadersTruncated || got.Truncated || got.Body != "complete" {
		t.Fatalf("ambiguous truncation: %+v", got)
	}
}
