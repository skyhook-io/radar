package mcp

import (
	"bytes"
	"context"
	"log"
	"strings"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/skyhook-io/radar/pkg/investigation"
)

func TestApplyServerDoesNotExposeDelete(t *testing.T) {
	enableExecToolsForTest(t)
	ctx := context.Background()
	server := newApplyServer()
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
	if strings.Contains(cs.InitializeResult().Instructions, "exec_pod") {
		t.Fatal("Apply instructions advertise excluded exec")
	}
	if !strings.Contains(cs.InitializeResult().Instructions, "patch_resource") {
		t.Fatal("Apply lost write routing guidance")
	}
	listed, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	patch := false
	for _, tool := range listed.Tools {
		if tool.Name == "delete_resource" || tool.Name == "exec_pod" {
			t.Fatalf("Apply exposes restricted tool %s", tool.Name)
		}
		patch = patch || tool.Name == "patch_resource"
	}
	if !patch {
		t.Fatal("ordinary remediation tool missing")
	}
	result, err := cs.CallTool(ctx, &mcpsdk.CallToolParams{Name: "delete_resource", Arguments: map[string]any{"kind": "ConfigMap", "name": "x", "namespace": "demo"}})
	if err == nil && (result == nil || !result.IsError) {
		t.Fatalf("unlisted deletion accepted: %+v", result)
	}
	result, err = cs.CallTool(ctx, &mcpsdk.CallToolParams{Name: "exec_pod", Arguments: map[string]any{"name": "x", "namespace": "demo", "command": []string{"true"}}})
	if err == nil && (result == nil || !result.IsError) {
		t.Fatalf("unlisted exec accepted: %+v", result)
	}
	if investigation.IsWriteTool("delete_resource") {
		t.Fatal("Diagnose allowlist enables deletion without preview approval")
	}
}

func TestDeleteConfirmationTokenIsNotLogged(t *testing.T) {
	var output bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&output)
	t.Cleanup(func() { log.SetOutput(previous) })
	handler := logToolCall("delete_resource", func(context.Context, *mcpsdk.CallToolRequest, deleteResourceInput) (*mcpsdk.CallToolResult, any, error) {
		return nil, nil, nil
	})
	_, _, err := handler(context.Background(), nil, deleteResourceInput{Kind: "ConfigMap", Namespace: "demo", Name: "settings", Confirm: "private-approval-token"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output.String(), "private-approval-token") {
		t.Fatal("confirmation token leaked to log")
	}
	if !strings.Contains(output.String(), "ConfigMap") {
		t.Fatal("useful target metadata missing")
	}
}
