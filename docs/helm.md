# Helm Support

Radar treats Helm as a release system, not just a set of Kubernetes objects. The Helm view combines release metadata, rendered resources, revision history, operation insight, revision comparison, and live Kubernetes evidence around failed hooks.

## Release List

The Helm list shows:

- Release status, chart version, app version, revision, and update time.
- Resource health derived from the current rendered manifest and live Kubernetes status.
- Helm storage namespace. This matters for controllers such as Flux, which may store the Helm release Secret outside the target namespace.
- Flux ownership when Radar can match the release to a Flux `HelmRelease`.
- `lastOperation` for current failed upgrades, rollback-after-failure patterns, explicit rollbacks, and stuck pending operations (including uninstalls).
- A capped operation trail for failed upgrades, rollbacks, rollback-after-failure, and stuck pending operations (including uninstalls).

When `storageNamespace` is present, use it for Helm detail/API/MCP calls. Helm stores release history there even when the chart deploys resources into another namespace. Rollback, upgrade, values preview/apply, and uninstall use the release’s recorded target namespace for objects without an explicit `metadata.namespace`, while history stays in the storage namespace. Objects with an explicit namespace retain it.

## Release Detail

The drawer includes:

- Overview: chart metadata, status, resource health, notes, dependencies, Flux ownership, and operation insight when available.
- History: revision status, description, update time, and operation classification.
- Manifest and values: rendered output and values for the selected release, with values redacted in MCP responses.
- Resources: live status for resources rendered by the current release.
- Hooks: hook events, path, weight, status, run times, delete policies, output-log policies, and diagnostics for failed/running hooks.

Compare opens as a full page instead of rendering inside the drawer. The drawer links to Compare from history rows and operation banners when Radar can identify a useful revision pair.

## Operation Insight

Radar derives a Helm operation signal from release history and the current release status. It distinguishes:

- Active failed upgrades.
- Active pending or stuck installs/upgrades/rollbacks/uninstalls.
- Explicit rollbacks.
- Recovered rollback-after-failure flows.

For active failed or stuck releases, Radar ranks the rendered live resources and surfaces the best resource to inspect when one is unhealthy or not ready. Workloads rank above leaf resources, but a Service or Ingress with a concrete issue can still be selected when it is the strongest signal.

For recovered operations, Radar suggests the revision comparison most likely to explain the recovery:

- Previous completed revision to failed upgrade revision.
- Restored revision to failed upgrade revision for rollback-after-failure.
- Previous completed revision to rollback revision for explicit rollbacks.

Flux-owned Helm releases defer to Flux. Radar shows the owning `HelmRelease` and does not synthesize native Helm operation insight for releases managed by Flux's helm-controller, because the GitOps controller is the authoritative reconciler.

Active native Helm failures and stuck pending operations (including uninstalls) also appear in the global Issues stream as `kind=HelmRelease`, `group=helm.sh`. Recovered rollbacks are deployment history, not live issues; use Helm detail or `get_changes` for those.

Uninstalls that remain `uninstalling` for at least ten minutes appear as `HelmReleaseUninstallStuck` warnings. Radar times them from Helm's `Info.Deleted`, the recorded uninstall start, rather than the last deployment. Revision history preserves `Info.Deleted` separately from the deployment update time; stuck-operation and issue onset timestamps use the uninstall start. A missing start time does not produce an age-based stuck issue.

Helm rewrites `Info.Deleted` at the start of every uninstall attempt, before the pre-delete hook runs, so a retry that fails on the same blocker would otherwise restart the clock and hide the issue for another ten minutes. Radar keeps the earliest uninstall start it has read for the release revision (per context, storage namespace and release name) and uses it for both the elapsed time and the issue onset; it resets when the release leaves `uninstalling` or moves to another revision. Revision history still shows Helm's latest `Info.Deleted`. The anchor lives in the Radar process: after a restart, or once Radar has not read the release for a day, it re-anchors to Helm's latest timestamp.

The issue suggests checking pre-delete hook Jobs and surviving resources with finalizers before retrying the uninstall (Helm re-runs an uninstall stuck in this state). When a hook is the blocker, `helm uninstall --no-hooks` completes the uninstall, but Helm does not remove the Jobs and Pods the hook created, and resources held by finalizers stay Terminating. Radar no longer ties either to the release afterwards, so the issue tells the operator to delete the hook's leftovers and check for Terminating resources. These are investigation steps, not verified blocker diagnoses.

To find them, Radar first lists only the labels of the Helm release Secrets the caller can read. Helm labels each one with the release name, revision and status. Radar then reads in full only the releases whose newest revision is failed or pending, plus any release with the same name in the same read. If any release Secret lacks those labels, Radar reads them all in full, as before. Releases stored with Helm's ConfigMap or SQL drivers are not read for issues. `/api/issues` waits for this read, and the read stops as soon as the caller disconnects.

When that read fails, for example because the API server returns an error or Radar's 60-second request limit runs out, `/api/issues` still answers, with `"unchecked_sources": [{"source": "helm", "reason": "read_failed"}]`: failed or stuck Helm releases may be missing from the response. The MCP `issues` tool returns the same field. A caller that may not list release Secrets gets no Helm issues and no `helm` entry, the same as for any other kind it can't list.

When the read covers several namespaces and only some fail, the issues from the others are kept, and the entry names the ones that failed: `{"source": "helm", "reason": "read_failed", "namespaces": ["payments"]}`. When Radar's ServiceAccount is not allowed to impersonate the user, every user is refused the same way. That is Radar's own access failing, not the user's permissions, so the entry says `"reason": "collector_access_denied"` instead of the read counting as a denial.

The Home Helm card and the MCP `get_dashboard` tool make the same split: they say access is restricted only when Kubernetes denied the caller's own read. Rejected credentials (401) and a missing `impersonate` permission show as a failed read, with the error.

## Failed Upgrades And Rollback Inference

Helm history does not persist whether `helm upgrade --atomic` was set. Radar infers an atomic-style rollback from the revision sequence:

- A failed upgrade revision.
- Followed by a deployed rollback revision.
- With revision descriptions/statuses that point back to the previously deployed release.

Radar surfaces that as a Helm operation instead of making the operator infer it from raw revision rows. The UI and MCP response include the failed revision, rollback revision, target revision, and failure description when Helm recorded one. Radar calls this an inferred rollback-after-failure pattern; it does not claim the exact Helm CLI flag that caused it.

## Revision Compare

The Helm Compare page is optimized for incident debugging. It starts with the rendered Kubernetes manifest diff as the source of truth, then keeps supporting evidence below it:

- Rendered resources: a compact index of Kubernetes object identities and meaningful in-place field changes.
- Effective values: revision-aware computed values diff, including each revision's chart defaults and user overrides.
- User overrides: diff of values explicitly supplied to each revision; the UI shows the user's own values, while MCP applies key-aware redaction.
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

## Known Limits

- Atomic rollback detection is inferred from Helm history because Helm does not persist the `--atomic` flag.
- Rendered resource diff is a compact summary. It can show field-level changes for known Kubernetes kinds, but the manifest diff remains the exact source of truth.
- When rendered object identities do not overlap, Radar reports added/removed context instead of guessing a rename.
- Hook evidence can disappear after Helm records the failure. Radar reports the absence and likely reasons, but cannot reconstruct deleted Job/Pod logs unless Kubernetes or Helm retained them.
- Flux-managed releases should be changed through Flux. Radar links them to the owning `HelmRelease` and warns that direct `helm upgrade` changes may be reconciled back.
- Upgrade source detection depends on source data available to the running Radar environment. Local OSS can use local Helm repo/OCI configuration; Radar Cloud should not assume that local repo config exists, so unresolved source states are reported explicitly.


## Chart sources and upgrades

Radar identifies a chart by its repository URL and chart name. Local repository aliases can differ between machines. OCI sources use the full registry/chart path. Charts with the same name or version in different repositories are separate choices.

Normal upgrades use the saved source. If no source is confirmed, use **Choose chart source** in the release drawer. The picker shows the last recorded source first, followed by other configured sources. A name match is a suggestion, not proof of where the release was installed from.

Use **ArtifactHub** to find a public source, then click **Search ArtifactHub**. Only the installed chart name is sent, after the click. Results show exact chart-name matches, publisher details and addresses. Select a result and add its repository or OCI prefix. This saves configuration without changing the release. Private sources can be entered through **Add manually**.

**Check and record source** compares the chart information Helm saved. Matching copies can come from more than one publisher. To use a source for an upgrade, select it and review the existing version, settings and resource preview before applying. Search badges do not prove the installed chart's origin.

You can associate an original source when its installed package matches the chart Helm stored. Older source records may need confirmation again if they lack a chart binding or a later Helm command changed the chart. This comparison cannot establish the origin of identical copies or verify subchart bodies Helm omitted from release storage.

Values-only edits can use the chart Helm stored. If Helm omitted dependencies from an umbrella chart, record its original source so Radar can restore the exact installed package before editing values.

To use another repository, select it for the next upgrade. Choose an available version, review your settings and preview the rendered resource changes. The old installed version does not need to remain in the selected repository. Preview and apply use the same selected package and settings; if either changes, preview again.

A preview does not guarantee compatibility of hooks, data or immutable fields. Helm does not upgrade CRDs from a chart's `crds/` directory. A failed source change records the attempted source on the failed revision and preserves earlier revision history. Select a source and preview again before retrying; a values-only edit cannot finish the source change.

Programmatic upgrades require a confirmed saved source or an explicit source selection with the preview digest and expected release revision. Name-only inference never authorizes an upgrade. New source-selection controls require the backend's `helmSourceIdentity` capability. Flux-managed releases should be changed through their owning Flux configuration.


Explicit source changes use `POST /api/helm/releases/{namespace}/{name}/source/preview`, followed by `/source/upgrade` or `/source/upgrade-stream`. These routes require the `sourceUpgrade` selection, explicit values and an exact version (body `version` for preview, query `version` for upgrade). Preview returns `previewDigest`; send it back in the selection when applying. The selection also carries `expectedRevision` and `expectedSource` from source status. Older backends return 404 for these routes instead of ignoring the selected source. Ordinary upgrade routes reject source-change fields.

### Chart download checksums

When an HTTP repository index advertises a SHA-256 checksum, Radar checks the downloaded chart archive before reading or using it. An invalid checksum or a mismatch stops chart details, installation, source recording and any upgrade or values operation that downloads that chart. The release stays unchanged.

If the download does not match, refresh the repository and retry. If it still fails, contact the repository maintainer. A stale index and a changed archive can both cause a mismatch.

Existing local Helm flows still accept repositories that omit checksums. Prepared installs for Cloud enrollment continue to require one. A checksum checks agreement with the index; it does not prove who published the chart. OCI pulls keep their registry digest handling. If an HTTP index lists an OCI chart URL, Radar also checks its advertised SHA-256 against the downloaded chart archive.
