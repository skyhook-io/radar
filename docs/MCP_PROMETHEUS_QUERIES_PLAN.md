# Plan: MCP Tools — Prometheus Queries

Task: expose Prometheus querying through Radar's built-in MCP server, so AI assistants
(Claude Code, Cursor, etc.) can run PromQL against the cluster's Prometheus through Radar —
reusing Radar's existing discovery, auth headers, and RBAC plumbing.

Status: **implemented** (2026-06-11). Kept as the design record — decisions and
rationale in §3/§6. Design review (grill session 2026-06-11) resolved all open
questions inline; post-implementation multi-agent review hardened discovery
limits, step math, and error contracts beyond what's written below.

---

## 1. Context — what already exists

Radar already has both halves of this feature; they are just not connected:

| Piece | Where | Notes |
|---|---|---|
| MCP server (official `modelcontextprotocol/go-sdk v1.6.1`) | `internal/mcp/server.go`, `/mcp` Streamable HTTP, stateless | Tools registered in `registerTools()` (`internal/mcp/tools.go:40`) |
| Prometheus client | `pkg/prom/client.go` | `Query(ctx, promQL)`, `QueryRange(ctx, promQL, start, end, step)`, `Probe(ctx)` → `QueryResult{ResultType, Series[]{Labels, DataPoints}}` |
| Transport w/ auth headers + 10MiB cap | `pkg/prom/transport.go` | `Do(ctx, method, path, params)` — generic, can reach any `/api/v1/*` path |
| App-level singleton + discovery | `internal/prometheus/client.go`, `discovery.go` | `GetClient()`, `EnsureConnected(ctx)`, manual URL → port-forward reuse → well-known → scored dynamic discovery |
| Config | `--prometheus-url`, `--prometheus-header(-from-env)`, `~/.radar/config.json` | Already documented in `docs/configuration.md` |
| REST endpoints for the frontend charts | `internal/prometheus/handlers.go` | incl. a raw `GET /prometheus/query` (instant/range via `?type=`), range presets `10m…14d` with auto step |
| Canned PromQL builders | `pkg/prom/queries.go` | CPU/memory/network/fs/restarts per kind + `SanitizeLabelValue` / `EscapeRegexMeta` |
| Auth gate for prometheus REST | `internal/server/server.go:387-409` → `prometheuspkg.SetAuthGate` | SAR-based `canRead` + namespace allow-list for derived endpoints |
| MCP RBAC helpers | `internal/mcp/permissions.go` | `filterNamespacesForUser(ctx, requested)`, `canReadClusterScopedKind(ctx, kind, group, verb)` |

**The gap:** no MCP tool can touch Prometheus. An AI assistant connected to Radar can see
`top_resources` (metrics-server) but cannot run PromQL, discover metric names, or pull
history — which is what incident investigation actually needs.

---

## 2. Tool surface (v1)

Two read-only tools. Keep the catalog small — Radar prefers consolidated tools with strong
routing descriptions over many micro-tools.

### 2.1 `query_prometheus`

One tool for instant + range queries (mirrors the existing REST `/prometheus/query` shape).

Input struct (`internal/mcp/tools_prometheus.go`):

```go
type queryPrometheusInput struct {
    Query string `json:"query" jsonschema:"PromQL query to execute. For metrics with many series (>10), wrap with topk(5, ...) to bound the result. Use discover_metrics first when unsure of metric or label names."`
    Type  string `json:"type,omitempty" jsonschema:"instant (default) for current values, or range for time series history"`
    Since string `json:"since,omitempty" jsonschema:"range queries: how far back to look, e.g. 30m, 1h, 24h (default 1h). Ignored for instant"`
    Start string `json:"start,omitempty" jsonschema:"range queries: RFC3339 start time. Overrides since when set (use with end to zoom into an incident window)"`
    End   string `json:"end,omitempty" jsonschema:"range queries: RFC3339 end time (default now)"`
    Step  string `json:"step,omitempty" jsonschema:"range queries: resolution like 30s, 5m. Omit to auto-calculate; the server lowers resolution when the result would exceed the point budget"`
    MaxPoints int `json:"max_points,omitempty" jsonschema:"range queries: max data points per series (default 300, max 600). Raise only for 1-3 series when hunting short spikes; narrow the window instead when possible"`
}
```

Behavior:

- Resolve the singleton: `prometheus.GetClient()` → `EnsureConnected(ctx)`. If not
  connected, return an actionable error: status + reason from `GetStatus()` + hint
  ("set --prometheus-url or check Settings → Prometheus").
- `type=instant` → `client.Query`; `type=range` → resolve window (`start/end` win over
  `since`, default 1h), auto-step (§3.2), then `client.QueryRange`.
- Window validation: reject `start > end` and unparseable times with an actionable error.
  **No max-window cap** — auto-step bounds the response, the 30s ctx (§3.5) bounds the cost,
  and a cap would break long-retention backends (Thanos/Mimir month-over-month questions).
- Successful-but-empty results are NOT silent: return
  `{"resultType": ..., "series": [], "note": "query returned no data — verify metric and label names with discover_metrics"}`.
- Oversized results return a **summary instead of data** (§3.3).
- Response shape: `{query, type, start, end, step, resultType, seriesCount, series, truncated?, summary?}`
  via `toJSONResult`. Echo the executed query/window so the model can self-correct.

### 2.2 `discover_metrics`

Metric-name and label-value discovery — the anti-hallucination tool. Wraps the two
Prometheus metadata APIs behind one tool:

```go
type discoverMetricsInput struct {
    Match string `json:"match,omitempty" jsonschema:"PromQL series selector to filter, e.g. {__name__=~\"node_cpu.*|node_memory.*\"} or {namespace=\"payments\"}. Combine patterns with regex | to reduce calls. REQUIRED when label is empty (unfiltered metric-name listing is rarely useful)"`
    Label string `json:"label,omitempty" jsonschema:"discover values of this label instead of metric names, e.g. namespace, pod, job, instance"`
    Limit int    `json:"limit,omitempty" jsonschema:"max values returned (default 100, max 500)"`
}
```

Behavior:

- `label==""` → `GET /api/v1/label/__name__/values` with `match[]` (**required**, reject
  empty match with a guidance error — this is the HolmesGPT lesson: unbounded name listing
  returns junk and burns tokens). **Enrich the matched names** with `/api/v1/metadata`
  (one extra call, `metric` filter not needed — fetch once, join in memory): return
  `[{name, type, help}]` instead of bare names. Counter-vs-gauge is what the model needs
  to know to write `rate()` correctly; names alone don't carry it. Metrics missing from
  metadata (recording rules, remote-write) get `type: ""` — never drop them from the list.
- `label!=""` → `GET /api/v1/label/{label}/values`, `match[]` optional.
- Always send `limit` (default 100) and a lookback window (`start` = now-1h) so dead series
  don't bloat results.
- When exactly `limit` values come back, set `"truncated": true` +
  `"note": "use a more specific match selector"`.

### Phase 2 — partially shipped (2026-06-12, HolmesGPT comparison follow-up)

Shipped beyond v1:

- **`get_prometheus_rules`** — `/api/v1/rules` flattened to per-rule entries (group
  attached); `type` sent server-side AND re-applied client-side (old backends ignore the
  param — same lesson as the discovery limit); `name`/`group` case-insensitive substring +
  `state` filters client-side; limit 50/200; 404 → actionable "backend has no rules API".
- **Model-settable `timeout`** on `query_prometheus` (default 30s, clamp 180s, log on
  clamp) — `http.Client` backstop raised 60s → 200s (= 180s MCP max + 20s margin) so the
  handler ctx still always wins.

**Guidance lives in tool descriptions + response notes, not server `Instructions`.** A
condensed HolmesGPT playbook was prototyped as an MCP-init `Instructions` block, then
reverted: it duplicated content already in the tool descriptions (counter→`rate()`,
must-`topk`, discover-first, rules-`FIRST`) over a weaker channel that every session pays
for. The high-value bits are delivered just-in-time instead:
- `discover_metrics` name-mode sets `usage` ("counters … wrap in `rate(metric[5m])`") when
  any result is a counter — the moment before the model composes a query.
- `query_prometheus` oversized-result `note` carries the never-answer-from-truncated rule +
  points at `labelCardinality` for which label to constrain.
- `get_prometheus_rules` description carries the rules-first alert-investigation flow.
The purely-reference tips (metric cheat-sheet, `_sum`/`_count` over `_bucket`) were dropped
as low-value — models know them.

Still out of scope:

- `/api/v1/series` passthrough. (`/api/v1/metadata` is consumed internally by
  `discover_metrics` enrichment — a raw passthrough tool remains out of scope.)
- Canned convenience queries (rightsizing/P95) — already partly served by `top_resources`
  and `get_resource include=metrics`.

---

## 3. Guardrails (design decisions)

These come from studying HolmesGPT's Prometheus toolset (`holmes/plugins/toolsets/prometheus/`),
which solved the same LLM-vs-Prometheus problems in production. Port the *behaviors*, not the code.

### 3.1 Required `match` on discovery

`discover_metrics` without `label` MUST require `match`. Hardcode `limit` ≤ 500.
Flag truncation explicitly (`truncated: true` + note) so the model narrows instead of
assuming completeness.

### 3.2 Auto step adjustment (range queries)

```
points = window_seconds / step
if points > maxPoints: step = window_seconds / maxPoints
```

- Default `maxPoints` 300, hard cap 600 (clamp silently, log when clamped).
- When `step` omitted: `step = max(15s, window/maxPoints)`.
- Tool description tells the model: raise `max_points` only for ≤3 series; otherwise zoom
  the window.

### 3.3 Response-size cap with summary fallback

The MCP server cannot know the client LLM's context window → use a fixed budget:

- After marshaling the series payload, if `len(bytes) > maxPromResponseBytes`
  (const, **64KiB default**, env override `RADAR_MCP_PROM_MAX_RESPONSE_BYTES`), drop the
  raw series and return instead:

```json
{
  "summary": {
    "seriesCount": 1742,
    "totalDataPoints": 522600,
    "labelCardinality": {"pod": 1742, "namespace": 31, "container": 8},
    "suggestion": "topk(5, <original query>)"
  },
  "truncated": true
}
```

- `labelCardinality` (distinct values per label, sorted desc) tells the model *which* label
  explodes the result; the `suggestion` gives it a ready retry. This self-correction loop is
  the single highest-value guardrail.
- Never return partially-truncated series data (a model answering from silently cut data is
  worse than no data).

### 3.4 Error contract

Every failure path must let the model self-correct (same rule as the REST handlers, stricter):

- PromQL 400 → include Prometheus's own error body (it names the parse position) + the query.
- Connection/transport errors → include the address tried (`Transport.Address()`) and the
  `ProbeReason` vocabulary from `pkg/prom/client.go` when relevant.
- Timeouts → say what timed out and suggest narrowing the window or simplifying the query.

### 3.5 Timeouts (RESOLVED — wiring change required)

Facts established during review: `/mcp` is mounted on the root router **outside** the
`/api` 60s timeout middleware (`internal/server/server.go:527` vs `:260`), so MCP handlers
have no harness timeout. The shared `http.Client{Timeout: 10s}` in
`internal/prometheus/client.go:62,147` is the *only* timeout on the query path today —
sized for canned dashboard charts, not arbitrary LLM PromQL (Grafana/VictoriaMetrics
default 30s; Prometheus server kills at 2m).

Decision:

- **Raise the shared `http.Client.Timeout` to 60s** (both construction sites in
  `internal/prometheus/client.go`). It becomes a pure hang backstop (dead TCP, stalled
  body reads) and matches the REST middleware cap, so REST semantics stay coherent:
  middleware (60s) governs, client (60s) backstops.
- **`context.WithTimeout(30s)` in both MCP handlers.** The handler ctx always fires
  before the client timeout, so the model always gets *our* actionable error (§3.4),
  never the generic `Client.Timeout exceeded` transport error.
- Do not expose a timeout parameter in v1 (HolmesGPT does; Radar's tool surface stays lean —
  add later if real queries hit the ceiling).

### 3.6 Auth / RBAC stance (RESOLVED — parity with REST)

- Both tools: `readOnly` annotations (`ReadOnlyHint: true, OpenWorldHint: false`) — same as
  the other read tools.
- **PromQL cannot be namespace-scoped server-side** (arbitrary queries can aggregate across
  namespaces). The REST raw `/prometheus/query` endpoint already accepts this: any
  authenticated user may query; only *derived* endpoints (rightsizing/PVC) do SAR checks.
  v1 keeps parity with that decision: authenticated MCP session ⇒ may query Prometheus.
- Make this explicit in `docs/mcp.md` Security section: *"metric data is not
  namespace-filtered; deploy with auth when Prometheus contains sensitive label values"*.
- Decision point for review: if the SSO deployment needs tenant isolation, the cheap
  follow-up is gating both tools on `canReadClusterScopedKind(ctx, "nodes", "", "list")`
  (proxy for "cluster-level observer") — leave a `// TODO` hook, do not invent per-query
  label injection in v1.

---

## 4. Implementation steps

### Step 1 — extend `pkg/prom` with label-values + metadata support

`pkg/prom/client.go` (new methods; transport already generic):

```go
// LabelValues returns values for a label via /api/v1/label/{label}/values.
// match entries are PromQL series selectors; zero start/end skip the window params.
func (c *Client) LabelValues(ctx context.Context, label string, matches []string, start, end time.Time, limit int) ([]string, error)

// Metadata returns metric metadata via /api/v1/metadata: name → {type, help}.
// Used to enrich discover_metrics name listings (counter vs gauge → rate() guidance).
func (c *Client) Metadata(ctx context.Context) (map[string]MetricMetadata, error)
```

- Build `url.Values` with `match[]` (repeated), `start`/`end` (unix), `limit`.
- Parse `{"status":"success","data":[...]}`; non-success → error including body.
- Metadata join is best-effort: on metadata fetch error, return names with empty
  type/help rather than failing discovery.
- Unit tests alongside existing `pkg/prom/client_test.go` (httptest server).

**Step 1b — timeout wiring (§3.5):** bump `http.Client{Timeout: 10s}` → 60s at both
construction sites in `internal/prometheus/client.go` (`Initialize`, `Reinitialize`).

### Step 2 — `internal/mcp/tools_prometheus.go`

New file, following the `tools_helm.go` / `tools_audit.go` pattern:

- Input structs from §2 (with `jsonschema` tags exactly in house style — guidance phrased
  for routing, defaults stated).
- `handleQueryPrometheus`, `handleDiscoverMetrics` with signature
  `func(ctx context.Context, req *mcp.CallToolRequest, input T) (*mcp.CallToolResult, any, error)`.
- Helpers in the same file: `resolveRange(since, start, end)` (rejects start>end),
  `adjustStep(window, step, maxPoints)`, `summarizeLargeResult(result, query)` (§3.3),
  `promNotConnectedError(status)`.
- Both handlers wrap with `context.WithTimeout(ctx, 30*time.Second)` (§3.5).
- Return via `toJSONResult` (`internal/mcp/tools.go:2742`).

### Step 3 — register the tools

In `registerTools` (`internal/mcp/tools.go:40`), new section `// --- Prometheus tools (read-only) ---`:

```go
mcp.AddTool(server, &mcp.Tool{
    Name: "query_prometheus",
    Description: "Use when the question needs metric VALUES or history: CPU/memory over time, " +
        "request rates, error ratios, saturation, restarts trend, 'was there a spike?'. Executes " +
        "PromQL against the cluster's Prometheus (auto-discovered or configured in Radar; also " +
        "works with PromQL-compatible backends: Thanos, VictoriaMetrics, Mimir). " +
        "type=instant returns current values; type=range returns time series for a window " +
        "(since=1h default). For live top-N snapshots prefer top_resources; for metric/label " +
        "NAME discovery use discover_metrics first — do not guess metric names. " +
        "High-cardinality queries must be wrapped in topk(5, ...): oversized results return a " +
        "summary with a suggested rewrite instead of data.",
    Annotations: readOnly,
}, logToolCall("query_prometheus", handleQueryPrometheus))

mcp.AddTool(server, &mcp.Tool{
    Name: "discover_metrics",
    Description: "Use BEFORE query_prometheus when unsure of exact metric or label names. " +
        "Lists metric names matching a selector (match={__name__=~\"node_cpu.*\"}) or values " +
        "of one label (label=namespace). Returns up to limit values from the last hour of " +
        "active series; truncated=true means narrow the match. Never answer about metric " +
        "values from this tool — names prove existence, not magnitude.",
    Annotations: readOnly,
}, logToolCall("discover_metrics", handleDiscoverMetrics))
```

(Descriptions above are first drafts — keep the routing-guidance style of the neighbors.)

### Step 4 — catalog + docs sync (CI-enforced)

- `web/src/components/home/mcpToolCatalog.ts`: add both entries (short human-facing `desc`,
  `params` list). `TestSetupDialogCoversAllTools` fails CI otherwise
  (`internal/mcp/tools_catalog_test.go`).
- `docs/mcp.md`: add rows to the Read Tools table; extend the Security section per §3.6.

### Step 5 — tests (`internal/mcp/tools_prometheus_test.go` + `pkg/prom` tests)

Use `httptest.NewServer` faking the Prometheus API (existing pattern in `pkg/prom/client_test.go`):

1. instant query happy path — response echoes query, seriesCount correct
2. range query: step auto-adjust (1h window, no step → ≤300 points; absurd step → clamped)
3. `max_points` clamp at 600
4. empty result → `note` present, not an error
5. oversized result → summary path: `truncated: true`, `labelCardinality` ordered,
   `suggestion` contains `topk(5,` + original query
6. PromQL error (400) → error contains Prometheus body + query
7. not-connected → actionable error mentioning configuration
8. `discover_metrics`: missing match+label rejected; `match[]`/`limit`/window params sent;
   truncation flag at exactly `limit` results
9. `LabelValues` + `Metadata` unit tests in `pkg/prom`
10. `start > end` and unparseable times rejected with actionable error
11. metric-name discovery enriched with type/help; metadata fetch failure degrades to
    bare names (not an error)

Run: `go test ./internal/mcp/... ./pkg/prom/...` and `make test`.

### Step 6 — manual verification

```bash
make build && ./radar                      # against a cluster with kube-prometheus-stack
claude mcp add radar --transport http http://localhost:9280/mcp
```

Prompts to exercise: "what's the p95 CPU of the payments pods over the last 6h?",
"find which namespace is using the most memory right now", "is there a metric tracking
HTTP 5xx for the checkout service?" — verify discovery→query routing, the summary fallback
(query `container_cpu_usage_seconds_total` bare on a big cluster), and the not-connected error
(`radar --prometheus-url http://localhost:1` to force failure).

Also verify `radar --mcp-catalog-only --no-browser` lists the new tools (registry introspection).

---

## 5. Acceptance criteria

- [ ] `query_prometheus` (instant + range) and `discover_metrics` registered, read-only annotated
- [ ] Works against auto-discovered Prometheus AND `--prometheus-url` with auth headers
- [ ] Range step auto-adjusts; points-per-series never exceeds 600; `start > end` rejected
- [ ] Responses > 64KiB return cardinality summary + `topk` suggestion, never raw truncation
- [ ] Metric-name discovery returns `{name, type, help}`; degrades gracefully without metadata
- [ ] Shared `http.Client` at 60s backstop; MCP handlers enforce 30s ctx; timeout error is
      the §3.4 actionable message, not the transport error
- [ ] Empty results, parse errors, timeouts, and not-connected each return distinct, actionable messages
- [ ] `TestSetupDialogCoversAllTools` green (catalog updated); `docs/mcp.md` updated
- [ ] Unit tests for step adjustment, summary fallback, discovery limits, LabelValues, Metadata
- [ ] `make test` + `make build` pass; manual smoke via Claude Code done

## 6. Open questions — RESOLVED (design review 2026-06-11)

1. **Tenant isolation under SSO** → **parity with REST** (any authenticated user may query);
   TODO hook for a cluster-scoped SAR gate, documented in `docs/mcp.md` Security (§3.6).
2. **Naming** → **`query_prometheus` / `discover_metrics`**. The input IS PromQL; models
   route better on "Prometheus" and a generic `query_metrics` would blur against
   `top_resources`. Compatibility line in the description covers Thanos/VictoriaMetrics/Mimir.
3. **Response budget** → **64KiB + env override** confirmed. Revisit with `agentlog` token
   estimates after rollout.

Decisions made beyond the original open questions:

4. **Timeouts** → 60s shared-client backstop + 30s handler ctx (§3.5).
5. **Empty results** → success + note, NOT HolmesGPT's hard error — emptiness is sometimes
   the true answer ("any 5xx in the last hour?"), and a hard error there pushes the model
   into pointless retries.
6. **Window validation** → order check only; no max-window cap (§2.1).
7. **No `description` param in v1** — considered (HolmesGPT requires one for intent/audit
   one-liners) but dropped: agentlog already records the full PromQL, which is the intent.
8. **Metadata enrichment in v1** — `discover_metrics` name mode returns `{name, type, help}`
   so the model knows counter vs gauge without a third tool (§2.2).

## 7. References

- Tool registration & helpers: `internal/mcp/tools.go` (`registerTools:40`, `toJSONResult:2742`),
  `internal/mcp/agentlog.go` (`logToolCall:120`)
- Prometheus plumbing: `pkg/prom/{client,transport,queries,discovery}.go`,
  `internal/prometheus/{client,discovery,handlers,auth}.go`
- Catalog sync: `internal/mcp/tools_catalog_test.go`, `web/src/components/home/mcpToolCatalog.ts`
- Pattern source (guardrails): HolmesGPT `holmes/plugins/toolsets/prometheus/prometheus.py` —
  required-match discovery (`GetMetricNames`), step adjustment (`adjust_step_for_max_points`),
  summary-instead-of-data (`create_data_summary_for_large_result`), LLM-facing instructions
  (`prometheus_instructions.jinja2`)
