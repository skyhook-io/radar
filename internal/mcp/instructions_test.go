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
// shorter cut, and the whole text stays under the longer one. Codex describes
// a deferred server by the first line alone, cut at 250 characters
// (MAX_NAMESPACE_DESCRIPTION_CHARS in codex-rs/core/src/context/world_state/tools.rs).
const (
	maxInstructionChars = 3800
	// Radar Hub prepends a fleet preamble of about 550 characters to the full
	// server's text and keeps the total under 4,000.
	maxFullServerChars = 3440
	routingCutoffChars = 2048
	firstLineChars     = 250
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
		if n := len([]rune(text)); includeWrites && n > maxFullServerChars {
			t.Errorf("full server text is %d chars, limit %d so Radar Hub's preamble fits", n, maxFullServerChars)
		}
		firstLine, _, _ := strings.Cut(text, "\n")
		if n := len([]rune(firstLine)); n > firstLineChars {
			t.Errorf("includeWrites=%v: first line is %d chars, limit %d", includeWrites, n, firstLineChars)
		}
		for _, tool := range []string{"issues", "diagnose", "get_changes", "get_resource", "list_resources", "search", "get_workload_logs", "get_pod_logs", "get_events"} {
			if !strings.Contains(firstLine, tool) {
				t.Errorf("includeWrites=%v: first line does not name core tool %s", includeWrites, tool)
			}
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
	instructionIssueFields  = []string{"incident_parent", "issue_timing", "resource_created_at", "timing_summary"}
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

// instructionClaims pairs what the instructions say about a tool (says) with
// what backs it in that tool's contract: a phrase in its description
// (backedBy) or a parameter in its input schema (param). It is a tripwire: when
// either side changes, the test fails and the pairing gets rechecked by hand.
var instructionClaims = []struct{ tool, says, backedBy, param string }{
	{tool: "issues", says: "Symptom known, faulty resource not: issues", backedBy: "live state"},
	{tool: "issues", says: "references to missing objects", backedBy: "references"},
	{tool: "diagnose", says: "a Pod, Deployment, StatefulSet, DaemonSet or Rollout", backedBy: "Pod, Deployment, StatefulSet, DaemonSet and Argo Rollout"},
	{tool: "diagnose", says: "a Service, Ingress or route that traffic doesn't reach", backedBy: "traffic not reaching a service, route or ingress"},
	{tool: "diagnose", says: "an Argo CD Application or Flux Kustomization or HelmRelease (sync and health)", backedBy: "GitOps sync/health"},
	{tool: "get_pod_logs", says: "get_pod_logs for one container or its previous instance", param: "previous"},
	{tool: "get_pod_logs", says: "the grep argument of get_pod_logs", param: "grep"},
	{tool: "get_workload_logs", says: "or get_workload_logs", param: "grep"},
	{tool: "search", says: "status message): search", backedBy: "status message"},
	{tool: "top_resources", says: "OOMKills: top_resources", backedBy: "OOMKills"},
	{tool: "get_neighborhood", says: "service routing): get_neighborhood", backedBy: "service routing"},
	{tool: "get_neighborhood", says: "The graph around one object (owners, selectors, config references", backedBy: "targetPort/selector/endpoints problems, dependency timeouts, config/secret refs, owner chains"},
	{tool: "get_cluster_audit", says: "get_cluster_audit is configuration posture", backedBy: "STATIC CONFIG POSTURE"},
	{tool: "manage_workload", says: "manage_workload to restart, scale or roll back", backedBy: "'rollback'"},
}

func TestServerInstructionsClaimsMatchToolContracts(t *testing.T) {
	tools := map[string]*mcpsdk.Tool{}
	for _, tool := range listRegisteredToolsWith(t, true) {
		tools[tool.Name] = tool
	}
	text := serverInstructions(true)
	for _, claim := range instructionClaims {
		if !strings.Contains(text, claim.says) {
			t.Errorf("instructions no longer say %q about %s; update or drop this pairing", claim.says, claim.tool)
		}
		tool := tools[claim.tool]
		if tool == nil {
			t.Errorf("%s is not registered", claim.tool)
			continue
		}
		if claim.backedBy != "" && !strings.Contains(tool.Description, claim.backedBy) {
			t.Errorf("%s description no longer says %q; recheck %q in pkg/investigation/guidance.go", claim.tool, claim.backedBy, claim.says)
		}
		if claim.param != "" {
			raw, err := json.Marshal(tool.InputSchema)
			if err != nil {
				t.Fatal(err)
			}
			var schema struct {
				Properties map[string]json.RawMessage `json:"properties"`
			}
			if err := json.Unmarshal(raw, &schema); err != nil {
				t.Fatal(err)
			}
			if _, ok := schema.Properties[claim.param]; !ok {
				t.Errorf("%s has no %q parameter; recheck %q in pkg/investigation/guidance.go", claim.tool, claim.param, claim.says)
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

// started_at_resource_creation can come from the owner workload's condition
// (issue_timing_basis owner_condition), so a Pod row may carry its
// Deployment's timing, and the classifier accepts a short healthy window after
// creation (internal/k8s/issue_timing.go). The instructions must not read it
// as the onset of the specific failure or as continuous failure since creation.
func TestServerInstructionsScopeCreationTimingToResourceOrOwner(t *testing.T) {
	text := serverInstructions(true)
	if !strings.Contains(text, "started_at_resource_creation means the resource, or its owner workload, started failing during creation or first reconciliation.") {
		t.Error("instructions no longer scope started_at_resource_creation to the resource or its owner workload")
	}
	for _, overclaim := range []string{"the failure has been present since", "failing since it was created"} {
		if strings.Contains(text, overclaim) {
			t.Errorf("instructions say %q; the timing classification supports neither the specific failure's onset nor continuity since creation", overclaim)
		}
	}
}

// Radar Hub can hide the Kubernetes write tools from a read-only organization
// while still listing other vendors' write tools, so the fixes line conditions
// each Kubernetes write tool on that tool being listed.
func TestServerInstructionsConditionFixesOnKubernetesWriteTools(t *testing.T) {
	text := serverInstructions(true)
	if !strings.Contains(text, "- Fixes, each only if listed: patch_resource") {
		t.Error("the fixes line no longer conditions each write tool on that tool being listed")
	}
	if strings.Contains(text, "write tools:") {
		t.Error("the fixes line is conditioned on write tools in general, which other vendors' tools can satisfy")
	}
}
