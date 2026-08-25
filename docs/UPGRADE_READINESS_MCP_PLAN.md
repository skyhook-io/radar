# Plan: Expose Upgrade Impact Analysis via MCP

Status: **proposal** — no code yet. This document is the design to agree on before implementation.

## Background

PR [#1195](https://github.com/skyhook-io/radar/pull/1195) shipped Kubernetes upgrade impact
analysis as an HTTP endpoint (`GET /api/upgrade-readiness?target={major.minor}`) and the
**Checks → Upgrade impact** UI. The MCP server was not part of that PR's surface sweep (UI,
Cloud RBAC, Hub navigation, README, radar-docs) and is not on its deferred list — the gap is
an oversight, not a decision.

### Why close it

- **Upgrade planning is an agent-shaped task.** The output of a scan is not something you
  watch — it's something you turn into a runbook, a ticket backlog, and a series of manifest
  fixes. That transformation is what agents do, and today they can only get there by
  reconstructing the analysis from raw kubectl, badly — the exact failure mode
  [docs/mcp.md](mcp.md) argues against.
- **The remediation loop closes inside Radar.** Findings carry `resource`, `evidence.path`,
  and `remediation`. Roughly half the catalog is source-manifest problems (deprecated APIs in
  Helm/last-applied manifests, non-canonical IP/CIDR, `gitRepo` volumes, `externalIPs`).
  Radar already ships `apply_resource` / `patch_resource`. *Read finding → patch manifest* is
  a workflow no other tool in the catalog enables.
- **Positioning.** The repo describes itself as "the missing open-source Kubernetes UI with a
  built-in MCP server for AI agents". A flagship analysis that agents cannot reach undercuts
  that.

### Why it is not a trivial wire-up

Three properties of the feature constrain the design; each maps to a section below.

1. **Cost** — a scan is an uncached live analysis (Helm secrets across namespaces, apiserver
   `/metrics`, per-node kubelet metrics behind `nodes/proxy`, dynamic informer syncs).
   #1195 itself flags benchmarking before making it a default workflow. Agents call tools
   speculatively and in loops. → memoization is a prerequisite (§4).
2. **Size** — `ScanResults` is 18 check families × findings × evidence; raw JSON would blow
   an agent's context. → tiered output (§2).
3. **Semantics** — the entire thesis of #1195 is that partial coverage is not a readiness
   guarantee: `unknown` (incomplete) is orthogonal to severity, `no_access` is an explicit
   product contract, absence is never an inferred pass. An LLM will flatten this to "you're
   good to go" unless the structure makes that impossible. → the certainty contract (§3).

## 1. Tool definition

One read-only tool: **`get_cluster_upgrade_readiness`**.

```
get_cluster_upgrade_readiness
  target  (required)  target Kubernetes minor, e.g. "1.34"
  check   (optional)  check id to expand, e.g. "node-drain-feasibility"
  level   (optional)  minimum finding level when expanding: blocker | warning | review
                      (default blocker — expansion returns the most severe first)
```

Design decisions:

- **A new tool, not a `get_cluster_audit` parameter.** Different severity vocabulary
  (`blocker`/`warning`/`review` vs the Checks posture ladder), different cost class (live
  collection vs informer cache), different scope contract (deliberately cluster-wide vs
  namespace-filtered), and a required `target` that audit has no concept of. Folding them
  together would corrupt a tool whose contract already takes effort to explain.
- **No namespace parameter.** Matches the HTTP surface: an upgrade affects the cluster; the
  caller's RBAC ceiling and `--namespace-scope` still bound evidence (and surface in
  `coverage`), but the tool does not offer a browsing filter that would silently narrow a
  readiness claim. The header namespace picker is likewise ignored, for the same reason
  `upgradeReadinessNamespaces` ignores it.
- **Read-only annotations** (`readOnlyHint`), like every other read tool.
- **`target` is validated** by the existing `upgradereadiness.ValidateTarget` — same-major,
  forward, at most reviewable distance; validation errors return the sentinel-mapped message
  (invalid target / non-forward / unknown current version) as a tool error, so the agent can
  self-correct.

### Tool description (draft)

The description must carry the certainty contract, because it is the only text the agent is
guaranteed to read:

> Analyze the impact of upgrading the cluster to a target Kubernetes minor version. Runs the
> evidenced check catalog (reviewed through 1.36): version skew, removed/deprecated API usage
> (live manifests, Helm sources, API-server metrics), node runtime and cgroup evidence, drain
> feasibility, admission/conversion webhook readiness, and release-specific configuration
> checks. The verdict is NOT a readiness guarantee: checks with status `unknown` had
> incomplete evidence and may hide blockers — report them alongside the verdict, never as
> passed. `coverage` describes what could actually be inspected under the caller's RBAC;
> `coverage.state: no_access` means the scan saw nothing namespaced and the verdict is
> meaningless. Default output is one row per check; pass `check=<id>` for that check's
> findings with evidence and remediation. Findings reference exact resources and fields —
> pair with `patch_resource` / `apply_resource` to fix source manifests. The first call runs
> a live cluster-wide evidence scan and can take several seconds on large clusters; results
> are briefly cached per caller, so follow-up `check=<id>` expansions of the same scan are
> cheap.

(Word count to be trimmed against the `maxCatalogBytes` budget in
`internal/mcp/tools_catalog_test.go` — raise the budget deliberately if needed rather than
gutting the semantics.)

## 2. Output shape

Two tiers, both minified structs in `internal/mcp` (not raw `ScanResults`).

### Tier 1 — overview (no `check` argument)

```jsonc
{
  "currentVersion": "1.33",
  "targetVersion": "1.34",
  "reviewedThrough": "1.36",        // catalog ceiling — targets beyond it are partially reviewed
  "verdict": "blocked",             // blocked | warning | review | no_known_blockers | unknown
  "verdictCaveat": "2 checks had incomplete evidence and may hide blockers",  // omitted only when clean
  "summary": {"blocked": 1, "warnings": 2, "reviews": 3, "passed": 9, "unknown": 2, "notApplicable": 1, "findings": 14},
  "coverage": {
    "state": "partial",             // full | partial | no_access
    "scopedNamespaces": ["team-a"], // present when evidence was namespace-bounded
    "unavailableKinds": ["ValidatingWebhookConfiguration"]
  },
  "checks": [
    {
      "id": "node-drain-feasibility",
      "title": "Node drain feasibility",
      "category": "Upgrade operations",
      "status": "blocked",
      "findings": 2,                // count only in tier 1
      "summary": "…",               // the check's one-line summary
      "caveat": "…"                 // present when evidence for this check was incomplete
    }
    // … all 18 families, ordered by required action (blocked → warning → review → unknown → passed → n/a)
  ]
}
```

Estimated size: ~2–4 KB. Every row keeps its `caveat` so incompleteness is visible at the
same level as status — an agent cannot summarize the verdict without the caveats being in
frame.

### Tier 2 — expansion (`check=<id>`)

Tier 1 header (verdict + coverage always travel with detail) plus the requested check with
findings:

```jsonc
{
  // …verdict/coverage header as above, checks omitted…
  "check": {
    "id": "manifest-api-compatibility",
    "status": "blocked",
    "summary": "…", "caveat": "…", "scope": "…", "inspected": 412,
    "findings": [
      {
        "title": "Helm manifest uses an API removed in 1.34",
        "level": "blocker",
        "resource": {"group": "", "kind": "Service", "namespace": "web", "name": "front"},
        "managedBy": {"kind": "HelmRelease", "namespace": "web", "name": "front"},
        "evidence": {"source": "Helm", "path": "…", "detail": "…"},
        "impact": "…",
        "remediation": "…"
      }
    ],
    "findingsTruncated": 12         // count withheld beyond the cap, 0 omitted
  }
}
```

Findings are capped (proposed: 25 per call, most severe first, then `level` filter) with the
withheld count reported — the policy-tool convention of never letting a cap masquerade as
completeness. References (doc URLs) are dropped from MCP output except the first per finding;
they are the least token-efficient field and the remediation text stands alone.

## 3. The certainty contract (non-negotiable invariants)

These mirror the per-value certainty discipline of [capacity.md](capacity.md) and are what
review should hold the implementation to:

1. **Verdict never travels without coverage.** Both tiers always include `coverage` and
   `verdictCaveat` when any check is `unknown`.
2. **`unknown` ≠ passed, absence ≠ pass.** The minifier must not fold `unknown` into any
   other bucket, and `summary.unknown` is always present (0 included).
3. **`no_access` short-circuits.** When `coverage.state` is `no_access` the tool returns the
   coverage object and an explicit sentence instead of a check table — same product contract
   the HTTP handler defends (see the declined Bugbot suggestion on #1195).
4. **Caps are visible.** `findingsTruncated`, `scopedNamespaces`, `unavailableKinds` are
   never silently dropped by minification.
5. **`reviewedThrough` is always present** so an agent targeting a minor beyond the catalog
   ceiling can say so.

## 4. Architecture

### 4.1 The refactor (most of the risk)

Evidence collection currently lives as `*Server` methods keyed on `*http.Request`
(`internal/server/upgrade_readiness_handler.go` + `upgrade_readiness_collectors.go`):
identity flows through `auth.UserFromContext(r.Context())`, RBAC through
`s.canRead(r, …)` / `s.canReadSubresource(r, …)`, namespace ceilings through
`s.getUserNamespaces(r, …)`. MCP handlers are package-level functions on `context.Context`
with their own RBAC helpers (`internal/mcp/permissions.go`: `resolveUserPerms`,
`filterNamespacesForUser`, `canReadClusterScopedKind`).

Plan: extract collection behind a narrow seam **inside `internal/server`** rather than a new
package:

```go
// internal/server/upgrade_evidence.go
type upgradeEvidenceAuthorizer interface {
    CanList(group, resource, namespace string) bool
    CanGetSubresource(group, resource, subresource string) bool
    Namespaces() []string            // nil = cluster-wide; empty = no access
    Identity() (username string, groups []string)   // for impersonated Helm reads
}

func collectUpgradeEvidence(ctx context.Context, authz upgradeEvidenceAuthorizer) (audit.UpgradeReadinessOptions, error)
```

- The HTTP handler implements the interface over `(s, r)` — behavior byte-for-byte identical,
  proven by the existing handler tests running unmodified.
- The MCP tool implements it over `ctx` using the `internal/mcp` permission helpers. The MCP
  side must reproduce the same authorization *decisions* (list probes / SAR per grant), not
  shortcut them: cluster-wide pod visibility must still not imply cluster-scoped reads.
- All timeouts, response-size bounds, and the worker pool stay in the shared collector.

Both `internal/server` and `internal/mcp` already import `internal/audit`, so the seam adds
no new package edges; the MCP tool calls `collectUpgradeEvidence` + 
`audit.RunUpgradeReadinessFromCache` exactly as the handler does. (`internal/mcp` currently
does not import `internal/server`; if that edge is unwanted, the collector moves to
`internal/audit` — decide during implementation, whichever keeps the import graph acyclic.)

### 4.2 Memoization (prerequisite, shared with HTTP)

A short-TTL memo keyed on `(identity, target, namespace-ceiling-hash)`:

- Proposed TTL: 60s (long enough to absorb an agent's follow-up `check=<id>` expansions of
  one scan; short enough that a fix-then-rescan loop sees fresh state). Tier 2 calls hit the
  same memo as the tier 1 call that preceded them — this is the main point.
- Follows the `rbac.Memoizer` precedent: invalidated by `finalizePostContextSwitch` so a
  kubeconfig context switch never serves the previous cluster's scan.
- Applied at the `ScanResults` level (post-RBAC, per-identity), so it can also back the HTTP
  handler — resolving #1195's deferred "identity/scope-safe server-side result memoization"
  item as a side effect.
- Memoizing means an agent's expansion sequence sees one consistent scan rather than three
  slightly different clusters.

### 4.3 What the MCP scan deliberately keeps

Everything. No evidence source is dropped for the MCP path — a cheaper scan that skips
kubelet metrics would produce more `unknown` rows and a weaker verdict while looking like the
same tool. Cost is handled by the memo, not by thinning evidence.

## 5. Catalog synchronization obligations

The repo enforces these; listing them so the PR is complete in one pass:

| Surface | Change |
|---|---|
| `internal/mcp/tools.go` (`registerTools`) | tool registration + description |
| `internal/mcp/tools_upgrade.go` (new) | handler, input struct, minified output types |
| `web/src/components/home/mcpToolCatalog.ts` | user-facing catalog entry (`TestSetupDialogCoversAllTools` gates CI) |
| `internal/mcp/tools_catalog_test.go` | NOT the write lists (read-only tool); `maxCatalogBytes` budget check |
| `docs/mcp.md` | tool table row + a short "Upgrade impact" usage note |
| `README.md` | mention in the MCP tool list if one exists there |

## 6. Testing

- **Unit (mcp):** tier 1 / tier 2 shaping from a fixture `ScanResults`; finding cap +
  `findingsTruncated`; `level` filter; unknown-preservation (invariant §3.2); `no_access`
  short-circuit (§3.3); unknown `check` id → error listing valid ids.
- **Unit (authorizer):** the MCP authorizer and the HTTP authorizer produce identical
  decisions for a matrix of identities (cluster-admin, namespace-restricted, no-nodes,
  no-secrets, no-access) — this is the regression net for the refactor.
- **Handler regression:** existing `upgrade_readiness_handler_test.go` passes unmodified
  after the extraction.
- **Memo:** TTL expiry, identity separation, context-switch invalidation.
- **Live smoke:** against `kind-radar-gitops-demo` — a real scan through `/mcp` (tier 1, one
  expansion), plus the same via HTTP to confirm identical verdicts.

## 7. Phasing

1. **Extract the evidence collector** behind the authorizer seam; HTTP handler tests prove
   no behavior change. (Reviewable alone; no user-visible change.)
2. **Memoization** at the `ScanResults` level, wired into the HTTP handler too.
3. **The tool**: registration, tiers, catalog sync, docs.

Phases 1–3 can land as one PR with three commits, or split if phase 1 review runs long.

## 8. Open questions

- **Tool name**: leading candidate is **`get_cluster_upgrade_readiness`**. The `cluster`
  qualifier matters: bare "upgrade" collides with Helm upgrades elsewhere in the catalog
  (`get_helm_release`, `get_changes`), and `get_cluster_audit` is exact precedent. A
  `check_*` verb was considered and rejected — the catalog has no `check_` vocabulary, it
  doesn't reliably signal effort, and it suggests a yes/no answer the certainty contract
  explicitly disclaims. If the name itself should signal non-trivial work, the in-catalog
  precedent is `diagnose` (active analysis, not retrieval) and the honest alternative is
  **`analyze_upgrade_readiness`**. Note the tension: the intended flow is overview →
  several `check=<id>` expansions, and with the memo those follow-ups are nearly free — a
  name radiating "heavy operation" discourages exactly the drill-in we want. The cost
  profile therefore lives in the description ("first call runs a live scan… expansions are
  cheap"), which is where agents actually read it. Length (29 chars) is fine; agents select
  on description, not name brevity.
- **Finding cap value** (25 proposed) and whether `level` filtering is worth the extra
  parameter in v1 or should wait for real usage.
- **Should tier 1 include per-check `summary` text** (~18 sentences, ≈1 KB) or only on
  expansion? Proposed: include — it is what lets an agent decide which checks to expand.
- **TTL value** (60s proposed) — long enough for expansion bursts, short enough for
  fix-and-rescan loops; validate against real scan latency on a large cluster.
- **Target discoverability**: an agent doesn't know the cluster's current version when it
  first calls, so its first `target` can be invalid. The validation error should enumerate
  what is valid — current version, the allowed forward range, and `reviewedThrough` — so the
  agent self-corrects in one round-trip instead of probing with `get_dashboard` first.
- **Scan stampede**: the memo only helps after a scan completes. Concurrent calls on an
  empty memo (two agents, or one agent's parallel tool calls) would each run the full live
  collection. The memo needs single-flight per key: concurrent callers wait on the in-flight
  scan rather than starting their own. This also protects the HTTP handler once it shares
  the memo.
