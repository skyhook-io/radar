package mcp

import (
	"context"
	"encoding/json"
	"maps"
	"slices"
	"strings"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// registerToolsOnce populates toolParamNames/toolRequired the same way a real
// server start does, so these tests exercise the registry that ships rather
// than a hand-written fixture.
func registerToolsOnce(t *testing.T) {
	t.Helper()
	// Check for a specific tool rather than non-emptiness: a test that
	// overwrites or removes one entry leaves the map populated, and a
	// len()>0 guard would then skip the rebuild and hand later tests a
	// registry missing exactly the tool they assert on.
	if accepted, _ := lookupToolParams("get_subject_permissions"); len(accepted) > 0 {
		return
	}
	server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "radar", Version: "test"}, nil)
	registerTools(server, true, defaultToolParams)
}

func TestSubjectPermissionsAliasRepairsTheBenchmarkFailure(t *testing.T) {
	registerToolsOnce(t)

	// The exact arguments an agent sent in the SREGym benchmark. They were
	// rejected outright, and the agent then reported "no RBAC RoleBindings at
	// all" for a ServiceAccount that had one.
	in := json.RawMessage(`{"namespace":"hotel-reservation","subject":"cleanup-controller","subjectKind":"ServiceAccount"}`)

	fixed, repairs, _ := repairToolArgs("get_subject_permissions", in)
	if len(repairs) == 0 {
		t.Fatal("expected the subject/subjectKind call to be repaired; it was left unchanged")
	}

	var got map[string]string
	if err := json.Unmarshal(fixed, &got); err != nil {
		t.Fatalf("repaired args are not valid JSON: %v", err)
	}
	want := map[string]string{
		"namespace": "hotel-reservation",
		"name":      "cleanup-controller",
		"kind":      "ServiceAccount",
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("arg %q = %q, want %q (full: %s)", k, got[k], v, fixed)
		}
	}
	for _, gone := range []string{"subject", "subjectKind"} {
		if _, still := got[gone]; still {
			t.Errorf("alias %q survived repair: %s", gone, fixed)
		}
	}
}

func TestOrthographicRepairCrossesTheSnakeCamelSplit(t *testing.T) {
	registerToolsOnce(t)

	// radar publishes snake_case throughout, so repair must absorb the camelCase
	// spelling an agent is most likely to guess: camelCase is what most
	// third-party MCP servers publish and what Kubernetes uses in manifests.
	cases := []struct{ tool, supplied, want string }{
		{"manage_gitops", "dryRun", "dry_run"},
		{"manage_gitops", "historyId", "history_id"},
		{"manage_gitops", "syncOptions", "sync_options"},
		{"apply_resource", "dryRun", "dry_run"},
		{"patch_resource", "dryRun", "dry_run"},
		{"get_workload_logs", "tailLines", "tail_lines"},
	}
	for _, tc := range cases {
		raw := json.RawMessage(`{"` + tc.supplied + `":true}`)
		fixed, repairs, _ := repairToolArgs(tc.tool, raw)
		if len(repairs) == 0 {
			t.Errorf("%s: %q was not repaired to %q", tc.tool, tc.supplied, tc.want)
			continue
		}
		var got map[string]any
		if err := json.Unmarshal(fixed, &got); err != nil {
			t.Fatalf("%s: bad JSON after repair: %v", tc.tool, err)
		}
		if _, ok := got[tc.want]; !ok {
			t.Errorf("%s: expected key %q after repair, got %s", tc.tool, tc.want, fixed)
		}
	}
}

func TestRepairNeverInventsOrClobbers(t *testing.T) {
	registerToolsOnce(t)

	t.Run("unknown name with no accepted target is left for the validator", func(t *testing.T) {
		raw := json.RawMessage(`{"kind":"ServiceAccount","name":"x","totally_made_up":1}`)
		fixed, repairs, _ := repairToolArgs("get_subject_permissions", raw)
		if len(repairs) != 0 {
			t.Fatalf("unexpected repair %v", repairs)
		}
		if string(fixed) != string(raw) {
			t.Errorf("args were rewritten: %s", fixed)
		}
	})

	t.Run("an alias never overwrites a correctly-named value", func(t *testing.T) {
		// Both spellings supplied: the canonical one must win untouched.
		raw := json.RawMessage(`{"kind":"ServiceAccount","name":"real","subject":"decoy"}`)
		fixed, _, _ := repairToolArgs("get_subject_permissions", raw)
		var got map[string]string
		if err := json.Unmarshal(fixed, &got); err != nil {
			t.Fatal(err)
		}
		if got["name"] != "real" {
			t.Errorf("name = %q, want %q — alias clobbered a supplied value", got["name"], "real")
		}
	})

	t.Run("non-object arguments are passed through", func(t *testing.T) {
		raw := json.RawMessage(`["not","an","object"]`)
		fixed, repairs, _ := repairToolArgs("get_subject_permissions", raw)
		if len(repairs) != 0 || string(fixed) != string(raw) {
			t.Errorf("array args should be untouched, got %s %v", fixed, repairs)
		}
	})

	t.Run("unregistered tool is passed through", func(t *testing.T) {
		raw := json.RawMessage(`{"subject":"x"}`)
		fixed, repairs, _ := repairToolArgs("no_such_tool", raw)
		if len(repairs) != 0 || string(fixed) != string(raw) {
			t.Errorf("unknown tool should be untouched, got %s %v", fixed, repairs)
		}
	})
}

func TestSchemaErrorNamesTheAcceptedArguments(t *testing.T) {
	registerToolsOnce(t)

	help := describeToolParams("get_subject_permissions")
	if help == "" {
		t.Fatal("no parameter help produced for get_subject_permissions")
	}
	for _, want := range []string{"kind", "name", "required", "verb", "resource"} {
		if !strings.Contains(help, want) {
			t.Errorf("parameter help missing %q: %s", want, help)
		}
	}

	// The annotation must attach to a schema-validation failure...
	res := &mcpsdk.CallToolResult{
		IsError: true,
		Content: []mcpsdk.Content{&mcpsdk.TextContent{
			Text: `validating "arguments": validating root: unexpected additional properties ["subject"]`,
		}},
	}
	annotateSchemaError("get_subject_permissions", res, nil)
	if txt := res.Content[0].(*mcpsdk.TextContent).Text; !strings.Contains(txt, "accepts:") {
		t.Errorf("validation error was not annotated: %s", txt)
	}

	// ...and must NOT rewrite a tool's own domain error.
	domain := &mcpsdk.CallToolResult{
		IsError: true,
		Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: "ServiceAccount requires a namespace"}},
	}
	annotateSchemaError("get_subject_permissions", domain, nil)
	if txt := domain.Content[0].(*mcpsdk.TextContent).Text; strings.Contains(txt, "accepts:") {
		t.Errorf("domain error should not be annotated: %s", txt)
	}
}

// TestAliasesRefuseQualifiedValues is the safety property that makes semantic
// aliases acceptable at all.
//
// An alias moves a value into a different field. If the value carries structure
// the target field does not parse — "system:serviceaccount:ns:sa", "ns/pod",
// "deployment/api" — renaming it turns a rejected call into a confidently WRONG
// lookup: schema validation passes, the handler searches for a resource with
// that literal name, finds nothing, and reports "no bindings" for a subject that
// has them. That is the exact false negative this file exists to remove, so a
// qualified value must be left for the validator to reject.
func TestAliasesRefuseQualifiedValues(t *testing.T) {
	registerToolsOnce(t)

	qualified := []string{
		`{"subject":"system:serviceaccount:prod:cleanup","subjectKind":"ServiceAccount"}`,
		`{"subject":"prod/cleanup-controller","subjectKind":"ServiceAccount"}`,
		`{"subject":"has space","subjectKind":"ServiceAccount"}`,
		`{"subject":123,"subjectKind":"ServiceAccount"}`,
	}
	for _, in := range qualified {
		fixed, _, unresolved := repairToolArgs("get_subject_permissions", json.RawMessage(in))
		var got map[string]json.RawMessage
		if err := json.Unmarshal(fixed, &got); err != nil {
			t.Fatalf("%s: %v", in, err)
		}
		if _, moved := got["name"]; moved {
			t.Errorf("qualified value was silently remapped to name: %s -> %s", in, fixed)
		}
		if !slices.Contains(unresolved, "subject") {
			t.Errorf("%s: expected 'subject' reported unresolved, got %v", in, unresolved)
		}
	}

	// The bare-name case must still be repaired — the guard must not disable
	// the alias entirely.
	_, repairs, _ := repairToolArgs("get_subject_permissions",
		json.RawMessage(`{"subject":"cleanup-controller","subjectKind":"ServiceAccount"}`))
	if len(repairs) == 0 {
		t.Error("guard disabled the alias for a plain name; bare names must still be repaired")
	}
}

// TestMiddlewareAnnotatesRealSDKRejection drives an actually-invalid tools/call
// through the SDK rather than fabricating its error text.
//
// The point is that the help must survive an SDK wording change: annotation is
// keyed off argument names we know the tool does not accept, not off matching
// the SDK's rendered message. A test that asserts against a hand-written string
// would keep passing while the real integration broke.
func TestMiddlewareAnnotatesRealSDKRejection(t *testing.T) {
	ctx := context.Background()
	session := connectTestServer(t)

	res, err := session.CallTool(ctx, &mcpsdk.CallToolParams{
		Name:      "get_subject_permissions",
		Arguments: map[string]any{"namespace": "kube-system", "whoIsThis": "x"},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if !res.IsError {
		t.Fatal("expected an unknown argument to be rejected")
	}
	text := renderContent(res.Content)
	if !strings.Contains(text, "get_subject_permissions accepts:") {
		t.Errorf("rejection was not annotated with accepted arguments:\n%s", text)
	}
	for _, want := range []string{"kind, name (required)", "resource_namespace"} {
		if !strings.Contains(text, want) {
			t.Errorf("annotation missing %q:\n%s", want, text)
		}
	}
}

// TestOrthographicRepairHandlesNumericSegments covers parameters whose words end
// in a digit.
//
// toSnake and toCamel do not round-trip there — toCamel("diff_revision_1") is
// "diffRevision1", but toSnake of that is "diff_revision1" — so deriving
// candidate spellings from the caller's key silently misses a real parameter.
// Candidates come from the accepted set instead, which cannot drift.
func TestOrthographicRepairHandlesNumericSegments(t *testing.T) {
	registerToolsOnce(t)

	fixed, repairs, unresolved := repairToolArgs("get_helm_release",
		json.RawMessage(`{"namespace":"n","name":"x","diffRevision1":1,"diffRevision2":2}`))
	if len(repairs) != 2 {
		t.Fatalf("expected both numeric-segment params repaired, got repairs=%v unresolved=%v", repairs, unresolved)
	}
	var got map[string]json.RawMessage
	if err := json.Unmarshal(fixed, &got); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"diff_revision_1", "diff_revision_2"} {
		if _, ok := got[want]; !ok {
			t.Errorf("missing %q after repair: %s", want, fixed)
		}
	}
}

// TestZeroArgumentToolSaysSo distinguishes a tool that takes no arguments from
// one we never registered. Both have an empty parameter list, but only the first
// can tell the caller what to do about it.
func TestZeroArgumentToolSaysSo(t *testing.T) {
	registerToolsOnce(t)

	help := describeToolParams("list_namespaces")
	if !strings.Contains(help, "accepts no arguments") {
		t.Errorf("zero-argument tool gave no usable help: %q", help)
	}
	if got := describeToolParams("no_such_tool_at_all"); got != "" {
		t.Errorf("unregistered tool should produce no help, got %q", got)
	}

	// A bogus argument on a zero-argument tool must be reported as unresolved so
	// the help attaches.
	_, _, unresolved := repairToolArgs("list_namespaces", json.RawMessage(`{"namespace":"x"}`))
	if !slices.Contains(unresolved, "namespace") {
		t.Errorf("expected 'namespace' unresolved on a zero-argument tool, got %v", unresolved)
	}
}

// TestAliasRepairIsDeterministic pins an invariant: the subject kind must be
// resolved before the argument map is iterated.
//
// Resolving it during iteration makes the outcome depend on whether Go's random
// map order happened to rename "SubjectKind" to "kind" first, so the identical
// request is repaired or refused at random.
func TestAliasRepairIsDeterministic(t *testing.T) {
	registerToolsOnce(t)

	const args = `{"Subject":"system:authenticated","SubjectKind":"Group"}`
	first := -1
	for i := 0; i < 300; i++ {
		_, repairs, _ := repairToolArgs("get_subject_permissions", json.RawMessage(args))
		if first == -1 {
			first = len(repairs)
			continue
		}
		if len(repairs) != first {
			t.Fatalf("run %d repaired %d args, first run repaired %d — result depends on map order",
				i, len(repairs), first)
		}
	}
	if first != 2 {
		t.Errorf("expected both Subject and SubjectKind repaired, got %d", first)
	}
}

// TestMiddlewareDeliversRepairedArgsToHandler proves a repaired call actually
// reaches the handler carrying the canonical names.
//
// Asserting only that a schema error is absent would pass even if the call were
// dropped or rewritten wrongly, so this registers a sentinel tool and inspects
// exactly what its handler received.
func TestMiddlewareDeliversRepairedArgsToHandler(t *testing.T) {
	ctx := context.Background()

	type sentinelIn struct {
		Kind      string `json:"kind"`
		Name      string `json:"name"`
		Namespace string `json:"namespace,omitempty"`
		DryRun    bool   `json:"dry_run,omitempty"`
	}
	var got sentinelIn
	var ran bool

	// Snapshot BEFORE addTool overwrites the entry, or the restore below would
	// put the sentinel's shape back instead of the real tool's.
	registerToolsOnce(t)
	prevAccepted, prevRequired := lookupToolParams("get_subject_permissions")

	server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "radar-test", Version: "test"}, nil)
	server.AddReceivingMiddleware(paramRepairMiddleware)
	addTool(server, &mcpsdk.Tool{Name: "get_subject_permissions", Description: "sentinel"},
		func(_ context.Context, _ *mcpsdk.CallToolRequest, in sentinelIn) (*mcpsdk.CallToolResult, any, error) {
			ran, got = true, in
			return &mcpsdk.CallToolResult{Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: "ok"}}}, nil, nil
		})
	// The registry is process-global and this sentinel deliberately overwrites a
	// real tool's entry. Snapshot and RESTORE it rather than deleting: deleting
	// leaves the map non-empty, so registerToolsOnce would skip rebuilding and
	// every later alias test would silently run without this tool's schema.
	t.Cleanup(func() {
		toolParamsMu.Lock()
		defer toolParamsMu.Unlock()
		if prevAccepted == nil {
			delete(toolParamNames, "get_subject_permissions")
			delete(toolRequired, "get_subject_permissions")
			return
		}
		toolParamNames["get_subject_permissions"] = prevAccepted
		toolRequired["get_subject_permissions"] = prevRequired
	})

	session := connectTo(t, server)
	res, err := session.CallTool(ctx, &mcpsdk.CallToolParams{
		Name: "get_subject_permissions",
		// The benchmark's exact argument names, plus a camelCase spelling.
		Arguments: map[string]any{
			"namespace":   "hotel-reservation",
			"subject":     "cleanup-controller",
			"subjectKind": "ServiceAccount",
			"dryRun":      true,
		},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.IsError {
		t.Fatalf("repaired call was rejected: %s", renderContent(res.Content))
	}
	if !ran {
		t.Fatal("handler never ran — the repaired call did not get through")
	}
	want := sentinelIn{Kind: "ServiceAccount", Name: "cleanup-controller", Namespace: "hotel-reservation", DryRun: true}
	if got != want {
		t.Errorf("handler received %+v, want %+v", got, want)
	}
}

// TestMiddlewareAnnotatesMissingRequiredArgument covers the other guaranteed
// validation failure. It is detected structurally from the registry, so the help
// attaches without depending on the SDK's wording.
func TestMiddlewareAnnotatesMissingRequiredArgument(t *testing.T) {
	ctx := context.Background()
	session := connectTestServer(t)

	// `name` is required and omitted.
	res, err := session.CallTool(ctx, &mcpsdk.CallToolParams{
		Name:      "get_subject_permissions",
		Arguments: map[string]any{"kind": "ServiceAccount", "namespace": "kube-system"},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if !res.IsError {
		t.Fatal("expected a missing required argument to fail")
	}
	if text := renderContent(res.Content); !strings.Contains(text, "accepts:") {
		t.Errorf("missing-required failure was not annotated:\n%s", text)
	}
}

// TestAliasGuardIsSubjectKindAware pins the boundary in both directions.
//
// ServiceAccount names are DNS subdomains, so a qualified value is not a name
// and must not be moved into `name`. User and Group names are opaque identities
// where the same characters are correct — "system:serviceaccount:ns:sa" is the
// canonical User name for a ServiceAccount identity, and refusing to repair it
// would reject a call that was right all along.
func TestAliasGuardIsSubjectKindAware(t *testing.T) {
	registerToolsOnce(t)

	cases := []struct {
		name, args string
		repaired   bool
	}{
		{"SA bare name repairs", `{"subject":"cleanup","subjectKind":"ServiceAccount"}`, true},
		{"SA qualified refused", `{"subject":"system:serviceaccount:p:c","subjectKind":"ServiceAccount"}`, false},
		{"SA slashed refused", `{"subject":"prod/cleanup","subjectKind":"ServiceAccount"}`, false},
		{"SA spaced refused", `{"subject":"two words","subjectKind":"ServiceAccount"}`, false},
		{"User qualified repairs", `{"subject":"system:serviceaccount:p:c","subjectKind":"User"}`, true},
		{"Group qualified repairs", `{"subject":"system:authenticated","subjectKind":"Group"}`, true},
		// Group identities are opaque and may contain spaces — an OIDC group
		// named "Platform Admins" is legitimate and must not be refused.
		{"Group with spaces repairs", `{"subject":"Platform Admins","subjectKind":"Group"}`, true},
		{"unknown kind is strict", `{"subject":"system:authenticated"}`, false},
		// Malformed ServiceAccount names must be refused too: aliasing one would
		// pass validation and have the handler report an empty permission set for
		// an account that cannot exist, instead of a rejection the caller can act on.
		{"SA underscore refused", `{"subject":"cleanup_controller","subjectKind":"ServiceAccount"}`, false},
		{"SA uppercase refused", `{"subject":"CleanupController","subjectKind":"ServiceAccount"}`, false},
		{"SA leading dash refused", `{"subject":"-cleanup","subjectKind":"ServiceAccount"}`, false},
		{"SA dotted name repairs", `{"subject":"cleanup.controller","subjectKind":"ServiceAccount"}`, true},
		// The kind alias carries an enum, not a resource name. Applying the
		// name validation to it would reject every correct call, since
		// "ServiceAccount" is not a valid DNS-1123 subdomain.
		{"kind alias is not name-validated", `{"subject":"cleanup","subjectKind":"ServiceAccount"}`, true},
		// Case variants of the alias keys must resolve the kind the same way, or
		// the result depends on Go's map iteration order.
		{"capitalised keys, Group", `{"Subject":"system:authenticated","SubjectKind":"Group"}`, true},
		{"capitalised keys, SA qualified refused", `{"Subject":"a:b","SubjectKind":"ServiceAccount"}`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fixed, _, _ := repairToolArgs("get_subject_permissions", json.RawMessage(tc.args))
			var got map[string]json.RawMessage
			if err := json.Unmarshal(fixed, &got); err != nil {
				t.Fatal(err)
			}
			_, moved := got["name"]
			if moved != tc.repaired {
				t.Errorf("subject moved to name = %v, want %v (%s -> %s)", moved, tc.repaired, tc.args, fixed)
			}
		})
	}
}

func renderContent(content []mcpsdk.Content) string {
	var b strings.Builder
	for _, c := range content {
		if tc, ok := c.(*mcpsdk.TextContent); ok {
			b.WriteString(tc.Text)
			b.WriteString("\n")
		}
	}
	return b.String()
}

// connectTestServer wires a client to a full radar MCP server over an in-memory
// transport, so tests exercise the real middleware chain.
func connectTestServer(t *testing.T) *mcpsdk.ClientSession {
	t.Helper()
	return connectTo(t, newServer(true))
}

// connectTo wires a client to an already-built server over an in-memory
// transport, so tests exercise the real middleware chain.
func connectTo(t *testing.T, server *mcpsdk.Server) *mcpsdk.ClientSession {
	t.Helper()
	ctx := context.Background()

	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "radar-test-client", Version: "test"}, nil)
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
	return clientSession
}

// TestRegistryMatchesPublishedSchema is the load-bearing correctness test.
//
// The registry is derived by reflection over the handler input structs, while
// the schema agents actually see is generated independently by the SDK. If the
// two ever disagree, repair silently stops recognising a real parameter and the
// error help starts naming arguments that don't exist — both of which make the
// failure mode worse than the one this code exists to fix. Compare against the
// live tools/list output rather than trusting the reflection.
func TestRegistryMatchesPublishedSchema(t *testing.T) {
	// Both server shapes: the read-only handler registers a different tool set,
	// and a tool that skipped addTool() in either is invisible until an agent
	// gets a name wrong against that server.
	for _, includeWrites := range []bool{true, false} {
		tools, registry := listRegisteredToolsWithRegistry(t, includeWrites)
		for _, tool := range tools {
			checkToolAgainstRegistry(t, registry, tool)
		}
	}
}

func checkToolAgainstRegistry(t *testing.T, registry *toolParamRegistry, tool *mcpsdk.Tool) {
	t.Helper()
	if tool.InputSchema == nil {
		return
	}
	accepted, required := registry.lookup(tool.Name)

	// InputSchema is `any` on the wire; round-trip it to read the shape the SDK
	// actually publishes.
	raw, err := json.Marshal(tool.InputSchema)
	if err != nil {
		t.Fatalf("%s: marshal InputSchema: %v", tool.Name, err)
	}
	var schema struct {
		Properties map[string]json.RawMessage `json:"properties"`
		Required   []string                   `json:"required"`
	}
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatalf("%s: unmarshal InputSchema: %v", tool.Name, err)
	}

	wantAccepted := slices.Sorted(maps.Keys(schema.Properties))
	gotAccepted := slices.Clone(accepted)
	slices.Sort(gotAccepted)
	if !slices.Equal(gotAccepted, wantAccepted) {
		t.Errorf("%s: registry params %v != published schema properties %v",
			tool.Name, gotAccepted, wantAccepted)
	}

	wantRequired := slices.Clone(schema.Required)
	slices.Sort(wantRequired)
	gotRequired := slices.Clone(required)
	slices.Sort(gotRequired)
	if !slices.Equal(gotRequired, wantRequired) {
		t.Errorf("%s: registry required %v != published schema required %v",
			tool.Name, gotRequired, wantRequired)
	}
}

// TestParameterNamingStaysConsistent stops the footgun class from growing.
//
// Every radar parameter is snake_case. A lone camelCase outlier is what lets an
// agent that learned one tool's spelling get hard-rejected by another for the
// same concept, so the invariant is worth enforcing rather than repairing.
//
// If this fails on a tool you just added: rename the parameter to snake_case.
func TestParameterNamingStaysConsistent(t *testing.T) {
	registerToolsOnce(t)

	grandfathered := map[string][]string{}

	for tool, params := range toolParamNames {
		for _, p := range params {
			if p != toSnake(p) { // contains an uppercase letter
				if slices.Contains(grandfathered[tool], p) {
					continue
				}
				t.Errorf("%s.%s is camelCase; radar parameters are snake_case (want %q). "+
					"Mixed spellings across tools cause hard schema rejections for agents.",
					tool, p, toSnake(p))
			}
		}
	}
}

// TestAliasTargetsExist keeps perToolAliases honest: an alias pointing at a
// parameter that no longer exists silently stops working.
func TestAliasTargetsExist(t *testing.T) {
	registerToolsOnce(t)

	for tool, aliases := range perToolAliases {
		accepted, ok := toolParamNames[tool]
		if !ok {
			t.Errorf("perToolAliases references unknown tool %q", tool)
			continue
		}
		for alias, target := range aliases {
			if !slices.Contains(accepted, target) {
				t.Errorf("%s: alias %q -> %q, but %q is not a parameter (have %v)", tool, alias, target, target, accepted)
			}
			if slices.Contains(accepted, alias) {
				t.Errorf("%s: alias %q shadows a real parameter of the same name", tool, alias)
			}
		}
	}
}

// TestServiceAccountShorthandExpands covers the shape a rename cannot express:
// a key carrying both the subject kind and its name.
//
// The first case is the exact call that lost a benchmark investigation. The
// agent had already identified cleanup-controller as the finalizer's owner and
// asked the right question; the rejection sent it to an unrelated crashloop and
// a wrong conclusion. It had read "cleanup-controller" out of radar's own
// get_resource output, under the field name serviceAccountName.
func TestServiceAccountShorthandExpands(t *testing.T) {
	registerToolsOnce(t)
	tests := []struct {
		name      string
		args      map[string]any
		wantKind  string
		wantName  string
		wantFixed bool
	}{
		{
			name:      "the observed call",
			args:      map[string]any{"namespace": "hotel-reservation", "serviceAccount": "cleanup-controller"},
			wantKind:  "ServiceAccount",
			wantName:  "cleanup-controller",
			wantFixed: true,
		},
		{
			name:      "snake spelling",
			args:      map[string]any{"namespace": "ns", "service_account": "builder"},
			wantKind:  "ServiceAccount",
			wantName:  "builder",
			wantFixed: true,
		},
		{
			name:      "the spelling k8s itself uses on a pod spec",
			args:      map[string]any{"namespace": "ns", "serviceAccountName": "builder"},
			wantKind:  "ServiceAccount",
			wantName:  "builder",
			wantFixed: true,
		},
		{
			// A qualified value is not a ServiceAccount name. Expanding it would
			// validate and then report an empty permission set for an account
			// that cannot exist — a confident wrong answer.
			name:      "qualified value is refused, not reinterpreted",
			args:      map[string]any{"namespace": "ns", "serviceAccount": "prod/cleanup"},
			wantFixed: false,
		},
		{
			// An explicit subject always wins; the caller who sent both meant
			// something we cannot infer.
			name:      "explicit kind and name are never overwritten",
			args:      map[string]any{"kind": "User", "name": "alice", "serviceAccount": "builder"},
			wantKind:  "User",
			wantName:  "alice",
			wantFixed: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw, err := json.Marshal(tt.args)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			fixed, repairs, _ := repairToolArgs("get_subject_permissions", raw)
			var got map[string]any
			if err := json.Unmarshal(fixed, &got); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			expanded := false
			for _, r := range repairs {
				if strings.Contains(r, "->kind+name") {
					expanded = true
				}
			}
			if expanded != tt.wantFixed {
				t.Errorf("expanded = %v, want %v (repairs %v)", expanded, tt.wantFixed, repairs)
			}
			if tt.wantKind != "" {
				if got["kind"] != tt.wantKind {
					t.Errorf("kind = %v, want %q", got["kind"], tt.wantKind)
				}
				if got["name"] != tt.wantName {
					t.Errorf("name = %v, want %q", got["name"], tt.wantName)
				}
			}
		})
	}
}

// TestPodAliasRepairsPlainNamesOnly restores an alias this file originally
// rejected. The reasoning for rejecting it — a caller may send "prod/api-0" —
// is handled by the value guard, since the alias target is `name`: plain names
// repair, qualified ones still fall through to the rejection carrying help.
func TestPodAliasRepairsPlainNamesOnly(t *testing.T) {
	registerToolsOnce(t)
	for _, tt := range []struct {
		val       string
		wantFixed bool
	}{
		{"wrk2-job-frr7c", true},
		{"prod/api-0", false},
	} {
		raw, _ := json.Marshal(map[string]any{"namespace": "ns", "pod": tt.val, "container": "app"})
		fixed, repairs, _ := repairToolArgs("get_pod_logs", raw)
		var got map[string]any
		_ = json.Unmarshal(fixed, &got)
		if fixedOK := got["name"] == tt.val; fixedOK != tt.wantFixed {
			t.Errorf("pod=%q: name set = %v, want %v (repairs %v)", tt.val, fixedOK, tt.wantFixed, repairs)
		}
	}
}
