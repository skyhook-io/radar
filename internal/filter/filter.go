// Package filter compiles and evaluates CEL boolean expressions used
// by /api/search and /api/issues to apply structured predicates over
// K8s objects and Issue rows.
//
// Why CEL: bounded execution (no DoS from a hallucinated expression),
// type-checked at compile time, K8s-ecosystem alignment, and 5-10×
// faster evaluation than gojq at scale. The downside (less LLM
// fluency cold) is offset by a small primer in the MCP tool
// description and the schema-aware error messages CEL produces when
// the agent misnames a field.
//
// Two compile entry points:
//
//	CompileObjectFilter — bindings shaped to a K8s object:
//	  kind, apiVersion, metadata, spec, status, labels, annotations
//
//	CompileIssueFilter — bindings shaped to an issues.Issue:
//	  severity, source, category, category_group, kind, group, ns,
//	  name, reason, message, count, first_seen, last_seen, grouping_scope,
//	  restart_count, last_terminated_reason
//
// Both return a Filter whose Match(activation) yields (bool, error).
// Compile errors are returned verbatim (CEL's parser produces
// position-tagged messages — pass them through to the caller, they're
// the most actionable thing the LLM gets).
package filter

import (
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/google/cel-go/cel"
	"github.com/google/cel-go/common/types"
	"github.com/skyhook-io/radar/pkg/issuesapi"
)

// Filter is a compiled boolean predicate.
type Filter struct {
	expr    string
	program cel.Program
}

// Source returns the original expression string. Useful for error
// messages and cache keys.
func (f *Filter) Source() string { return f.expr }

// Match evaluates the filter against the activation map. The map is
// a flat top-level binding (e.g. {"kind": "Pod", "metadata": {...}}).
// Returns (false, nil) when the filter evaluates to a falsy non-error
// value, (true, nil) when truthy, and (false, err) on a runtime error
// — callers typically treat eval errors as "this object doesn't match"
// and continue, so the user sees zero results rather than an opaque
// 500. The bool returned in the error case is always false.
func (f *Filter) Match(activation map[string]any) (bool, error) {
	out, _, err := f.program.Eval(activation)
	if err != nil {
		return false, fmt.Errorf("eval: %w", err)
	}
	b, ok := out.(types.Bool)
	if !ok {
		return false, fmt.Errorf("filter must return bool, got %s", out.Type().TypeName())
	}
	return bool(b), nil
}

// envObject is the CEL environment for K8s-object filters. Built once
// and reused — building takes a few hundred microseconds and creating
// a new env per request would dominate evaluation cost on repeat
// queries.
//
// MIRROR: radar-hub/internal/server/celcheck.go declares the same
// bindings (celObjectEnv) for fail-fast pre-validation at the hub.
// If you change the binding list, update both places in lockstep —
// there's no shared package and no compile-time check that catches
// drift. Hub's TestCelEnv_ObjectDeclarations exercises the bindings
// from its side; radar's filter_test.go does the same here.
var envObject = mustNewEnv(
	cel.Variable("kind", cel.StringType),
	cel.Variable("apiVersion", cel.StringType),
	cel.Variable("metadata", cel.DynType),
	cel.Variable("spec", cel.DynType),
	cel.Variable("status", cel.DynType),
	cel.Variable("labels", cel.MapType(cel.StringType, cel.StringType)),
	cel.Variable("annotations", cel.MapType(cel.StringType, cel.StringType)),
)

// envIssue is the CEL environment for /api/issues filters. The shape
// mirrors issues.Issue's JSON form so an LLM that's seen one row of
// output can write a filter against it without docs.
// envIssue uses `ns` instead of `namespace` because the latter is a
// CEL reserved identifier — bare references like `namespace == "x"`
// fail at parse time even when the variable is declared. We took the
// short form rather than fight cel-go's reservation list; `ns:` is
// also the short modifier in the search query parser, so the two
// surfaces stay parallel.
var envIssue = mustNewEnv(issueCELVariables()...)

func issueCELVariables() []cel.EnvOption {
	out := make([]cel.EnvOption, 0, len(issuesapi.CELBindings))
	for _, b := range issuesapi.CELBindings {
		switch b.Type {
		case issuesapi.BindingString:
			out = append(out, cel.Variable(b.Name, cel.StringType))
		case issuesapi.BindingInt:
			out = append(out, cel.Variable(b.Name, cel.IntType))
		}
	}
	return out
}

func mustNewEnv(opts ...cel.EnvOption) *cel.Env {
	env, err := cel.NewEnv(opts...)
	if err != nil {
		panic(fmt.Sprintf("cel.NewEnv: %v", err))
	}
	return env
}

// CompileObjectFilter compiles a CEL expression that runs against a
// K8s object's bindings (kind, metadata, spec, status, labels,
// annotations). Returns the compiler's diagnostic verbatim on parse
// or type-check failure — that's what the agent retries against.
func CompileObjectFilter(expr string) (*Filter, error) {
	return compileWith(envObject, expr)
}

// CompileIssueFilter compiles a CEL expression against the Issue
// row bindings (severity, source, kind, …, count, last_seen).
func CompileIssueFilter(expr string) (*Filter, error) {
	return compileWith(envIssue, expr)
}

func compileWith(env *cel.Env, expr string) (*Filter, error) {
	if expr == "" {
		return nil, errors.New("empty filter expression")
	}
	ast, issues := env.Compile(expr)
	if issues != nil && issues.Err() != nil {
		return nil, issues.Err()
	}
	if ast.OutputType() != cel.BoolType {
		return nil, fmt.Errorf("filter must return bool, got %s", ast.OutputType().String())
	}
	prg, err := env.Program(
		ast,
		// Cap evaluation cost. CEL is bounded by language design but
		// `..` (recursive descent) doesn't exist; the cost limit
		// guards against pathological-but-legal expressions like
		// deeply-nested macros.
		cel.CostLimit(1_000_000),
	)
	if err != nil {
		return nil, fmt.Errorf("program: %w", err)
	}
	return &Filter{expr: expr, program: prg}, nil
}

// ---------------------------------------------------------------------------
// LRU-ish cache for compiled programs.
//
// Caching hot expressions matters: for an LLM that fan-outs a fleet
// query and re-issues with the same filter on refetch, paying the
// compile cost (~200µs) per request adds up. Cache compiled programs
// keyed on (envName + expression).
//
// The map is bounded to maxCacheEntries; once full, we evict the
// oldest entries by insertion time. Not strict LRU — the access
// pattern here is "compile-once, eval-many" so we don't need to
// re-rank on hit. Entries are also TTL'd so an expression we
// haven't seen in a while drops out instead of pinning forever.
// ---------------------------------------------------------------------------

const (
	// maxCacheEntries — 256 covers typical agent + UI working sets
	// while keeping the O(n) eviction sweep fast. Tuned by intuition;
	// revisit when telemetry exists.
	maxCacheEntries = 256
	// cacheTTL — 1h roughly matches a typical AI agent session.
	// Beyond that a "stale" compile is no real harm (the same
	// expression compiles to the same program) but bounding the
	// working set keeps the memory profile flat for long-lived hubs.
	cacheTTL = 1 * time.Hour
)

type cacheEntry struct {
	filter *Filter
	added  time.Time
}

type cache struct {
	mu      sync.Mutex
	entries map[string]cacheEntry // key = "obj:" or "issue:" + expr
}

var defaultCache = &cache{entries: map[string]cacheEntry{}}

func (c *cache) get(key string) *Filter {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	if !ok {
		return nil
	}
	if time.Since(e.added) > cacheTTL {
		delete(c.entries, key)
		return nil
	}
	return e.filter
}

func (c *cache) put(key string, f *Filter) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.entries) >= maxCacheEntries {
		// Evict the oldest entry. O(n) but n=256 and this happens
		// on miss only — cheaper than a heap in practice.
		var oldestKey string
		var oldestAt time.Time
		for k, e := range c.entries {
			if oldestKey == "" || e.added.Before(oldestAt) {
				oldestKey = k
				oldestAt = e.added
			}
		}
		delete(c.entries, oldestKey)
	}
	c.entries[key] = cacheEntry{filter: f, added: time.Now()}
}

// CachedObjectFilter compiles or returns the cached compilation of an
// object-scoped filter expression. Caller-facing wrapper most callers
// should use.
func CachedObjectFilter(expr string) (*Filter, error) {
	if expr == "" {
		return nil, nil
	}
	key := "obj:" + expr
	if f := defaultCache.get(key); f != nil {
		return f, nil
	}
	f, err := CompileObjectFilter(expr)
	if err != nil {
		return nil, err
	}
	defaultCache.put(key, f)
	return f, nil
}

// CachedIssueFilter is the issue-scoped twin of CachedObjectFilter.
func CachedIssueFilter(expr string) (*Filter, error) {
	if expr == "" {
		return nil, nil
	}
	key := "issue:" + expr
	if f := defaultCache.get(key); f != nil {
		return f, nil
	}
	f, err := CompileIssueFilter(expr)
	if err != nil {
		return nil, err
	}
	defaultCache.put(key, f)
	return f, nil
}
