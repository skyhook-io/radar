package mcp

import (
	"context"
	"net/http/httptest"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/skyhook-io/radar/internal/investigationrefs"
	"github.com/skyhook-io/radar/internal/server"
	"github.com/skyhook-io/radar/pkg/investigation"
)

// Claude Code before v2.1.296 keeps only the first 2,048 characters of server
// instructions, and newer versions 4,096. The routing has to survive the
// shorter cut, and the whole text stays well under the longer one.
const (
	maxInstructionChars = 3500
	routingCutoffChars  = 2048
)

func TestServerInstructionsFitClientLimits(t *testing.T) {
	for _, includeWrites := range []bool{true, false} {
		text := serverInstructions(includeWrites)
		if n := len([]rune(text)); n > maxInstructionChars {
			t.Errorf("includeWrites=%v: %d chars, limit %d", includeWrites, n, maxInstructionChars)
		}
		end := strings.Index(text, "READING RADAR'S SIGNALS")
		if end < 0 || len([]rune(text[:end])) >= routingCutoffChars {
			t.Errorf("includeWrites=%v: routing ends at char %d, must end before %d", includeWrites, end, routingCutoffChars)
		}
		if !strings.HasSuffix(text, investigation.ResultDiscipline) {
			t.Errorf("includeWrites=%v: text does not end with investigation.ResultDiscipline", includeWrites)
		}
	}
}

// instructionFieldNames are the snake_case words in the instructions that name
// result fields and values rather than tools.
var instructionFieldNames = []string{
	"diagnostic_context", "issue_timing", "started_after_resource_was_healthy",
	"started_at_resource_creation", "timing_summary",
}

func TestServerInstructionsNameOnlyRegisteredTools(t *testing.T) {
	snake := regexp.MustCompile(`\b[a-z]+(?:_[a-z]+)+\b`)
	for _, includeWrites := range []bool{true, false} {
		registered := map[string]bool{}
		for _, tool := range listRegisteredToolsWith(t, includeWrites) {
			registered[tool.Name] = true
		}
		text := serverInstructions(includeWrites)
		named := []string{"issues", "diagnose", "search"}
		for _, word := range snake.FindAllString(text, -1) {
			if !slices.Contains(instructionFieldNames, word) {
				named = append(named, word)
			}
		}
		for _, name := range named {
			if !registered[name] {
				t.Errorf("includeWrites=%v: instructions name %q, which is not a registered tool", includeWrites, name)
			}
		}
		for _, field := range instructionFieldNames {
			if !strings.Contains(text, field) {
				t.Errorf("instructionFieldNames lists %q, which the text no longer uses", field)
			}
		}
	}
}

// RunStdio serves newFullServer; check it over a transport, as the HTTP test
// below does for the mounted handlers.
func TestStdioServerSendsInstructions(t *testing.T) {
	ctx := context.Background()
	serverTransport, clientTransport := mcpsdk.NewInMemoryTransports()
	serverSession, err := newFullServer().Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer serverSession.Close()
	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "instructions-test", Version: "test"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	if got := session.InitializeResult().Instructions; got != serverInstructions(true) {
		t.Fatalf("stdio server instructions:\n got %q\nwant %q", got, serverInstructions(true))
	}
}

func TestInitializeReturnsServerInstructions(t *testing.T) {
	refs := investigationrefs.NewRegistry()
	scope := strings.Repeat("c", 26)
	lease, err := refs.Begin(scope)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()

	srv := server.New(server.Config{
		DevMode:                 true,
		MCPHandler:              NewHandler(),
		MCPReadOnlyHandler:      NewReadOnlyHandler(),
		MCPInvestigationHandler: NewInvestigationHandler(refs),
	})
	httpServer := httptest.NewServer(srv.Handler())
	defer httpServer.Close()

	cases := []struct {
		path string
		want string
	}{
		{"/mcp", serverInstructions(true)},
		{"/mcp-readonly", serverInstructions(false)},
		// Radar's own investigation agent runs under investigation.SystemPrompt.
		{"/mcp-investigation?scope=" + scope, ""},
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "instructions-test", Version: "test"}, nil)
			session, err := client.Connect(ctx, &mcpsdk.StreamableClientTransport{Endpoint: httpServer.URL + tc.path}, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer session.Close()
			if got := session.InitializeResult().Instructions; got != tc.want {
				t.Fatalf("initialize instructions:\n got %q\nwant %q", got, tc.want)
			}
		})
	}
	if serverInstructions(false) == serverInstructions(true) || strings.Contains(serverInstructions(false), "patch_resource") {
		t.Fatal("read-only instructions must leave out the write tools")
	}
}
