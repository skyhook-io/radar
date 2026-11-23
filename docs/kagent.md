# kagent workspace

Radar reads kagent 1.x (`api.kagent.dev`) configuration and, separately,
Substrate's runtime. The backend supports the planned Overview, Agents, MCP
servers, Models and Sessions screens. This change does not add those screens;
the existing resource lists and drawers remain available.

## Three evidence layers

| Layer | Source | Meaning and limits |
|---|---|---|
| Latest change | Agent conditions, current `spec`, `status.desiredRevision` | Prepared, preparing, blocked, failed, not ready, not processed, not reported or deleting. Compilation can clear desiredRevision; an empty digest does not imply there was never a good revision. References describe this change, including when it is blocked. |
| What new sessions run | `status.latestSuccessfulRevision` | The revision new sessions start on. A blocked edit can leave a previous good revision serving. Its old configuration is not reconstructed from today's Harness or AgentTemplate. There is no promotion timestamp; template creation time is not preparation time. |
| Runtime | Substrate actors, templates and workers | Session actors by state, their pinned revisions, crash messages and worker bookings. A suspended actor can follow a failed model call. Conversation outcomes are not visible to Radar. |

`pkg/kagent` owns configuration readings, declared reference resolution, impact,
folding and counts. `pkg/substrate.KagentIndex` owns runtime attribution, including
earlier revisions and unattributed hints; see [substrate.md](substrate.md#which-agent-a-session-belongs-to).

## Configuration API

`GET /api/kagent/workspace?namespaces=team-a,team-b` is advertised by
`kagentWorkspace`. The single-namespace `namespace` spelling also works.

The response has `context`, `installed`, `namespaces`, optional
`forcedNamespace`, `coverage`, `referenceCoverage`, `agents`, `models`,
`mcpServers`, `modelProviders`, atomic `findings`, folded `groups` and `counts`.
Coverage keys are `agents`, `agentTemplates`, `harnesses`, `models`, `mcpServers`,
`egressPolicies`, `modelProviders` and `sandboxTemplates`.

Every kind is listed as the caller through `readWorkspaceKind`; coverage uses
`full`, `partial`, `denied`, `notInstalled`, `syncing`, `uncached` and `error`.
`coverage` describes the selected inventory. `referenceCoverage` describes the
broader read used for references and consumers: the namespace picker narrows
lists, findings and their counts, while references and observed consumers may
lie outside it, within the caller's RBAC and Radar's `--namespace-scope` pin.
A connection switch discards the whole answer with 503.

| Value | Source | Unknown / partial / denied |
|---|---|---|
| Agent reading and latest change | Agent conditions and observed generation | Old status is not current success. A latest change without a reported condition stays not reported. |
| New sessions revision | Agent `status.latestSuccessfulRevision` | Omitted when no revision was prepared; no preparation age is invented. |
| Model / MCP summary | Current template, recursively resolved subagents, Harness | An unread intermediate marks `incomplete`; traversal stops at 32 templates or depth 4 and detects cycles. These declarations do not reconstruct a previous prepared revision. |
| Reference | Declared coordinate and caller's per-kind list | `present` with a reading where the kind reports status; `absent` only when fully read in that namespace; otherwise `unread` with the coverage state. A readable sharing policy can produce `notPermitted`. Namespace-label selectors stay `policyUnknown` if namespace metadata cannot be read. |
| Credential and prompt references | Spec coordinates | Names and keys only. Secrets and ConfigMaps are never read. Their missing status comes from kagent's own conditions. |
| Tools offered | RemoteMCPServer's successful controller discovery | `toolsOfferedKnown:false` when discovery failed, was disabled, or is unreported; an empty list then is not zero tools. Asked-for tools are compared only with known offered tools. |
| Used by | Distinct Agents' current declared reference paths | Capped at 500 displayed Agents with an uncapped total. Partial inventory or traversal yields a lower bound. `none` means no Agent uses it within the read scope; `noneSeen` means no Agent consumers seen in a partial read. |
| Model provider | ModelConfig provider/model fields; ModelProviderConfig type | No ModelConfig-to-ModelProviderConfig relationship is inferred. kagent 1.0.1 reports no ModelProviderConfig status. |

Template models and MCP servers default to their declaring template's namespace;
Harness summarizer and embedding models default to the Harness namespace. A
summarizer without its own model falls back to the Agent model. WorkerPool,
Secret and ConfigMap references are coordinates (`declared`), not health claims.
RemoteMCPServer headers expose their name and source coordinate or `inline:true`,
never a literal value. URLs and quoted condition messages use `pkg/kagent`'s
credential redaction; no default headers, environment or secretHash is projected.

## Findings, effects and counts

An atomic finding has a stable object coordinate and reason, severity, message,
action, optional cause and optional `affects`. The next step stays in `action`.
An affected Agent is counted once across paths and roles:

- `blocked`: that Agent's own finding matches a bounded recognizer of kagent's
  literal compiler/runtime message forms naming this dependency. It is the
  **first reported cause**; fixing it may reveal another.
- `mayRunWithoutTools`: a failing RemoteMCPServer is bound by an Agent whose
  latest change is prepared. kagent's controller cannot discover its tools;
  sessions may run without them. Runtime discovery is separate and unobserved.
- `usesIt`: the Agent declares the dependency; no stronger effect is observed.

Folding is presentation over the findings this caller receives, after the
namespace filter. A matched Agent finding folds under its dependency finding;
when that row is denied or outside the view it stays separate. Independent Agent
findings (such as egress grants) stay separate. ModelConfigs reporting the same
missing API-key Secret group by that Secret coordinate. A group keeps its worst
severity, including a critical Agent that cannot start any session.

`counts.findings` and `counts.critical` count atomic findings before folding.
`counts.groups` counts displayed groups, with `incomplete` rather than a
mathematical lower bound: more evidence can merge groups. `agentsAffected` counts
distinct Agents, without the 500-link presentation cap. Counts carry
`lowerBound:true` when their supporting inventory or traversal is incomplete.
Runtime crashes and pool capacity are not added to this configuration badge;
Substrate already counts them.

Issues and MCP Issues keep each object's independent Issue, never workspace
folding. A multi-object enrichment adds positively observed Agents through
uncapped `AffectedRefs` and capped diagnostic links. It checks exact inventory
reads before composing paths and retains them in `RequiredReads`. If impact
cannot be read, the original dependency Issue, action and onset remain. A shared
memo projected to a caller missing any retained impact read conservatively drops
that Issue's entire optional impact; it does not hide the underlying Issue or
match it through withheld Agent membership. Fresh per-caller composition can
retain the readable subset. The existing related-reference redactor also removes unreadable Agent links and
their counts.

Shared vectors in `pkg/kagent/testdata/impact.json` cover blocked edits with a
previous revision (including an empty desiredRevision), shared missing Secrets,
independent findings, sharing refusal, Harness namespace defaulting, missing
subagents, MCP discovery failures, unavailable requested tools, unread models and
cycles. They are also available for the future UI.

## Runtime summary and Sessions filter

`GET /api/substrate/kagent/summary?namespaces=team-a` is advertised by
`kagentSessionSummary`, alongside the new `GET /api/substrate/actors?kagent=sessions`
filter. Older `agentSession`, `sandbox` and `unattributed` values keep their
semantics under `kagentAttribution`.

The summary uses exactly the per-Agent sessions endpoint's Substrate API-tier
gate plus the caller's Agents list. It returns `apiTier`, `coverage` (actors,
templates, workers, atespaces, kagentAgents), `rows`, `total`, `lowerBound`, `limit`
and `offset`. Rows are sorted by namespace/name. The default and maximum page
size is 1,000; the snapshot is bounded at 50,000 actors and 10,000 of each other
Substrate kind. No per-Agent fan-out or log reads run at list time.

Each row carries the existing `KagentAgentSessions` shape (`agent`, `revisions`,
`sessions` by state, `onEarlierRevisions`, `complete`, `needAttention`, first five
`attention` rows, `hintedEarlierRevisions`, `hintShared`) plus `crashed`,
`onNewSessionsRevision`, `onOtherRevisions` and `placement`. Crash explanations
come only from recorded actor crash messages. Revision counts exclude hints.
`onOtherRevisions` includes attributed earlier revisions and a distinct desired
revision; `onEarlierRevisions` keeps the attribution contract's narrower meaning.

A row's `complete:false` means its counts are lower bounds. Entirely unread
actors return 503 rather than a fabricated zero. Coverage and `apiTier` timestamps
explain the snapshot's age and refresh failures separately from the fresh
Kubernetes Agent read. Missing templates leave earlier sessions as hints; they
are not counted as proven attribution.

`placement` uses the **latest successful revision's actual Substrate template**
and active workers' unbooked resources through `HasRoomFor`. It returns `unknown`,
`mayFit`, `noActiveWorkers`, `full` or `noRoom`, the template coordinate, matching
observed pools and completeness. `mayFit` never promises capacity for N sessions:
runtime memory overhead, snapshot and runtime compatibility can still prevent
placement. A negative verdict requires a complete worker read. Missing prepared
templates, no prepared revision and partial workers read as unknown.

The Sessions filter includes attributed Agent sessions and unattributed
kagent-shaped actors, excluding standalone sandboxes. `namespace` / `namespaces`
map to the actor's **atespace**, so they narrow unattributed actors too, before
counts and paging. An explicit `atespace` intersects that selection. The process
namespace pin also applies. `agent=ns/name` still requires proven attribution.

## Run modes

| Mode | Configuration workspace | Runtime summary / Sessions |
|---|---|---|
| Local, auth off | Lists with the kubeconfig identity; redacted projection | Private listener only, using the saved Substrate connection |
| In-cluster with auth | Every kind listed as the signed-in caller; exact-kind ModelConfig and RemoteMCPServer access | Caller mints `ate-system/ate-client` with their own impersonated access; no Radar-ServiceAccount mint |
| Radar Hub | Same caller gates through the cluster proxy; frontend negotiates `kagentWorkspace` and `kagentSessionSummary` independently | Same signed-in API-tier gate; unsupported endpoints/filters require a newer Radar |
| Shared without auth | Anyone reaching Radar sees the redacted configuration its Kubernetes identity can list; no host access, Secret/ConfigMap reads or Substrate dial | 403 `sharedRadar`, exactly as the existing API-tier routes |

The API middleware refuses browser cross-site reads in every mode. No write,
controller mutation, connection change or cluster fixture is required by either
endpoint. The existing chart's credential-sensitive collection split is retained.
