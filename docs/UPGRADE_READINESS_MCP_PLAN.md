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
- **The remediation loop closes inside Radar — when routed by provenance.** Findings carry
  `resource`, `evidence.source`, `evidence.path`, and `remediation`. Roughly half the catalog
  is source-manifest problems (deprecated APIs in Helm/last-applied manifests, non-canonical
  IP/CIDR, `gitRepo` volumes, `externalIPs`), and Radar already ships `apply_resource` /
  `patch_resource`. But the pairing is only correct for findings whose evidence *is* the live
  object (last-applied annotations, live spec fields) — there, an apply updates both the
  object and the evidence the next scan reads. Findings sourced from **stored Helm release
  manifests** are different: patching the live object leaves the release Secret — the actual
  evidence — untouched, so the finding persists across rescans while the object drifts from
  its chart. The tool output and description must route by `evidence.source`: live/
  last-applied → `patch_resource`/`apply_resource`; Helm → fix the chart/values and upgrade
  the release; GitOps-managed (`managedBy`) → fix in Git. *Read finding → fix at the source
  of truth* is still a workflow no other tool in the catalog enables — the routing is what
  keeps it from becoming *read finding → create drift*.
- **Positioning.** The repo describes itself as "the missing open-source Kubernetes UI with a
  built-in MCP server for AI agents". A flagship analysis that agents cannot reach undercuts
  that.

### Does this earn a catalog slot?

Catalog bloat is a real failure mode — every tool's description occupies every agent
session's context, used or not. The bar a new tool should clear: **agents cannot compose the
capability from existing tools, and the question it answers is one agents actually get
asked.** This tool clears both. The evidence behind the 18 check families is outside every
existing tool's reach (Helm stored manifests, `apiserver_requested_deprecated_apis` metrics,
kubelet cgroup/CRI metrics behind `nodes/proxy`, last-applied annotations the informer cache
deliberately strips) — an agent asked "can we upgrade to 1.34" will reconstruct the analysis
from `list_resources` + `get_events` anyway and produce a confidently wrong answer. And it
stays one tool, not an overview/detail pair — the `check` parameter is the drill-down.

The same bar is why the other tool-less surfaces (Capacity, RBAC reverse-lookups, Velero,
CNPG, policy reverse-lookups) do **not** get tools in this plan: their questions route
acceptably through `query_prometheus`, `get_subject_permissions`, and `get_resource`. This
is a capability gap, not a convenience gap.

Fair counter-argument, named for completeness: in the local no-auth setup an agent with
shell access can curl `/api/upgrade-readiness` directly. The tool still earns its place —
raw `ScanResults` would flood an agent's context, curl bypasses RBAC in authenticated
setups, and undiscoverable endpoints don't get used — but a reviewer weighing catalog size
should weigh this too.

### Prior art and real-world validation

**Existing upgrade-check tooling stops at deprecated APIs.** The established OSS tools —
[Pluto](https://www.civo.com/learn/pluto-a-tool-to-manage-deprecated-kubernetes-apis)
(manifests + Helm releases, CI-oriented),
[kubent](https://github.com/doitintl/kube-no-trouble) (live cluster scan), kubepug, and the
EKS-specific [eksup](https://clowdhaus.github.io/eksup/info/checks/) — detect deprecated
API usage and little else ([comparison](https://medium.com/@rameshavutu/kubernetes-upgrade-deprecated-apis-kubent-pluto-30c917c77835),
[survey](https://medium.com/@anupam_gupta86/tools-approaches-to-identify-kubernetes-deprecated-apis-c8b843243faf)).
Radar's catalog subsumes both their modes (static manifest evidence *and* live API-server
metrics) and adds the operational checks none of them attempt: drain/PDB feasibility,
webhook backend readiness, node runtime evidence. The managed-cloud analogue,
[EKS upgrade insights](https://docs.aws.amazon.com/eks/latest/userguide/cluster-insights.html),
validates the verdict framing from the other direction: since 2025 EKS
[enforces its insight checks as an upgrade gate](https://aws.amazon.com/about-aws/whats-new/2025/03/amazon-eks-enforces-upgrade-insights-check-cluster-upgrades)
— a cloud provider decided pre-upgrade findings should *block* an upgrade, which is exactly
the `blocked` verdict's posture. Radar's engine is the vendor-neutral equivalent (EKS
insights are audit-log-based and EKS-only).

**No tool in this space is agent-reachable.** The MCP-enabled Kubernetes tools that exist —
[K8sGPT's MCP server](https://www.perfectscale.io/blog/kubernetes-clusters-ai) (general
troubleshooting) and
[kubernetes-mcp-server](https://dasroot.net/posts/2026/03/kubernetes-mcp-server-guide-ai-integration/)
(general resource access) — have no upgrade-readiness capability, and none of the upgrade
tools expose MCP. This tool would be the first agent-reachable upgrade analysis, which
strengthens the positioning argument above.

**Real upgrade failures validate the operational checks.** Reddit's Pi-Day 2023 outage
(5+ hours) came from a 1.24 upgrade removing the `node-role.kubernetes.io/master` label
from running nodes while Calico route-reflector selectors still targeted it; their
[postmortem's own lesson](https://overmind.tech/blog/reddit-pi-day-outage) — teams were
"evaluating the warnings in the CHANGELOG against a model of the system, not against the
actual system" — is verbatim the thesis of Radar's evidenced-check model
([incident analysis](https://geek-cookbook.funkypenguin.co.nz/blog/2023/03/24/post-mortem-reddit-pi-day-kube-1.25/)).
The label-selector-vs-removed-label class of risk is a candidate for future catalog
entries. Likewise, stuck drains from single-replica workloads behind
`minAvailable: 1` PDBs are common enough that
[AKS documents `PodDrainFailure` as a named upgrade error code](https://learn.microsoft.com/en-us/troubleshoot/azure/azure-kubernetes/create-upgrade-delete/error-code-poddrainfailure)
— the exact state the `node-drain-feasibility` check evidences from authoritative PDB
status. Making these findings agent-consumable is what turns them from a dashboard row
into a fixed manifest.

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
  target   (optional)  target Kubernetes minor, e.g. "1.34"; defaults to the next minor
                       above the cluster's current version (engine behavior today)
  check    (optional)  check id to expand, e.g. "node-drain-feasibility"
  level    (optional)  filter expanded findings to this level and above:
                       blocker | warning | review. No default — expansion returns ALL
                       levels, most severe first. (A default threshold would make tier 2
                       return zero findings for a check whose tier-1 row reports some.)
  offset   (optional)  skip the first N findings when expanding (default 0). Paired with
                       the per-call cap this makes every finding retrievable.
  scanId   (optional)  bind a paging request to the scan that produced the earlier page
                       (every response carries its scanId). If the memoized scan has been
                       replaced (TTL expiry, a refresh) the tool errors "scan changed —
                       restart from offset 0" instead of silently paging a different
                       snapshot. Recommended whenever offset > 0.
  refresh  (optional)  bypass the memo and run a fresh scan (default false). For
                       fix-then-rescan loops; the description steers agents to use it
                       only after changing something. Rejected when combined with a
                       nonzero offset — a fresh scan invalidates the page sequence.
```

Design decisions:

- **A new tool, not a `get_cluster_audit` parameter.** Different severity vocabulary
  (`blocker`/`warning`/`review` vs the Checks posture ladder), different cost class (live
  collection vs informer cache), different scope contract (deliberately cluster-wide vs
  namespace-filtered), and a `target` version that audit has no concept of. Folding them
  together would corrupt a tool whose contract already takes effort to explain.
- **No namespace parameter.** Matches the HTTP surface: an upgrade affects the cluster; the
  caller's RBAC ceiling and `--namespace-scope` still bound evidence (and surface in
  `coverage`), but the tool does not offer a browsing filter that would silently narrow a
  readiness claim. The header namespace picker is likewise ignored, for the same reason
  `upgradeReadinessNamespaces` ignores it.
- **Read-only annotations** (`readOnlyHint`), like every other read tool.
- **`target` is validated** by the existing `upgradereadiness.ValidateTarget`, whose actual
  contract is narrower than a gate: parseable minor + strictly forward. It does **not**
  enforce same-major or a max distance — multi-minor and cross-major jumps are accepted and
  the scan itself reports them as blockers (`control-plane-upgrade-path`), and targets beyond
  `reviewedThrough` are accepted with the partial-review banner. The MCP tool keeps exactly
  this engine contract rather than inventing a stricter one: the certainty machinery
  (`reviewedThrough`, check statuses) already says what a hard gate would say, with evidence.
  Validation errors (invalid target / non-forward / unknown current version) return the
  sentinel-mapped message as a tool error; the error text enumerates the current version and
  `reviewedThrough` so the agent self-corrects in one round-trip.

### Tool description (draft)

The description must carry the certainty contract, because it is the only text the agent is
guaranteed to read:

> Analyze the impact of upgrading the cluster to a target Kubernetes minor version (default:
> the next minor above the current version). Runs the
> evidenced check catalog (reviewed through 1.36): version skew, removed/deprecated API usage
> (live manifests, Helm sources, API-server metrics), node runtime and cgroup evidence, drain
> feasibility, admission/conversion webhook readiness, and release-specific configuration
> checks. The verdict is NOT a readiness guarantee: checks with status `unknown` had
> incomplete evidence and may hide blockers — report them alongside the verdict, never as
> passed. `coverage` describes what could actually be inspected under the caller's RBAC;
> `coverage.state: no_access` means the scan saw nothing namespaced and the verdict is
> meaningless. Default output is one row per check; pass `check=<id>` for that check's
> findings with evidence and remediation. Findings reference exact resources and fields —
> fix them at the source of truth `evidence.source` names: live objects via
> `patch_resource`/`apply_resource`, Helm-sourced findings in the chart/values (patching the
> live object leaves the stored release manifest unfixed), GitOps-managed resources in Git.
> The first call runs a live cluster-wide evidence scan and can take several seconds on
> large clusters; results are briefly cached per caller (`observedAt` reports the scan
> time), so follow-up `check=<id>` expansions page over the same scan cheaply. Pass
> `refresh=true` only after changing something, to re-scan instead of reading the cache.

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
  "observedAt": "2026-08-26T10:14:03Z",  // when the underlying scan ran — cached responses keep the original stamp
  "scanId": "sc_9f2e",              // identifies this scan snapshot; echo as scanId when paging with offset
  "verdict": "blocked",             // blocked | warning | review | no_known_blockers | unknown
  "verdictCaveat": "2 checks had incomplete evidence and may hide blockers",  // omitted only when clean
  "summary": {"blocked": 1, "warnings": 2, "reviews": 3, "passed": 9, "unknown": 2, "notApplicable": 1, "findings": 14},
  "coverage": {
    "state": "partial",             // full | partial | no_access
    "scopedNamespaces": ["team-a"], // present when evidence was namespace-bounded
    "scopedKinds": {"pods": ["team-a", "team-b"]},  // per-kind ceilings — mixed RBAC scopes can't collapse into scopedNamespaces
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
      "caveat": "…",                // present when evidence for this check was incomplete
      "evidenceNote": "…"           // present when evidence has known limits even on a pass
                                    // (e.g. deprecated-API metrics cover one API-server process, not cluster-wide history)
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
    "summary": "…", "caveat": "…", "evidenceNote": "…", "scope": "…", "inspected": 412,
    "findingsTotal": 40,            // all findings on this check, before level filter and cap
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
    "findingsTruncated": 12         // matching findings withheld beyond this page, 0 omitted
  }
}
```

Findings are ordered most severe first and capped (proposed: 25 per call), with the counts
kept distinct so a cap can never masquerade as completeness: `findingsTotal` (all findings on
the check), the returned page, and `findingsTruncated` (matching findings beyond this page).
Every finding is retrievable: `offset` pages through the remainder, and page consistency is
*bound*, not assumed — pages read the memoized scan snapshot, and passing the response's
`scanId` on continuation requests turns a replaced snapshot (TTL expiry, refresh) into an
explicit "scan changed — restart" error rather than a silently different page with
duplicated or dropped findings. `level` narrows to the levels the agent cares
about but is never a silent default — no filter, no cap may hide a finding without an
explicit count saying so. References (doc URLs) are dropped from MCP output except the first
per finding; they are the least token-efficient field and the remediation text stands alone.

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
4. **Caps and ceilings are visible.** `findingsTotal`, `findingsTruncated`,
   `scopedNamespaces`, `scopedKinds`, and `unavailableKinds` are never silently dropped by
   minification. `scopedKinds` exists precisely because mixed per-kind RBAC ceilings cannot
   be represented by one namespace list — collapsing it into `scopedNamespaces` would
   overstate coverage.
5. **`reviewedThrough` is always present** so an agent targeting a minor beyond the catalog
   ceiling can say so.
6. **Evidence limits survive on passed checks.** `evidenceNote` records what a check could
   not see even when it passed (deprecated-API metrics cover one API-server process, not
   cluster-wide history); the minifier carries it in both tiers.
7. **Staleness is visible.** `observedAt` always reports when the underlying scan ran;
   cached responses keep the original stamp rather than the response time, so an agent in a
   fix-then-rescan loop can tell pre-fix evidence from a failed fix.

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

- The HTTP handler implements the interface over `(s, r)` — behavior byte-for-byte
  identical. The existing `upgrade_readiness_handler_test.go` covers the helpers
  (`upgradeReadinessNamespaces`, bounded reads, discovery fallbacks) but never invokes the
  full handler, so it alone cannot prove this — the extraction is preceded by
  characterization tests around the real handler (§6).
- The MCP tool implements it over `ctx` using the `internal/mcp` permission helpers. The MCP
  side must reproduce the same authorization *decisions* (list probes / SAR per grant), not
  shortcut them: cluster-wide pod visibility must still not imply cluster-scoped reads.
- All timeouts, response-size bounds, and the worker pool stay in the shared collector.

Both `internal/server` and `internal/mcp` already import `internal/audit`, and
`internal/mcp` already imports `internal/server` (`tools_packages.go`) while the reverse
edge does not exist — so placing the seam in `internal/server` adds no new package edges
and cannot create a cycle. The MCP tool calls `collectUpgradeEvidence` +
`audit.RunUpgradeReadinessFromCache` exactly as the handler does.

### 4.2 Memoization (prerequisite, shared with HTTP)

A short-TTL memo keyed on `(cluster-context generation, identity, target,
namespace-ceiling-hash)`:

- Proposed TTL: 60s — long enough to absorb an agent's follow-up `check=<id>` expansions of
  one scan. Tier 2 calls hit the same memo as the tier 1 call that preceded them — this is
  the main point. Fix-then-rescan loops do **not** wait out the TTL: `refresh=true` bypasses
  the memo (and replaces the entry), and `observedAt` in every response lets the agent see
  it is reading a pre-fix scan.
- **Context isolation is in the key AND at the return boundary.** The key includes a
  monotonic context generation (bumped on every kubeconfig context switch);
  `finalizePostContextSwitch` invalidation alone is insufficient — a racing read can hit the
  old entry before the clear, and a pre-switch in-flight scan can complete after it and
  repopulate the map. Insertion refusal is necessary but not sufficient: a scan whose
  collectors straddle a switch can contain mixed-cluster evidence, and returning it (cached
  or fresh) is worse than failing. So the generation is captured together with the
  client/cache snapshot at scan start and **revalidated immediately before returning any
  result**; on mismatch the caller gets a retryable "cluster context changed — re-run the
  scan" error with no payload. Tests assert no stale data is *returned* (switch-during-hit,
  switch-during-scan), not merely that insertion is refused.
- **Single-flight per key covers refresh too**: concurrent callers on an empty entry (two
  agents, or one agent's parallel tool calls) wait on the in-flight scan rather than each
  running the full live collection — and a `refresh` call issued while a scan is already in
  flight joins that scan instead of starting another. Consecutive refreshes are additionally
  bounded by a short per-key cooldown (proposed 5s: within it, refresh returns the newest
  completed scan) so a looping agent cannot turn `refresh` into a scan stampede.
  Description text asking agents to behave is guidance, not the control — the coalescing and
  cooldown are. (Today's HTTP endpoint is fully uncached and unlimited, so this is strictly
  tighter than the status quo.) Failed scans are not cached; a canceled leader's waiters get
  the error, not a poisoned entry.
- Applied at the `ScanResults` level (post-RBAC, per-identity), so it can also back the HTTP
  handler — resolving #1195's deferred "identity/scope-safe server-side result memoization"
  item as a side effect.
- **Memoizing HTTP obligates an HTTP refresh contract.** The Upgrade impact view's Refresh
  button today is a plain React Query `refetch()` of the same GET — if the endpoint silently
  gains a 60s memo, a user who fixes a finding and clicks Refresh gets the pre-fix scan
  while `dataUpdatedAt` advances, i.e. stale evidence presented as fresh. So the endpoint
  gains `?refresh=true` with the same coalescing/cooldown semantics as the MCP parameter,
  the UI's manual Refresh passes it (ordinary/background fetches stay memoized), and the
  view surfaces the response's `observedAt` as the scan time instead of implying the fetch
  time. A post-fix manual-refresh regression test covers this path.
- Memoizing means an agent's expansion sequence sees one consistent scan rather than three
  slightly different clusters — and makes `offset` paging stable.

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

- **Characterization first (pre-refactor):** tests around the real HTTP handler pinning
  which evidence is collected under each RBAC boundary (cluster-admin, namespace-restricted,
  no-nodes/proxy, no-secrets, no-access) — the existing `upgrade_readiness_handler_test.go`
  covers helpers, not the handler, so these are written *before* phase 1 and must pass
  unmodified after it. This is the regression net for the extraction.
- **Unit (mcp):** tier 1 / tier 2 shaping from a fixture `ScanResults`; `findingsTotal` /
  cap / `findingsTruncated` / `offset` paging; `scanId` mismatch → "scan changed" error;
  `refresh` + nonzero `offset` rejected; `level` filter (and that no filter is applied
  by default); unknown-preservation (invariant §3.2); `no_access` short-circuit (§3.3);
  `evidenceNote` on a passed check and per-kind `scopedKinds` survival (§3.4/§3.6); unknown
  `check` id → error listing valid ids.
- **Unit (authorizer):** the MCP authorizer and the HTTP authorizer produce identical
  decisions for the same identity matrix — evidence-set equality, not just boolean parity.
- **Memo:** TTL expiry; identity separation (restricted identity never reads an admin's
  entry); `refresh` replacing the entry, concurrent refreshes coalescing into one scan, the
  refresh cooldown; single-flight — concurrent callers coalesce, a failed scan is not
  cached, a canceled leader doesn't poison waiters; context-switch races — switch-during-hit
  and switch-during-scan both return the retryable stale-context error with **no payload**,
  not merely refuse insertion.
- **HTTP refresh:** post-fix manual refresh returns fresh evidence (`?refresh=true`
  bypasses the memo); ordinary refetch within the TTL serves the memoized scan with its
  original `observedAt`.
- **Live smoke:** against `kind-radar-gitops-demo` — a real scan through `/mcp` (tier 1, one
  expansion), plus the same via HTTP to confirm identical verdicts.

## 7. Phasing

1. **Characterization tests** around the current HTTP handler (evidence-per-RBAC-boundary),
   then **extract the evidence collector** behind the authorizer seam; the new tests prove
   no behavior change. (Reviewable alone; no user-visible change.)
2. **Memoization** at the `ScanResults` level (context generation, single-flight), wired
   into the HTTP handler together with its `?refresh=true` contract, the UI Refresh button
   passing it, and the view surfacing `observedAt` — the memo and the UI truth-telling land
   as one change, never the memo alone.
3. **The tool**: registration, tiers, catalog sync, docs.

Phases 1–3 can land as one PR with three commits, or split if phase 1 review runs long.

## 8. Adjacent work, out of scope here

**Kubernetes 1.37 catalog extension** (separate PR): 1.37 released 2026-08-26
([sneak peek](https://kubernetes.io/blog/2026/07/31/kubernetes-v1-37-sneak-peek/)), so
`target=1.37` scans currently hit the "coverage ends at 1.36" banner. The headline risk is
blocker-class: static Pods may no longer reference Secrets/ConfigMaps and the
`PreventStaticPodAPIReferences` opt-out gate is
[removed](https://github.com/kubernetes/kubernetes/pull/140226) — a violating manifest under
`/etc/kubernetes/manifests` produces no pod, control plane included
([analysis](https://bex.co/blog/2026/08/24/kubernetes-137-sneak-peek-ipvs-static-pods-cgroup-v1)).
That check is fully evidenced from Radar's informer cache (mirror-pod annotations + pod
spec references). Also: kube-proxy `ipvs` deprecation warning (review-level), continued
cgroup v1 phase-out (already covered by `node-cgroup-v1`), and a `ReviewedThrough` bump.
Finalize against the official `CHANGELOG-1.37.md` urgent-upgrade notes, not the sneak peek.
The MCP tool inherits whatever the catalog knows — no coupling between the two PRs.

## 9. Open questions

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
- **Finding cap value** (25 proposed). `level` and `offset` are settled: `level` is a pure
  filter with no default (a default threshold made tier 2 contradict tier 1's counts), and
  `offset` exists because a cap with no retrieval path would make findings beyond the first
  page unreachable — fatal for the runbook/backlog workflow the tool exists for.
- **Should tier 1 include per-check `summary` text** (~18 sentences, ≈1 KB) or only on
  expansion? Proposed: include — it is what lets an agent decide which checks to expand.
- **TTL value** (60s proposed) — long enough for expansion bursts; `refresh=true` covers
  fix-and-rescan, so the TTL no longer has to be short for that. Validate against real scan
  latency on a large cluster.
- **Post-mutation invalidation**: should Radar's own write tools (`apply_resource`,
  `patch_resource`, Helm operations) invalidate the scan memo, making `refresh` mostly
  unnecessary? Deferred unless cheap — the explicit `refresh` + `observedAt` contract
  already keeps staleness visible; write-path coupling to the memo is easy to get wrong.
