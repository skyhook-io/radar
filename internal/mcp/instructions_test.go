package mcp

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/skyhook-io/radar/internal/investigationrefs"
	"github.com/skyhook-io/radar/internal/server"
	"github.com/skyhook-io/radar/pkg/investigation"
	"github.com/skyhook-io/radar/pkg/issuesapi"
)

// Claude Code before v2.1.296 keeps only the first 2,048 characters of server
// instructions, and newer versions 4,096. The routing has to survive the
// shorter cut, and the whole text stays under the longer one.
const (
	maxInstructionChars = 3800
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
		if !strings.HasSuffix(text, investigation.ResultDiscipline+" "+investigation.UntrustedClusterData) {
			t.Errorf("includeWrites=%v: text does not end with ResultDiscipline and UntrustedClusterData", includeWrites)
		}
	}
}

// instructionIssueFields are the snake_case words in the instructions that
// name fields of an issues row, and instructionTimingValues the issue_timing
// values they name; neither is a tool.
var (
	instructionIssueFields  = []string{"incident_parent", "issue_timing", "timing_summary"}
	instructionTimingValues = []string{"started_after_resource_was_healthy", "started_at_resource_creation"}
)

func TestServerInstructionsNameRealIssueFields(t *testing.T) {
	tags := map[string]bool{}
	issue := reflect.TypeOf(issuesapi.Issue{})
	for i := range issue.NumField() {
		name, _, _ := strings.Cut(issue.Field(i).Tag.Get("json"), ",")
		tags[name] = true
	}
	for _, field := range instructionIssueFields {
		if !tags[field] {
			t.Errorf("instructions name issue field %q, which issuesapi.Issue does not have", field)
		}
	}
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
			if !slices.Contains(instructionIssueFields, word) && !slices.Contains(instructionTimingValues, word) {
				named = append(named, word)
			}
		}
		for _, name := range named {
			if !registered[name] {
				t.Errorf("includeWrites=%v: instructions name %q, which is not a registered tool", includeWrites, name)
			}
		}
		for _, field := range slices.Concat(instructionIssueFields, instructionTimingValues) {
			if !strings.Contains(text, field) {
				t.Errorf("%q is listed as a field name the text uses, but the text no longer uses it", field)
			}
		}
	}
}

// instructionClaims pins what the instructions say about a tool to the phrase
// in that tool's own description or input schema that backs it. When a tool's
// contract changes, this fails and the instructions get rechecked.
var instructionClaims = []struct{ tool, backedBy string }{
	{"issues", "live state"},
	{"issues", "references"},
	{"diagnose", "Pod, Deployment, StatefulSet, DaemonSet and Argo Rollout"},
	{"diagnose", "current and previous logs"},
	{"diagnose", "startup blockers"},
	{"diagnose", "traffic not reaching a service, route or ingress"},
	{"diagnose", "per-route reachability"},
	{"diagnose", "Application, Kustomization and Flux HelmRelease"},
	{"diagnose", "GitOps sync/health"},
	{"get_changes", "field-level diffs"},
	{"get_pod_logs", `"previous"`},
	{"get_pod_logs", `"grep"`},
	{"get_workload_logs", `"grep"`},
	{"search", "status message"},
	{"top_resources", "OOMKills"},
	{"query_prometheus", "history"},
	{"get_neighborhood", "service routing"},
	{"get_cluster_audit", "STATIC CONFIG POSTURE"},
	{"manage_workload", "'restart'"},
	{"manage_workload", "'scale'"},
	{"manage_workload", "'rollback'"},
}

func TestServerInstructionsClaimsMatchToolContracts(t *testing.T) {
	contracts := map[string]string{}
	for _, tool := range listRegisteredToolsWith(t, true) {
		schema, err := json.Marshal(tool.InputSchema)
		if err != nil {
			t.Fatal(err)
		}
		contracts[tool.Name] = tool.Description + "\n" + string(schema)
	}
	text := serverInstructions(true)
	for _, claim := range instructionClaims {
		if !strings.Contains(text, claim.tool) {
			t.Errorf("claim about %q, which the instructions no longer name", claim.tool)
		}
		if !strings.Contains(contracts[claim.tool], claim.backedBy) {
			t.Errorf("%s no longer says %q; recheck what instructions.go says about it", claim.tool, claim.backedBy)
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
