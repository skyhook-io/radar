# @skyhook-io/k8s-ui

Shared, source-distributed Kubernetes UI components used by Radar.

## Local terminal metadata

`LocalTerminalTab` accepts optional `onSessionInfo` and `toolbarExtra` props.
`onSessionInfo` reports the server's `session` frame (`context` and
`kubeconfigIsolated`), and receives `null` when a connection attempt starts.
This describes the supplied kubeconfig, not a shell's live command target.
`toolbarExtra` lets the host render that information in the terminal toolbar.
Hosts can update a dock tab's label and optional full-name tooltip with
`useDock().setTabTitle(id, title, titleTooltip)`.
The optional `DockTab.localTerminalContext` field lets a host retain the full
context selected when a local terminal was requested. An empty string records
no active context; an omitted field records no intent. The shared terminal and
open hook do not interpret it; Radar's host wrapper checks it on open/reconnect.
`canConnect` is a predicate evaluated during rendering to disable Reconnect/Retry,
and before an attempt to preserve the existing terminal when refused.
`onConnectionError` lets a host refresh connection state after a failed WebSocket
handshake.

`initialCommand` is sent once per mounted terminal. Reconnect does not repeat a
command that was already sent; if the connection closes before delivery, the
command remains pending for the next connection.

## YAML editor bundling

`YamlEditor` and `YamlDiffEditor` bundle Monaco, its editor worker, and the YAML language worker into the consuming application. They make no runtime CDN or internet requests, so they work in air-gapped environments.

Consumers must use a bundler that supports module workers created with `new Worker(new URL(..., import.meta.url), { type: 'module' })`. The source package is compatible with Vite and webpack 5. Monaco and `monaco-yaml` are intentionally pinned as a compatible pair because their worker-factory APIs must move together. The package declares that exact Monaco version as a peer so the host and YAML runtime share one Monaco instance.

## Resource kind views and certainty

`ResourcesView.renderKindView(kind)` optionally replaces a kind's table while
keeping the sidebar and resource drawer. Return `null` or `false` to use the table. Hosts
own data fetching; row/search/bulk shortcuts and column preferences are inactive
while a replacement is shown. Sidebar kind navigation remains available.

`CertaintyGlyph` displays `=`, `≥`, `≤` or `?` for an exact, lower-bound,
upper-bound or unknown value, with an accessible label and optional evidence
tooltip. `certaintyGlyph` and `certaintyValueLabel` expose the same vocabulary.

## Optional flow controls

`CreateResourceDialog` defaults to Apply. Guided flows can set `initialMode="create"` and `lockMode` to require creation, and use `onBack(draftYaml)` to retain edits when returning to setup. Cancel closes the dialog. After a successful write, `onCreated(result, submittedYaml)` receives the first result and the exact submitted manifest; dry runs do not invoke it. `result.uid`, when supplied by Radar, identifies the persisted object. Partial creates name the objects already created. Unlocked flows switch to Apply for review when preview is available; direct creation keeps Create selected so a retry cannot silently update existing objects.

`useOpenTerminal` accepts `shell` (one executable name, without arguments), `title`, and `sessionNote`. Dock hosts must forward these fields to their terminal renderer; Radar does so. The shared `TerminalTab` accepts `command` to describe that executable, pin the container, and suppress shell debug advice, plus `note` for session context. `createSession` remains responsible for launching the command. Omitted options retain the normal shell session.

`AreaChart` supports optional `shadedRanges`, controlled `selection`, and `onSelectRange`. Range timestamps use Unix seconds. Dragging selects a range; with positive `stepSeconds`, clicking selects a step-wide window centered on the pointer, clipped at the chart edges. Arrow keys plus Enter select a window from the keyboard. Shaded range labels appear on hover. `AlertBanner.action` places a host-supplied control alongside the title. Omitting these props preserves existing charts and banners.

## Facts, problems and reviewed actions

`FactValue`, `FactSource`, `FactGrid` and `FactRow` show an observed value and its source. Hosts supply explicit unknown or partial text; these components never infer completeness. `at` is a past event by default; `atMeaning="since"` describes a continuing state. `FoldSection` opens when its `attention` becomes true. `RefLink` is navigable only when the host supplies `onNavigate`; `ManagedByText` reuses the GitOps owner mapper for a plain relationship label.

`WorkspaceProblemCallout`, `ProblemList` and `ProblemMeta` show host-classified findings using non-healthy `HealthLevel` tones. `source` selects issue, audit or measurement provenance; source-specific fields cannot be combined. `rootKind` identifies the workspace's root; `subjectIsSelf` suppresses a link back to the current object. The host supplies `OpenIssueContext` only when it can open the matching Issues entry. CNPG maps its critical, warning and posture classifications to shared tones. `formatGrant` formats a permission for display; it performs no authorization.

`ActionConfirmDialog` adds context, reviewed writes and optional typed acknowledgement to the simpler `ConfirmDialog`. Hosts own loading, permission checks, stale-fact validation and result classification. Its `result` binds the state (`error`, `unknown` or `partial`), message and optional heading; unknown and partial results lock another submit. Retain that result outside the dialog across dismissal and remount, and clear it only after explicitly reviewing current state. The component does not infer outcomes from HTTP status. Remount it when changing the reviewed target or action, so acknowledgement and disclosure state start fresh. The default error heading makes no claim about whether a write occurred; use a stronger result title only when the producer proves it.

Existing CNPG primitive exports remain available and delegate to these shared implementations.
