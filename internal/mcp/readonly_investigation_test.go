package mcp

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/skyhook-io/radar/internal/investigationrefs"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/server"
)

func TestInvestigationWorksWithoutFullMCP(t *testing.T) {
	if err := k8s.InitTestResourceCache(fake.NewClientset(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "readonly-investigation"}})); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k8s.ResetResourceCache)
	refs := investigationrefs.NewRegistry()
	scope := strings.Repeat("a", 26)
	lease, err := refs.Begin(scope)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	srv := server.New(server.Config{MCPReadOnlyHandler: NewReadOnlyHandler(), MCPInvestigationHandler: NewInvestigationHandler(refs), InvestigationRefs: refs})
	host := httptest.NewServer(srv.Handler())
	defer host.Close()
	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "investigation-test", Version: "1"}, nil)
	session, err := client.Connect(t.Context(), &mcpsdk.StreamableClientTransport{Endpoint: host.URL + "/mcp-investigation?scope=" + scope}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	result, err := session.CallTool(t.Context(), &mcpsdk.CallToolParams{Name: "list_namespaces", Arguments: struct{}{}})
	if err != nil || result.IsError {
		t.Fatalf("read: %v, %v", result, err)
	}
	encoded, _ := json.Marshal(result)
	if !strings.Contains(string(encoded), "readonly-investigation") || !strings.Contains(string(encoded), "radar:evidence-ref=") {
		t.Fatalf("missing read evidence: %s", encoded)
	}
	result, err = session.CallTool(t.Context(), &mcpsdk.CallToolParams{Name: "apply_resource", Arguments: struct{}{}})
	if err == nil || !strings.Contains(err.Error(), "unknown tool") {
		t.Fatalf("investigation write = %v, %v; want unknown tool", result, err)
	}
	if len(lease.Close()) == 0 {
		t.Fatal("read evidence not recorded")
	}
}
