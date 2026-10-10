# Helm Support

Radar treats Helm as a release system, not just a set of Kubernetes objects. The Helm view combines release metadata, rendered resources, revision history, operation insight, revision comparison, and live Kubernetes evidence around failed hooks.

## Release List

The Helm list shows:

- Release status, chart version, app version, revision, and update time.
- Resource health derived from the current rendered manifest and live Kubernetes status.
- Helm storage namespace. This matters for controllers such as Flux, which may store the Helm release Secret outside the target namespace.
- Flux ownership when Radar can match the release to a Flux `HelmRelease`.
- `lastOperation` for current failed upgrades, rollback-after-failure patterns, explicit rollbacks, and stuck pending operations.
- A capped operation trail for failed upgrades, rollbacks, rollback-after-failure, and stuck pending operations.

When `storageNamespace` is present, use it for Helm detail/API/MCP calls. Helm stores release history there even when the chart deploys resources into another namespace.

## Release Detail

The drawer includes:

- Overview: chart metadata, status, resource health, notes, dependencies, Flux ownership, and operation insight when available.
- History: revision status, description, update time, and operation classification.
- Manifest and values: rendered output and values for the selected release, with values redacted in MCP responses.
- Resources: live status for resources rendered by the current release.
- Hooks: hook events, path, weight, status, run times, delete policies, output-log policies, and diagnostics for failed/running hooks.

Compare opens a full-page workspace instead of rendering inside the drawer. The drawer links to Compare from history rows and operation banners when Radar can identify a useful revision pair.

## Operation Insight

Radar derives a Helm operation signal from release history and the current release status. It distinguishes:

- Active failed upgrades.
- Active pending or stuck installs/upgrades/rollbacks.
- Explicit rollbacks.
- Recovered rollback-after-failure flows.

For active failed or stuck releases, Radar ranks the rendered live resources and surfaces the best resource to inspect when one is unhealthy or not ready. Workloads rank above leaf resources, but a Service or Ingress with a concrete issue can still be selected when it is the strongest signal.

For recovered operations, Radar suggests the revision comparison most likely to explain the recovery:

- Previous completed revision to failed upgrade revision.
- Restored revision to failed upgrade revision for rollback-after-failure.
- Previous completed revision to rollback revision for explicit rollbacks.

Flux-owned Helm releases defer to Flux. Radar shows the owning `HelmRelease` and does not synthesize native Helm operation insight for releases managed by Flux's helm-controller, because the GitOps controller is the authoritative reconciler.

Active native Helm failures and stuck pending operations also appear in the global Issues stream as `kind=HelmRelease`, `group=helm.sh`. Recovered rollbacks are deployment history, not live issues; use Helm detail or `get_changes` for those.

## Failed Upgrades And Rollback Inference

Helm history does not persist whether `helm upgrade --atomic` was set. Radar infers an atomic-style rollback from the revision sequence:

- A failed upgrade revision.
- Followed by a deployed rollback revision.
- With revision descriptions/statuses that point back to the previously deployed release.

Radar surfaces that as a Helm operation instead of making the operator infer it from raw revision rows. The UI and MCP response include the failed revision, rollback revision, target revision, and failure description when Helm recorded one. Radar calls this an inferred rollback-after-failure pattern; it does not claim the exact Helm CLI flag that caused it.

## Revision Compare

The Helm Compare page is optimized for incident debugging. It starts with the rendered Kubernetes manifest diff as the source of truth, then keeps supporting evidence below it:

- Rendered resources: a compact index of Kubernetes object identities and meaningful in-place field changes.
- Values: user-supplied values diff; the UI shows the user's own values, while MCP applies key-aware redaction.
- Hooks: stable hook definition diff, ignoring runtime timestamps and volatile hook status.
- Notes: release notes diff.

The rendered resource section is intentionally not a second manifest diff. It summarizes resource identities and selected field changes so operators can scan what moved:

- Added and removed rendered objects.
- Modified objects for known Kubernetes kinds.
- Field summaries such as image, probe, selector, or workload template changes.
- A low-confidence warning when old and new rendered object identities do not overlap, such as a chart rename.

Use the manifest diff for exact YAML and for cases where a resource was renamed or the compact resource index cannot pair old and new objects.

## Hook Diagnostics

For failed or running hooks, Radar reports:

- Hook identity, namespace, kind, path, lifecycle events, and last-run phase.
- Delete/output-log policies that may explain missing evidence.
- Live Job/Pod/Event evidence when it still exists.
- Short, redacted log snippets from correlated hook pods when the current identity can read logs.

Evidence is best-effort. Helm hook delete policies, `ttlSecondsAfterFinished`, garbage collection, or RBAC can remove or hide the Job/Pod/Event/log data after Helm records the hook phase. Radar says when evidence is unavailable instead of pretending there is no hook failure.

Evidence reads use the requester's Kubernetes identity in auth-enabled deployments. Log snippets are capped and scrubbed for secret patterns before they are returned.

## MCP

Use `list_helm_releases` first for broad Helm deployment triage. It returns release status, health, storage namespace, Flux ownership, and operation signals.

Use `get_helm_release` for detail:

- The default response includes owned resources, resource health, Flux ownership, current operation signal, hooks, and hook diagnostics when present.
- `include=history,operations` returns the full revision and operation trail.
- `include=values` returns user-supplied values with key-aware secret redaction.
- `include=diff` returns manifest diff.
- `include=values_diff` returns a user-supplied values diff with key-aware secret redaction applied to both revisions before comparison.
- `include=notes_diff` returns release notes diff.
- `include=resource_diff` returns added, removed, unchanged, and modified rendered resources between revisions.

Use `issues` for currently broken native Helm releases: active failed and stuck pending releases are surfaced as live operational issues. Use `get_changes` when an agent asks what changed before an incident; it includes the Kubernetes-resource timeline plus recent native Helm release deployment and operation history (`source: helm`). For full Helm revision history, operation details, hook diagnostics, values, and diffs, use the Helm MCP tools above.

## Uninstall And Rollback Actions

`manage_helm_release` exposes uninstall and rollback through MCP with a mandatory
preview. Omit `dry_run` (or set it to `true`) to inspect current status, release
manifest resources, action hooks, and history policy. Pass `dry_run=false` and the
returned `confirm` token only after showing the plan to the user and obtaining
explicit approval. Uninstall with history purge is irreversible. Tokens expire after five
minutes and bind the caller, cluster context, release snapshot, and action options.
The current and target Helm records are rechecked before execution. A confirmation
is consumed when an action starts. REST and MCP uninstall/rollback share an
in-process guard for the context, storage namespace, and release name. Helm has no
atomic release preconditions across processes, so avoid concurrent external operations.

The preview reads stored Helm declarations as the caller and checks the same
release-storage write capability as REST (`secrets/create` in the storage namespace).
Rollback effects use live resource reads; failed/running hooks include best-effort
live diagnostics. It does not simulate hooks, validate all eventual writes, inspect
live finalizers, or enumerate resources
created by controllers or chart CRDs outside the release manifest. It returns
identities rather than manifest bodies or values. A malformed manifest or a stored
list document that cannot be fully enumerated refuses confirmation. All execution
uses the caller's Kubernetes identity; actual writes can still fail or partially
complete.

- `uninstall`: `no_hooks=true` skips pre/post-delete hooks; this can leave external
  resources behind. `keep_history=true` preserves Helm history, not Kubernetes
  resources. The default purges release history. Resources annotated
  `helm.sh/resource-policy=keep` (trimmed, case-insensitive) are identified as
  retained. Helm v3.22 also omits manifests bearing any other value of that
  annotation from uninstall; the plan labels them as not deleted.
- An `uninstalling` release can be retried with the same uninstall action. Inspect
  pre-delete hook Jobs and remaining resources/finalizers before choosing to skip
  hooks. Hook status is the last recorded phase, which can remain Running after
  hook creation or waiting failed. A surviving hook Job without a
  before-hook-creation policy can make retry fail with AlreadyExists; inspect it
  before explicitly approving deletion or skipped cleanup. The preview reports
  recorded Deleted and LastDeployed timestamps. This action never strips finalizers
  or edits Helm storage to force success.
- `rollback`: requires an explicit positive revision older than the current one
  (`get_helm_release include=history` lists revisions). The preview uses that
  revision's pre/post-rollback hooks and one sorted resource plan: create, update,
  delete, or keep based on live evidence. Removed resources with the live policy
  exactly `keep` are retained. API version changes do not change object identity.
  Rollback creates a new revision; pending releases carry a concurrent-operation
  warning because Helm v3.22 has no pending rollback lock. `no_hooks` is supported, while
  `keep_history` is uninstall-only.
- For an already `uninstalled` release, uninstall with default options only purges
  history. The preview says resources/hooks are not processed again.

REST `DELETE /api/helm/releases/{namespace}/{name}` also accepts `no_hooks`,
`keep_history`, and `dry_run`, each supplied once as `true` or `false`. Its existing
default remains a real uninstall with hooks and history purge. `dry_run=true`
returns the same stored-manifest preview without a mutation; REST uses its existing
UI confirmation flow and does not require an MCP confirmation token. Missing
releases return 404; preview refusals return 409. Release objects and hooks use the
recorded target namespace while release history stays in the storage namespace.
When known, the preview names the owning Flux HelmRelease and warns it may
reconcile direct actions back. Both surfaces audit previews and actions with
revision, no_hooks, and keep_history.

## Known Limits

- Atomic rollback detection is inferred from Helm history because Helm does not persist the `--atomic` flag.
- Rendered resource diff is a compact summary. It can show field-level changes for known Kubernetes kinds, but the manifest diff remains the exact source of truth.
- When rendered object identities do not overlap, Radar reports added/removed context instead of guessing a rename.
- Hook evidence can disappear after Helm records the failure. Radar reports the absence and likely reasons, but cannot reconstruct deleted Job/Pod logs unless Kubernetes or Helm retained them.
- Flux-managed releases should be changed through Flux. Radar links them to the owning `HelmRelease` and warns that direct `helm upgrade` changes may be reconciled back.
- Upgrade source detection depends on source data available to the running Radar environment. Local OSS can use local Helm repo/OCI configuration; Radar Cloud should not assume that local repo config exists, so unresolved source states are reported explicitly.
