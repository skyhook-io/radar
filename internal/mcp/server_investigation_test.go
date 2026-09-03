package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/skyhook-io/radar/internal/investigationrefs"
)

func TestAnnotateInvestigationEvidenceReferencePreservesProducerPayload(t *testing.T) {
	ref := "ev_" + strings.Repeat("a", 26) + "_" + strings.Repeat("b", 26)
	for _, payload := range []string{
		`{"kind":"Pod"}`,
		`[{"kind":"Pod"}]`,
		"plain text result",
	} {
		t.Run(payload[:min(len(payload), 12)], func(t *testing.T) {
			result := &mcpsdk.CallToolResult{Content: []mcpsdk.Content{
				&mcpsdk.TextContent{Text: payload},
			}}
			annotateInvestigationEvidenceReference(result, ref)
			if len(result.Content) != 2 {
				t.Fatalf("content blocks = %d, want marker + payload", len(result.Content))
			}
			marker, ok := result.Content[0].(*mcpsdk.TextContent)
			if !ok || marker.Text != investigationEvidenceMarkerPrefix+ref+investigationEvidenceMarkerSuffix {
				t.Fatalf("marker = %#v", result.Content[0])
			}
			original, ok := result.Content[1].(*mcpsdk.TextContent)
			if !ok || original.Text != payload {
				t.Fatalf("producer payload changed: %#v", result.Content[1])
			}
		})
	}
}

func TestInvestigationEvidenceReferenceMiddlewareScopesSuccessfulToolResults(t *testing.T) {
	scope := strings.Repeat("a", 26)
	refs := investigationrefs.NewRegistry()
	lease, err := refs.Begin(scope)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.WithValue(context.Background(), investigationEvidenceScopeKey{}, scope)
	success := &mcpsdk.CallToolResult{Content: []mcpsdk.Content{
		&mcpsdk.TextContent{Text: `[]`},
	}}
	wrapped := investigationEvidenceReferenceMiddleware(refs)(
		func(context.Context, string, mcpsdk.Request) (mcpsdk.Result, error) {
			return success, nil
		},
	)
	result, err := wrapped(ctx, "tools/call", nil)
	if err != nil {
		t.Fatal(err)
	}
	got := result.(*mcpsdk.CallToolResult)
	marker := got.Content[0].(*mcpsdk.TextContent).Text
	prefix := investigationEvidenceMarkerPrefix + "ev_" + scope + "_"
	if !strings.HasPrefix(marker, prefix) || !strings.HasSuffix(marker, investigationEvidenceMarkerSuffix) {
		t.Fatalf("marker = %q, want scoped prefix %q", marker, prefix)
	}
	if got.Content[1].(*mcpsdk.TextContent).Text != `[]` {
		t.Fatal("middleware changed the producer result")
	}
	ref := strings.TrimSuffix(strings.TrimPrefix(marker, investigationEvidenceMarkerPrefix), investigationEvidenceMarkerSuffix)
	if payload := lease.Close()[ref]; payload != `[]` {
		t.Fatalf("issued payload = %q, want exact producer text", payload)
	}
}

func TestInvestigationEvidenceReferenceMiddlewareMarksToolErrorsForProvenance(t *testing.T) {
	scope := strings.Repeat("b", 26)
	refs := investigationrefs.NewRegistry()
	lease, err := refs.Begin(scope)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.WithValue(context.Background(), investigationEvidenceScopeKey{}, scope)
	toolError := &mcpsdk.CallToolResult{
		Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: "permission denied"}},
		IsError: true,
	}
	wrapped := investigationEvidenceReferenceMiddleware(refs)(
		func(context.Context, string, mcpsdk.Request) (mcpsdk.Result, error) {
			return toolError, nil
		},
	)
	if _, err := wrapped(ctx, "tools/call", nil); err != nil {
		t.Fatal(err)
	}
	if len(toolError.Content) != 2 {
		t.Fatalf("content blocks = %d, want marker + error payload", len(toolError.Content))
	}
	marker := toolError.Content[0].(*mcpsdk.TextContent).Text
	ref := strings.TrimSuffix(strings.TrimPrefix(marker, investigationEvidenceMarkerPrefix), investigationEvidenceMarkerSuffix)
	if payload := lease.Close()[ref]; payload != "permission denied" {
		t.Fatalf("issued error payload = %q, want exact producer text", payload)
	}
}

func TestInvestigationEvidenceReferenceMiddlewareFailsClosed(t *testing.T) {
	scope := strings.Repeat("a", 26)
	validCtx := context.WithValue(context.Background(), investigationEvidenceScopeKey{}, scope)
	sentinelErr := errors.New("transport failed")
	tests := []struct {
		name        string
		ctx         context.Context
		method      string
		err         error
		activeScope bool
	}{
		{name: "missing scope", ctx: context.Background(), method: "tools/call", activeScope: true},
		{name: "inactive scope", ctx: validCtx, method: "tools/call"},
		{name: "other method", ctx: validCtx, method: "resources/read", activeScope: true},
		{name: "protocol error", ctx: validCtx, method: "tools/call", err: sentinelErr, activeScope: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			refs := investigationrefs.NewRegistry()
			var lease *investigationrefs.Scope
			if test.activeScope {
				var err error
				lease, err = refs.Begin(scope)
				if err != nil {
					t.Fatal(err)
				}
			}
			result := &mcpsdk.CallToolResult{
				Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: "payload"}},
			}
			wrapped := investigationEvidenceReferenceMiddleware(refs)(
				func(context.Context, string, mcpsdk.Request) (mcpsdk.Result, error) {
					return result, test.err
				},
			)
			_, err := wrapped(test.ctx, test.method, nil)
			if !errors.Is(err, test.err) {
				t.Fatalf("error = %v, want %v", err, test.err)
			}
			if len(result.Content) != 1 || result.Content[0].(*mcpsdk.TextContent).Text != "payload" {
				t.Fatalf("unexpected marker: %#v", result.Content)
			}
			if records := lease.Close(); len(records) != 0 {
				t.Fatalf("failed-closed call issued records: %v", records)
			}
		})
	}
}

func TestInvestigationHandlerRequiresTurnScope(t *testing.T) {
	refs := investigationrefs.NewRegistry()
	handler := NewInvestigationHandler(refs)
	for _, target := range []string{
		"http://radar.test/mcp-investigation",
		"http://radar.test/mcp-investigation?scope=not-a-scope",
	} {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, target, nil))
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("%s status = %d, want 400", target, recorder.Code)
		}
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(
		http.MethodPost,
		"http://radar.test/mcp-investigation?scope="+strings.Repeat("a", 26),
		nil,
	))
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("inactive valid scope status = %d, want 403", recorder.Code)
	}
}

func TestInvestigationHandlerAnnotatesRealToolCallWithoutChangingPublicContract(t *testing.T) {
	const payload = `{"kind":"Pod","status":"Running"}`
	scope := strings.Repeat("a", 26)
	refs := investigationrefs.NewRegistry()
	lease, err := refs.Begin(scope)
	if err != nil {
		t.Fatal(err)
	}

	newFixtureServer := func() *mcpsdk.Server {
		server := mcpsdk.NewServer(
			&mcpsdk.Implementation{Name: "radar-investigation-test", Version: "test"},
			nil,
		)
		server.AddTool(&mcpsdk.Tool{
			Name:        "fixture_read",
			InputSchema: json.RawMessage(`{"type":"object"}`),
		}, func(context.Context, *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
			return &mcpsdk.CallToolResult{Content: []mcpsdk.Content{
				&mcpsdk.TextContent{Text: payload},
			}}, nil
		})
		return server
	}

	mux := http.NewServeMux()
	mux.Handle("/mcp-readonly", handlerForServer(newFixtureServer()))
	mux.Handle("/mcp-investigation", investigationHandlerForServer(newFixtureServer(), refs))
	httpServer := httptest.NewServer(mux)
	defer httpServer.Close()

	callFixture := func(endpoint string) *mcpsdk.CallToolResult {
		t.Helper()
		client := mcpsdk.NewClient(
			&mcpsdk.Implementation{Name: "radar-investigation-test-client", Version: "test"},
			nil,
		)
		session, err := client.Connect(
			context.Background(),
			&mcpsdk.StreamableClientTransport{Endpoint: endpoint},
			nil,
		)
		if err != nil {
			t.Fatalf("initialize %s: %v", endpoint, err)
		}
		defer session.Close()
		result, err := session.CallTool(context.Background(), &mcpsdk.CallToolParams{Name: "fixture_read"})
		if err != nil {
			t.Fatalf("CallTool %s: %v", endpoint, err)
		}
		return result
	}

	publicResult := callFixture(httpServer.URL + "/mcp-readonly")
	if len(publicResult.Content) != 1 {
		t.Fatalf("public content blocks = %d, want producer payload only", len(publicResult.Content))
	}
	publicText, ok := publicResult.Content[0].(*mcpsdk.TextContent)
	if !ok || publicText.Text != payload {
		t.Fatalf("public producer payload = %#v, want %q", publicResult.Content[0], payload)
	}

	privateResult := callFixture(httpServer.URL + "/mcp-investigation?scope=" + scope)
	if len(privateResult.Content) != 2 {
		t.Fatalf("private content blocks = %d, want marker + producer payload", len(privateResult.Content))
	}
	marker, ok := privateResult.Content[0].(*mcpsdk.TextContent)
	if !ok {
		t.Fatalf("private marker type = %T, want TextContent", privateResult.Content[0])
	}
	wantPrefix := investigationEvidenceMarkerPrefix + "ev_" + scope + "_"
	if !strings.HasPrefix(marker.Text, wantPrefix) || !strings.HasSuffix(marker.Text, investigationEvidenceMarkerSuffix) {
		t.Fatalf("private marker = %q, want scoped prefix %q", marker.Text, wantPrefix)
	}
	ref := strings.TrimSuffix(strings.TrimPrefix(marker.Text, investigationEvidenceMarkerPrefix), investigationEvidenceMarkerSuffix)
	privateText, ok := privateResult.Content[1].(*mcpsdk.TextContent)
	if !ok || privateText.Text != payload {
		t.Fatalf("private producer payload = %#v, want unchanged %q", privateResult.Content[1], payload)
	}
	if issuedPayload := lease.Close()[ref]; issuedPayload != payload {
		t.Fatalf("issued payload = %q, want %q", issuedPayload, payload)
	}
}
