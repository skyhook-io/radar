package mcp

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/skyhook-io/radar/pkg/investigation"
)

func TestApplyHandlerExcludesRuntimeCollection(t *testing.T) {
	enableExecToolsForTest(t)
	httpServer := httptest.NewServer(NewApplyHandler())
	defer httpServer.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "apply-boundary-test", Version: "test"}, nil)
	session, err := client.Connect(ctx, &mcpsdk.StreamableClientTransport{Endpoint: httpServer.URL}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	if got := session.InitializeResult().Instructions; got != investigation.GeneralGuidance(true) {
		t.Fatalf("restricted catalog lost server guidance: got %q", got)
	}
	names := map[string]bool{}
	cursor := ""
	for {
		result, err := session.ListTools(ctx, &mcpsdk.ListToolsParams{Cursor: cursor})
		if err != nil {
			t.Fatal(err)
		}
		for _, tool := range result.Tools {
			names[tool.Name] = true
		}
		cursor = result.NextCursor
		if cursor == "" {
			break
		}
	}
	for _, name := range []string{"collect_application_evidence", "exec_pod", "delete_resource"} {
		if names[name] {
			t.Fatalf("%s listed on apply handler", name)
		}
	}
	for _, tool := range listRegisteredToolsWith(t, true) {
		if tool.Name != "collect_application_evidence" && tool.Name != "exec_pod" && tool.Name != "delete_resource" && !names[tool.Name] {
			t.Errorf("existing tool %s missing from apply handler", tool.Name)
		}
	}
	result, err := session.CallTool(ctx, &mcpsdk.CallToolParams{Name: "collect_application_evidence", Arguments: map[string]any{"application": "vault", "namespace": "lab", "pod": "vault", "confirm_network_access": true}})
	if err == nil || !strings.Contains(err.Error(), `unknown tool "collect_application_evidence"`) {
		t.Fatalf("excluded tool accepted: %+v", result)
	}
}
