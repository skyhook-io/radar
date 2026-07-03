# API Conventions

Cross-cutting contracts that apply to multiple REST endpoints. Endpoint
routes themselves live in `internal/server/server.go` (the single source of
truth); this file defines shared vocabulary those endpoints reference.

## `include` — response body verbosity

List endpoints that return object bodies accept an `include` query parameter
selecting **how much of each object's body ships**. It is a scalar scale,
not a field selector:

| Value | Meaning |
|---|---|
| `raw` (or absent) | Full objects, unmodified. The default — omitting `include` is always safe. |
| `summary` | Same shape, lighter: heavy subtrees are removed per a **kind-specific server-side profile**. Kinds without a profile return full objects. |
| `none` | No object bodies (where an endpoint supports it — e.g. search results without body context). |

### The `summary` contract

- **Same schema, fewer fields** — a summarized object is the original object
  minus profiled subtrees, never a transformed representation. (Transformed
  micro-shapes exist separately in the AI/MCP context layer — `pkg/ai/context`
  verbosity levels — which is a different surface with its own vocabulary.)
- **Consumers may rely on the presence of profiled kinds' keep-list fields,
  never on the absence of anything.** Growing a keep-list is non-breaking;
  shrinking one is breaking and must update the contract tests that pin it.
- Profiles are declarative data over the shared `pkg/prune` mechanism; each
  new profile requires a consumer field-inventory and a contract test
  (see `internal/server/resource_summary.go` for the pattern).

Current adopters: `GET /api/search` (`none|summary|raw`),
`GET /api/resources/{kind}` (`summary|raw`).
