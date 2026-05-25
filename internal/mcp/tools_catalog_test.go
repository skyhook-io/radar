package mcp

import (
	"context"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestRegisteredToolAnnotations(t *testing.T) {
	tools := listRegisteredTools(t)
	writeTools := map[string]bool{
		"manage_workload": true,
		"manage_cronjob":  true,
		"manage_gitops":   true,
		"apply_resource":  true,
		"manage_node":     true,
	}

	seenWriteTools := map[string]bool{}
	for _, tool := range tools {
		if tool.Annotations == nil {
			t.Fatalf("tool %q missing annotations", tool.Name)
		}
		if tool.Annotations.OpenWorldHint == nil || *tool.Annotations.OpenWorldHint {
			t.Errorf("tool %q should set openWorldHint=false", tool.Name)
		}
		if writeTools[tool.Name] {
			seenWriteTools[tool.Name] = true
			if tool.Annotations.ReadOnlyHint {
				t.Errorf("write tool %q should not set readOnlyHint=true", tool.Name)
			}
			if tool.Annotations.DestructiveHint == nil || !*tool.Annotations.DestructiveHint {
				t.Errorf("write tool %q should set destructiveHint=true", tool.Name)
			}
			continue
		}
		if !tool.Annotations.ReadOnlyHint {
			t.Errorf("read tool %q should set readOnlyHint=true", tool.Name)
		}
		if tool.Annotations.DestructiveHint != nil && *tool.Annotations.DestructiveHint {
			t.Errorf("read tool %q should not set destructiveHint=true", tool.Name)
		}
	}

	for name := range writeTools {
		if !seenWriteTools[name] {
			t.Errorf("write tool %q was not registered", name)
		}
	}
}

func listRegisteredTools(t *testing.T) []*mcpsdk.Tool {
	t.Helper()

	server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "radar-test", Version: "test"}, nil)
	registerTools(server)

	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "radar-test-client", Version: "test"}, nil)
	ctx := context.Background()
	serverTransport, clientTransport := mcpsdk.NewInMemoryTransports()
	serverSession, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatalf("server.Connect: %v", err)
	}
	clientSession, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("client.Connect: %v", err)
	}
	t.Cleanup(func() {
		_ = clientSession.Close()
		_ = serverSession.Wait()
	})

	result, err := clientSession.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if len(result.Tools) == 0 {
		t.Fatal("no MCP tools registered")
	}
	return result.Tools
}
