package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"reflect"
	"slices"
	"strings"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// Argument-name repair for tool calls.
//
// Every radar tool is generated with `additionalProperties: false`, so an agent
// that guesses one argument name wrong gets a hard rejection rather than a
// partial result — and the SDK's message names the properties it did NOT expect
// without ever saying which ones it does. Agents generally do not retry a schema
// error, so a single wrong guess silently costs the whole investigation.
//
// This was observed in a benchmark: an agent called get_subject_permissions with
// {"subject": ..., "subjectKind": ...} (the nouns that tool's own description
// uses) instead of {"name": ..., "kind": ...}, got
//
//	validating "arguments": validating root: unexpected additional properties ["subject" "subjectKind"]
//
// and went on to report "the ServiceAccount has no RBAC RoleBindings at all"
// when in fact a ClusterRoleBinding existed and was merely missing a verb. The
// tool was correct; the interface refused the call.
//
// Two mitigations, applied as receiving middleware so tool schemas stay clean:
//
//  1. Repair arguments before validation — orthographic variants globally
//     (snake_case vs camelCase of the same word) plus a small curated set of
//     semantic aliases where a tool's description invites a different noun.
//  2. When validation still fails, append the accepted argument names so the
//     agent can correct itself instead of guessing again.
//
// Repair is deliberately conservative: an argument is only renamed when the
// target name is a real parameter of *that* tool and the caller did not already
// supply it. Unknown names we cannot resolve are left alone so validation still
// rejects genuine mistakes.

// toolParamNames maps a tool name to its accepted argument names; toolRequired
// maps it to the subset that must be present. Both are populated by addTool at
// registration, from the handler's input struct.
var (
	toolParamNames = map[string][]string{}
	toolRequired   = map[string][]string{}
)

// perToolAliases lists semantic aliases per tool: names an agent plausibly
// reaches for that mean an existing parameter. Keys are lowercased.
//
// Keep this list evidence-driven and narrow. A wrong entry silently redirects a
// caller's value to a different field, which is worse than the rejection it
// replaces — so only add a name whose meaning is unambiguous for that specific
// tool. Cross-tool spelling differences do not belong here; they are handled
// orthographically below.
var perToolAliases = map[string]map[string]string{
	// The description says "subject" throughout ("for a ServiceAccount, User,
	// or Group", "ServiceAccounts require a subject namespace") while the
	// parameters are kind/name. This is the case observed in the wild.
	"get_subject_permissions": {
		"subject":      "name",
		"subjectkind":  "kind",
		"subject_kind": "kind",
		"subjectname":  "name",
		"subject_name": "name",
	},
	// Pod-scoped tools: "pod" can only mean the pod's name.
	"get_pod_logs": {"pod": "name", "podname": "name", "pod_name": "name"},
	// Workload-scoped: "workload" can only mean the workload's name.
	"get_workload_logs": {"workload": "name", "workloadname": "name", "workload_name": "name"},
}

// addTool registers a tool and records its accepted argument names.
//
// Use this instead of mcpsdk.AddTool for every radar tool: the registry it
// builds is what lets a failed call report what it should have been called with.
func addTool[In, Out any](s *mcpsdk.Server, t *mcpsdk.Tool, h mcpsdk.ToolHandlerFor[In, Out]) {
	accepted, required := structJSONFields(reflect.TypeFor[In]())
	toolParamNames[t.Name] = accepted
	toolRequired[t.Name] = required
	mcpsdk.AddTool(s, t, h)
}

// structJSONFields returns the json field names of a struct type, and the subset
// that is required. It mirrors the SDK's own schema generation: a field is
// required when its json tag carries no `omitempty`.
func structJSONFields(t reflect.Type) (accepted, required []string) {
	for t != nil && t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t == nil || t.Kind() != reflect.Struct {
		return nil, nil
	}
	for i := range t.NumField() {
		f := t.Field(i)
		if f.Anonymous {
			// Embedded struct fields are inlined into the same JSON object.
			a, r := structJSONFields(f.Type)
			accepted = append(accepted, a...)
			required = append(required, r...)
			continue
		}
		if !f.IsExported() {
			continue
		}
		tag := f.Tag.Get("json")
		if tag == "-" {
			continue
		}
		name, opts, _ := strings.Cut(tag, ",")
		if name == "" {
			name = f.Name
		}
		accepted = append(accepted, name)
		if !slices.Contains(strings.Split(opts, ","), "omitempty") {
			required = append(required, name)
		}
	}
	return accepted, required
}

// toSnake and toCamel convert between the two spellings of a multi-word
// argument name. radar is snake_case nearly everywhere (dry_run, tail_lines,
// resource_namespace) but manage_gitops uses camelCase (dryRun, applyOnly,
// syncOptions, historyId). An agent that learned `dry_run` from apply_resource
// is otherwise hard-rejected by manage_gitops for the same concept.
func toSnake(s string) string {
	var b strings.Builder
	for i, r := range s {
		if r >= 'A' && r <= 'Z' {
			if i > 0 {
				b.WriteByte('_')
			}
			b.WriteRune(r - 'A' + 'a')
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

func toCamel(s string) string {
	parts := strings.Split(s, "_")
	var b strings.Builder
	for i, p := range parts {
		if p == "" {
			continue
		}
		if i == 0 {
			b.WriteString(p)
			continue
		}
		b.WriteString(strings.ToUpper(p[:1]))
		b.WriteString(p[1:])
	}
	return b.String()
}

// repairToolArgs rewrites recognisable misspellings of argument names to the
// names this tool actually accepts. It returns the (possibly rewritten) JSON and
// a human-readable list of the renames performed, for logging.
func repairToolArgs(tool string, raw json.RawMessage) (json.RawMessage, []string) {
	accepted := toolParamNames[tool]
	if len(accepted) == 0 || len(raw) == 0 {
		return raw, nil
	}
	var args map[string]json.RawMessage
	if err := json.Unmarshal(raw, &args); err != nil || len(args) == 0 {
		// Not an object (or malformed) — leave it for the validator to reject.
		return raw, nil
	}

	isAccepted := func(n string) bool { return slices.Contains(accepted, n) }
	// Resolve a supplied-but-unknown key to an accepted parameter, or "".
	resolve := func(key string) string {
		if alias, ok := perToolAliases[tool][strings.ToLower(key)]; ok && isAccepted(alias) {
			return alias
		}
		for _, cand := range []string{toSnake(key), toCamel(key)} {
			if cand != key && isAccepted(cand) {
				return cand
			}
		}
		return ""
	}

	var repairs []string
	for key, val := range args {
		if isAccepted(key) {
			continue
		}
		target := resolve(key)
		// Never clobber a value the caller supplied under the correct name.
		if target == "" {
			continue
		}
		if _, taken := args[target]; taken {
			continue
		}
		delete(args, key)
		args[target] = val
		repairs = append(repairs, fmt.Sprintf("%s->%s", key, target))
	}
	if len(repairs) == 0 {
		return raw, nil
	}
	fixed, err := json.Marshal(args)
	if err != nil {
		return raw, nil
	}
	slices.Sort(repairs)
	return fixed, repairs
}

// describeToolParams renders the accepted arguments for a tool, required first.
func describeToolParams(tool string) string {
	accepted := toolParamNames[tool]
	if len(accepted) == 0 {
		return ""
	}
	required := toolRequired[tool]
	optional := make([]string, 0, len(accepted))
	for _, n := range accepted {
		if !slices.Contains(required, n) {
			optional = append(optional, n)
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "\n\n%s accepts: ", tool)
	if len(required) > 0 {
		fmt.Fprintf(&b, "%s (required)", strings.Join(required, ", "))
		if len(optional) > 0 {
			b.WriteString("; ")
		}
	}
	if len(optional) > 0 {
		fmt.Fprintf(&b, "%s (optional)", strings.Join(optional, ", "))
	}
	b.WriteString(". Retry with these names.")
	return b.String()
}

// paramRepairMiddleware repairs tool arguments before schema validation and, when
// validation still fails, tells the caller which arguments the tool accepts.
func paramRepairMiddleware(next mcpsdk.MethodHandler) mcpsdk.MethodHandler {
	return func(ctx context.Context, method string, req mcpsdk.Request) (mcpsdk.Result, error) {
		if method != "tools/call" {
			return next(ctx, method, req)
		}
		call, ok := req.(*mcpsdk.CallToolRequest)
		if !ok || call.Params == nil {
			return next(ctx, method, req)
		}

		if fixed, repairs := repairToolArgs(call.Params.Name, call.Params.Arguments); repairs != nil {
			call.Params.Arguments = fixed
			logRepairedArgs(call.Params.Name, repairs)
		}

		res, err := next(ctx, method, req)
		if err != nil {
			return res, err
		}
		// The SDK reports a schema failure as an isError result with a nil
		// error, so the enrichment hangs off the result rather than err.
		if out, ok := res.(*mcpsdk.CallToolResult); ok {
			annotateSchemaError(call.Params.Name, out)
		}
		return res, nil
	}
}

// logRepairedArgs records renames so a tool call that only worked because of
// repair is visible, rather than looking like the agent got it right. A run
// full of these is a signal to rename the parameter or fix its description.
func logRepairedArgs(tool string, repairs []string) {
	log.Printf("\033[1;35m[MCP]\033[0m \033[1m%s\033[0m repaired args: %s", tool, strings.Join(repairs, " "))
}

// annotateSchemaError appends the accepted argument names to a validation
// failure. Anything else (a tool's own domain error) is left untouched.
func annotateSchemaError(tool string, res *mcpsdk.CallToolResult) {
	if res == nil || !res.IsError {
		return
	}
	help := describeToolParams(tool)
	if help == "" {
		return
	}
	for i, c := range res.Content {
		text, ok := c.(*mcpsdk.TextContent)
		if !ok || !strings.Contains(text.Text, `validating "arguments"`) {
			continue
		}
		if strings.Contains(text.Text, "accepts:") {
			continue // already annotated
		}
		res.Content[i] = &mcpsdk.TextContent{Text: text.Text + help}
	}
}
