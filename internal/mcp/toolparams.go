package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"reflect"
	"slices"
	"strings"
	"sync"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"k8s.io/apimachinery/pkg/util/validation"
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
//
// Guarded because these are package-level maps written during registration and
// read on every tool call. Today every server is built before it serves
// (bootstrap constructs the read-write and read-only handlers in sequence), so
// there is no race — but a concurrent map write panics, and "someone later
// builds a handler lazily" is not a failure worth discovering in production.
// The read cost is nil next to the Kubernetes calls these tools make.
var (
	toolParamsMu   sync.RWMutex
	toolParamNames = map[string][]string{}
	toolRequired   = map[string][]string{}
)

func lookupToolParams(tool string) (accepted, required []string) {
	toolParamsMu.RLock()
	defer toolParamsMu.RUnlock()
	return toolParamNames[tool], toolRequired[tool]
}

// isRegisteredTool distinguishes a tool that takes no arguments from one this
// process never registered. Both have an empty parameter list, but only the
// first should tell a caller "this tool accepts no arguments" — for the second
// we know nothing and must stay quiet.
func isRegisteredTool(tool string) bool {
	toolParamsMu.RLock()
	defer toolParamsMu.RUnlock()
	_, ok := toolParamNames[tool]
	return ok
}

// perToolAliases lists semantic aliases per tool: names an agent plausibly
// reaches for that mean an existing parameter. Keys are lowercased.
//
// This list is deliberately tiny, and adding to it needs real evidence of an
// agent getting the name wrong — not a hunch that someone might. A silent
// rename routes a caller's value into a different field, and if the value does
// not mean what the target field expects, the call now *succeeds* with the
// wrong answer. That is strictly worse than the rejection it replaced, and it
// is the same false-negative class this file exists to remove.
//
// Only aliases whose value means the same thing under the target parameter
// qualify, as decided by aliasValueKeepsMeaning below. `pod` and `workload`
// were considered and rejected: an agent may reasonably send
// `pod: "prod/api-0"` or `workload: "deployment/api"`, which are not names.
//
// Cross-tool spelling differences do NOT belong here — those are pure
// orthography and are handled generically below.
var perToolAliases = map[string]map[string]string{
	// RBAC subjects are naturally called "subjects", so an agent reaching for
	// this tool tends to send subject/subjectKind rather than name/kind.
	"get_subject_permissions": {
		"subject":      "name",
		"subjectkind":  "kind",
		"subject_kind": "kind",
		"subjectname":  "name",
		"subject_name": "name",
	},
}

// aliasValueKeepsMeaning reports whether moving val into the canonical
// parameter preserves what the caller meant.
//
// What counts as safe depends on the subject kind, and getting this wrong in
// either direction is a bug:
//
//   - ServiceAccount names are DNS subdomains. A qualified value like
//     "prod/cleanup" or "system:serviceaccount:prod:cleanup" is NOT a
//     ServiceAccount name; renaming it to `name` would pass validation and make
//     the handler look up a resource that cannot exist, reporting "no bindings"
//     for a subject that has them — the false negative this file exists to
//     remove.
//   - User and Group names are opaque identities where those same characters
//     are normal and correct: "system:authenticated" is a real Group, and
//     "system:serviceaccount:prod:cleanup" is the canonical *User* name for a
//     ServiceAccount identity. Rejecting those would refuse to repair calls
//     that were right all along.
//
// kind is resolved once, before any rewriting (see repairToolArgs) — resolving
// it inside the rewrite loop would make the result depend on Go's random map
// iteration order, so the same request would be repaired or refused at random.
// When the kind is absent or unrecognised the strict ServiceAccount rule
// applies: that is the kind these tools are asked about most, and being
// conservative only costs a rejection with help attached.
func aliasValueKeepsMeaning(kind string, val json.RawMessage) bool {
	var s string
	if err := json.Unmarshal(val, &s); err != nil || s == "" {
		return false // non-string and empty values are never a name
	}

	switch strings.ToLower(kind) {
	case "user", "group":
		// Opaque identity, preserved verbatim by the RBAC index. ":" and "/"
		// are normal ("system:authenticated"), and so is whitespace — an OIDC
		// group may legitimately be named "Platform Admins".
		return true
	default:
		// ServiceAccount names are DNS-1123 subdomains. Validate in full rather
		// than blacklisting separators: a malformed name like "cleanup_controller"
		// would otherwise pass validation after aliasing and have the handler
		// report an empty permission set for an account that cannot exist —
		// a confident wrong answer where a rejection carrying the accepted
		// parameter names would have let the caller retry.
		return len(validation.IsDNS1123Subdomain(s)) == 0
	}
}

// subjectKindOf reads the subject kind from whichever spelling the caller used.
//
// Matching is case-insensitive because perToolAliases accepts case variants of
// the alias keys; recognising "SubjectKind" here but not there is what made the
// outcome depend on map order.
func subjectKindOf(args map[string]json.RawMessage) string {
	wanted := map[string]bool{"kind": true, "subjectkind": true, "subject_kind": true}
	for key, raw := range args {
		if !wanted[strings.ToLower(key)] {
			continue
		}
		var s string
		if err := json.Unmarshal(raw, &s); err == nil && s != "" {
			return s
		}
	}
	return ""
}

// addTool registers a tool and records its accepted argument names.
//
// Use this instead of mcpsdk.AddTool for every radar tool: the registry it
// builds is what lets a failed call report what it should have been called with.
func addTool[In, Out any](s *mcpsdk.Server, t *mcpsdk.Tool, h mcpsdk.ToolHandlerFor[In, Out]) {
	accepted, required := structJSONFields(reflect.TypeFor[In]())
	toolParamsMu.Lock()
	toolParamNames[t.Name] = accepted
	toolRequired[t.Name] = required
	toolParamsMu.Unlock()
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
// argument name.
//
// radar publishes snake_case throughout (dry_run, tail_lines,
// resource_namespace), matching Anthropic's own agent tooling and kubectl's
// flags. Most third-party MCP servers publish camelCase, and Kubernetes
// manifests use it too, so camelCase is a very common agent guess. Repair
// absorbs it rather than rejecting a call over spelling.
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
// names this tool actually accepts. It returns the (possibly rewritten) JSON,
// the renames performed (for logging), and any supplied names it could NOT
// resolve — which are exactly the names that will make schema validation fail,
// and so the signal for attaching parameter help without parsing SDK text.
func repairToolArgs(tool string, raw json.RawMessage) (fixed json.RawMessage, repairs, unresolved []string) {
	if !isRegisteredTool(tool) || len(raw) == 0 {
		return raw, nil, nil
	}
	accepted, _ := lookupToolParams(tool)
	var args map[string]json.RawMessage
	if err := json.Unmarshal(raw, &args); err != nil || len(args) == 0 {
		// Not an object (or malformed) — leave it for the validator to reject.
		return raw, nil, nil
	}

	isAccepted := func(n string) bool { return slices.Contains(accepted, n) }
	// Resolved before any rewriting: doing it per-key inside the loop below
	// would make the outcome depend on map iteration order.
	subjectKind := subjectKindOf(args)
	// Resolve a supplied-but-unknown key to an accepted parameter, or "".
	// Orthographic variants are always safe — same word, same meaning. Semantic
	// aliases additionally require the value to be a bare name, so a qualified
	// form is never silently reinterpreted.
	resolve := func(key string, val json.RawMessage) string {
		if alias, ok := perToolAliases[tool][strings.ToLower(key)]; ok && isAccepted(alias) {
			// Only the subject NAME needs the value check. Aliases targeting
			// `kind` carry an enum ("ServiceAccount", "User", "Group"), which is
			// not a resource name — validating it as one would reject every
			// correct call.
			if alias == "name" && !aliasValueKeepsMeaning(subjectKind, val) {
				return ""
			}
			return alias
		}
		// Compare against spellings of the ACCEPTED names, not of the supplied
		// key. toSnake and toCamel do not round-trip through numeric segments —
		// toCamel("diff_revision_1") is "diffRevision1", but toSnake of that is
		// "diff_revision1" — so deriving candidates from the caller's key silently
		// misses real parameters. Deriving them from the accepted set cannot.
		for _, p := range accepted {
			if p != key && (toCamel(p) == key || toSnake(p) == key) {
				return p
			}
		}
		return ""
	}

	for key, val := range args {
		if isAccepted(key) {
			continue
		}
		target := resolve(key, val)
		if target == "" {
			unresolved = append(unresolved, key)
			continue
		}
		// Never clobber a value the caller supplied under the correct name.
		if _, taken := args[target]; taken {
			unresolved = append(unresolved, key)
			continue
		}
		delete(args, key)
		args[target] = val
		repairs = append(repairs, fmt.Sprintf("%s->%s", key, target))
	}
	// Missing required arguments are the other guaranteed validation failure.
	// Detecting them here means the help attaches without parsing SDK text.
	_, required := lookupToolParams(tool)
	for _, req := range required {
		if _, ok := args[req]; !ok {
			unresolved = append(unresolved, "(missing "+req+")")
		}
	}
	slices.Sort(repairs)
	slices.Sort(unresolved)

	if len(repairs) == 0 {
		return raw, nil, unresolved
	}
	fixed, err := json.Marshal(args)
	if err != nil {
		return raw, nil, unresolved
	}
	return fixed, repairs, unresolved
}

// describeToolParams renders the accepted arguments for a tool, required first.
func describeToolParams(tool string) string {
	if !isRegisteredTool(tool) {
		return ""
	}
	accepted, required := lookupToolParams(tool)
	if len(accepted) == 0 {
		return fmt.Sprintf("\n\n%s accepts no arguments. Retry with an empty object.", tool)
	}
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

		fixed, repairs, unresolved := repairToolArgs(call.Params.Name, call.Params.Arguments)
		if repairs != nil {
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
			annotateSchemaError(call.Params.Name, out, unresolved)
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

// annotateSchemaError appends the accepted argument names to an argument-shape
// failure, so the caller can correct itself instead of guessing again.
//
// `unresolved` is the authoritative signal: those are argument names the caller
// supplied that this tool does not accept and repair could not map, so the SDK
// is certain to reject the call. Keying off that rather than the SDK's rendered
// message means a wording change upstream cannot silently disable the help, and
// a tool's own domain error can never be mistaken for a schema failure.
//
// The text match is a secondary path for validation failures we did not predict
// (wrong type, missing required field), where the SDK's wording is the only
// signal available.
func annotateSchemaError(tool string, res *mcpsdk.CallToolResult, unresolved []string) {
	if res == nil || !res.IsError {
		return
	}
	help := describeToolParams(tool)
	if help == "" {
		return
	}
	for i, c := range res.Content {
		text, ok := c.(*mcpsdk.TextContent)
		if !ok {
			continue
		}
		if strings.Contains(text.Text, "accepts:") {
			continue // already annotated
		}
		if len(unresolved) == 0 && !strings.Contains(text.Text, `validating "arguments"`) {
			continue
		}
		res.Content[i] = &mcpsdk.TextContent{Text: text.Text + help}
	}
}
