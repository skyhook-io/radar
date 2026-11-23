# kagent workspace

Radar reads kagent 1.x (`api.kagent.dev`) configuration and, separately,
Substrate's runtime. The workspace connects configuration findings, their Agent consumers and the
revision new sessions run, with Substrate runtime evidence shown separately.

## Screens and navigation

The Resources sidebar's **Agents** row contains a **kagent** section:
**Overview**, **Agents**, **MCP servers**, **Models**, and **Sessions →**. The
badge counts atomic configuration findings before folding (≥ over partial
reads); runtime crashes and pool capacity remain in Substrate's badge.

| Screen | Route | What it shows |
|---|---|---|
| Overview | `/kagent` | Prepared/blocked Agents, failing MCP servers, models with credential problems, separate Substrate session context, folded Needs attention groups, then the Agents table. Each finding names its object, Radar's reading, observed affected Agents and their effects, a next step, and kagent's message folded last. Unused broken dependencies follow findings with observed consumers in a folded, lower-priority section. |
| Agents | `/kagent/agents` | Description from the latest change’s root template (inline or read AgentTemplate), latest-change state, the 12-hex new-sessions revision, model and its reading, MCP declarations/failures, sessions/crashes or the unavailable tier, and namespace. |
| Agent page | `/kagent/agents/<namespace>/<name>?ctx=` | Its own and folded dependency findings; What new sessions run; Configuration of the latest change, explicitly qualified when blocked or unprocessed; declared model/credential, tools, subagents, Harness/worker-pool, Harness-model and egress chains. Placement describes the prepared template, never a blocked edit's new declarations. Sessions and Revisions tabs supplement Spec & status and the generic history/events. |
| MCP servers | `/kagent/mcpservers` | Controller discovery state, known offered tools, consumers, requested tools not offered, and namespace. Unknown discovery never reads as zero tools. |
| MCP server page | `/kagent/remotemcpservers/<namespace>/<name>?ctx=` | Reading, next steps, folded controller message, redacted URL, offered tools and header source coordinates, then Used by with each Agent's tools and latest-change state. |
| Models | `/kagent/models` | Resolution or credential failure, provider/model, Secret/key coordinates, consumers and roles, namespace, then ModelProviderConfigs (status not reported in kagent 1.0.1). |
| Model page | `/kagent/modelconfigs/<namespace>/<name>?ctx=` | Reading, next step, provider/model, credential coordinates and Used by with roles and effects. |
| Sessions | `/substrate/actors?kagent=sessions` | Substrate's paged Actors list, including attributed sessions and unattributed kagent-shaped actors, excluding standalone sandboxes. The namespace view selection narrows their atespaces. |

The namespace picker narrows inventory and findings, while reference and consumer
evidence may come from outside that selection. Every object page pins `ctx`:
a changed context cannot silently open a namesake. The three main kinds open
their own pages; other declared objects open in the ordinary drawer. The drawer
also leads with the workspace summary; **Spec & status** retains the existing
renderers and individual get-authorized dependency reads. Get-only access remains
useful when the workspace's list reads cannot compose a chain.

Below 1280px tables move secondary facts beneath the name or the fact they
qualify rather than clipping columns. Loading, not-installed, partial, denied
and failed reads remain distinct. Denied dependencies name the missing list or
get grant. The two capabilities are negotiated independently: older Radars keep
the generic Agents destination, tables and drawer, and never receive unsupported
workspace, summary or Sessions-filter requests.

The Agent Sessions tab pages attributed actors and retains unattributed hints
without adding them to counts. A session opens its Substrate actor page with an
**Agent: <name>** return link. Recover through kagent, never by moving or reverting
its actor directly. Revisions distinguish desired, new-sessions, earlier and
ambiguous/unprovable attribution; template creation and golden-snapshot state are
labelled as Substrate facts, with no invented preparation age.

Spec & status and the projected chains redact credential literals. The YAML
view shows the object as stored to whoever may read it, under the exact-kind
read policy for ModelConfigs and RemoteMCPServers.
**Conversation outcomes: not visible to Radar.** No Prometheus outcome panel is
included here.

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
A connection switch discards the whole answer with 503. Composition refuses an
inventory above 10,000 distinct objects with 503 instead of silently truncating.

| Value | Source | Unknown / partial / denied |
|---|---|---|
| Agent reading and latest change | Agent conditions and observed generation | Old status is not current success. A latest change without a reported condition stays not reported. |
| New sessions revision | Agent `status.latestSuccessfulRevision` | Omitted when no revision was prepared; no preparation age is invented. |
| Model / MCP summary | Current template, recursively resolved subagents, Harness | An unread intermediate marks `incomplete`; traversal stops at 32 templates or depth 4 and detects cycles. These declarations do not reconstruct a previous prepared revision. |
| Reference | Declared coordinate and caller's per-kind list | `present` with a reading where the kind reports status; `absent` only when fully read in that namespace; otherwise `unread` with the coverage state or `outside_namespace_pin` reason. A readable sharing policy can produce `notPermitted`. Namespace-label selectors stay `policyUnknown` if namespace metadata cannot be read. |
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
templates and no prepared revision read as unknown. A partial worker read can
return `mayFit` with `complete:false` when an observed worker has room; without
that positive evidence it returns `unknown`.

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
