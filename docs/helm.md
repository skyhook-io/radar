# Helm Support

Radar treats Helm as a release system, not just a set of Kubernetes objects. The Helm view combines release metadata, rendered resources, revision history, operation insight, revision comparison, and live Kubernetes evidence around failed hooks.

## Release List

The Helm list shows:

- Release status, chart version, app version, revision, and update time.
- Resource health derived from the current rendered manifest and live Kubernetes status, counting only objects the caller can read. A release can place objects in namespaces the caller can't read, or create cluster-scoped kinds they can't list: those objects get no live status, health doesn't count them, and it reads `unknown` rather than `healthy` when a workload or Pod is among them. `statusUnreadCount` counts only unchecked workloads and Pods, because those affect release health. Other unread objects keep their own `statusUnread` markers in Resources.
- Helm storage namespace. This matters for controllers such as Flux, which may store the Helm release Secret outside the target namespace.
- Flux ownership when Radar can match the release to a Flux `HelmRelease`.
- `lastOperation` for current failed upgrades, rollback-after-failure patterns, explicit rollbacks, and stuck pending operations (including uninstalls).
- A capped operation trail for failed upgrades, rollbacks, rollback-after-failure, and stuck pending operations (including uninstalls).

When `storageNamespace` is present, use it for Helm detail/API/MCP calls. Helm stores release history there even when the chart deploys resources into another namespace. Rollback, upgrade, values preview/apply, and uninstall use the release’s recorded target namespace for objects without an explicit `metadata.namespace`, while history stays in the storage namespace. Objects with an explicit namespace retain it.

## Without Access To Secrets

Helm stores each release as a Secret, and Kubernetes has no metadata-only permission, so someone who can't read Secrets can't read Helm's records. The Helm list then shows what it can from objects that carry Helm's metadata, and says so in a callout above the list. It reads no Secret to do this, as the caller or as Radar. A row is a claim made by those objects, not proof of a release: nothing here can confirm that Helm installed it, and anyone who can edit an object can set the same metadata.

Helm stamps what it deploys with the `meta.helm.sh/release-name` and `meta.helm.sh/release-namespace` annotations. Charts conventionally add the `helm.sh/chart` and `app.kubernetes.io/version` labels. Radar groups objects by those two annotations. An object with no annotations isn't counted, even with Helm labels: charts rendered by Argo CD carry the labels with no Helm release behind them. An object with a controller owner isn't counted either, because operators copy their parent's annotations onto what they create.

| Field | Without Secret access |
|---|---|
| Name, namespace | From the annotations |
| Chart, chart version | From `helm.sh/chart`. Unknown if no object has the label. The version is unknown if the label is at or one short of Kubernetes' 63-character limit, since the `helm create` helper truncates to 63 and then trims a trailing `-`. When the chosen chart's objects carry different versions, as after an interrupted upgrade, the version is unknown and `chartVersions` lists them |
| App version | The `app.kubernetes.io/version` on objects of the chosen chart. Unknown if none has it. When they disagree it is unknown and `appVersions` lists the values: a majority would hide the object still on another version, and voting on the two labels separately could pair values no object carries |
| Resources, object health | Live objects the caller can read, which can be fewer than the release has. Secrets are never read, and only kinds Radar is already watching are included. With gaps in the read, health is `unknown` rather than `healthy`, and `healthSummary` still counts what was read. The list shows healthy objects in a neutral label, since they say nothing about Helm's status |
| Flux ownership, history | From a Flux `HelmRelease` the caller can read: its `status.history` supplies the chart, the versions and the history shown in the drawer. Flux's recorded status is shown as Flux's, in the drawer, and never as the release's status |
| Status, revision, last updated | Unknown. Observed rows have no such fields, so nothing can default to "deployed". When the chart or app versions disagree, the list adds a "versions differ" marker beside the status, with the versions in its tooltip |
| Helm history, compare, values, manifest, notes, hooks | Not available: these routes stay denied, and the drawer has no tabs for them. It shows History only where Flux recorded one |
| Install, upgrade, rollback, uninstall | Not available |

Subcharts label their own objects and can outnumber the parent's, so a count can't identify the chart: one real release has 21 objects from its `clickhouse` subchart and 8 from its own `signoz` chart. With several chart names, Radar chooses the one named exactly like the release. Otherwise it shows "Several charts" and lists them rather than guessing. The name match is a convention, not proof: release `redis` of a chart `platform` with a `redis` dependency shows `redis`. That's why the drawer says "Other charts", not "Subcharts".

What this can miss, by design:

- A release that failed before creating any object doesn't appear.
- A release doesn't appear if the caller can read none of its objects, or if it deploys only CRDs from a chart's `crds/` directory, which Helm doesn't stamp. Scanning CRDs wouldn't help: `karpenter-crd`'s CRDs, for example, carry no stamps at all.
- A release doesn't appear if a controller owns all its objects, because owned objects aren't counted. Helm-based operators built with Operator SDK do this: they add an owner reference to everything they render.
- A failed release with objects looks like any other: its objects don't record the failure.
- Objects kept after an uninstall, for example with `helm.sh/resource-policy: keep`, can show up as a release. So can objects that carry the metadata with no release behind them, such as a chart that templates it itself and was applied with `kubectl`.
- Two Flux releases that share a name and target namespace but store in different namespaces show as one row credited to neither HelmRelease, because the stamps can't tell them apart. `get_helm_release` refuses to pick one and names both.
- Someone who can read Secrets in a release's namespace gets Helm's answer for that namespace, even if Helm stores the release elsewhere. A Flux release stored in `flux-system` and deployed into `apps` isn't listed for someone with Secrets in `apps` who can't read its `HelmRelease`: Radar can't tell it from objects left behind, so it keeps to what Helm says there.

Measured in November 2026 on one test cluster against `helm list`, over 20 kinds: 22 of 23 releases found with no false ones, and no wrong chart or version where one was shown.

The list comes from `GET /api/helm/inventory`, which returns `{releases, observed, storageDenied?, objectCoverage?}`. `releases` are ordinary rows from Helm's storage. `observed` rows exist only for releases whose storage the caller didn't read, so anyone who can read Secrets gets exactly what `GET /api/helm/releases` returns, without any object read. Access is judged per namespace: someone with Secrets in `team-a` and `view` on `team-b` gets full rows for `team-a` and observed rows for `team-b`, and a release in `team-a` that Helm no longer has stays gone even if objects from it remain. `storageDenied` names what wasn't read, and `objectCoverage` is what the object read reached, in the [search coverage](mcp.md#search-coverage-and-cel-object-filters) shape. A namespace filter selects by storage namespace for `releases`, as `/api/helm/releases` does, and by release namespace for `observed`, so a Flux release stored elsewhere can answer differently depending on access. On an observed row, an empty `storageNamespace`, `managedByFluxHelmRelease` or `fluxHistory` means Radar got no such evidence, not that there's none.

`GET /api/helm/inventory/{namespace}/{name}` returns one observed release with its resources and Flux history. The namespace is the release's, and a 404 means no object the caller can read counts toward the release, not that the release doesn't exist. A 503 means the read may still settle, so it's safe to retry, and a 409 with `error_code: connection_changed` means Radar's cluster connection changed during the read, so confirm the cluster first. `fluxHistory` is newest first, and its `digest` is Flux's checksum of the stored Helm release. `GET /api/helm/releases/{namespace}/{name}` returns 403 with `error_code: release_read_denied` when the caller's own read of Helm's storage was refused, which is what sends a shared link to the observed drawer. Any other 403, such as Radar's ServiceAccount being refused impersonation, isn't one and shows as an error.

Objects are read through the same walk and per-user gates as search (`internal/search.Scan`): the namespaces the caller can see, exact-kind checks for Roles and RoleBindings, and per-kind checks for cluster-scoped kinds. The namespace picker filters rows by the release's namespace after that read, so a release keeps its cluster-scoped objects and those in other namespaces. Nothing is cached across callers. Without authentication the caller is Radar's own identity, so a default chart install with `rbac.secrets=false` gets this view too, and it shows nothing `/api/resources` doesn't.

Home's Helm card (`GET /api/dashboard/helm`) and the MCP `get_dashboard` tool still read Helm's storage only. When some namespaces couldn't be read they return `unreadNamespaces`, in the same shape as `storageDenied`, and the card says where Helm's records weren't read, so a partial count doesn't pass for the whole. When none could be read, the card says Helm's records are restricted and points to the Helm view.

A Radar too old to serve the inventory doesn't advertise `helmInventory`, so the UI reads `/api/helm/releases` and keeps its "Access Restricted" state.

A failed read isn't a denial: if Radar can't check which namespaces the caller may read, the inventory returns 503 so the request can be retried, a connection change during the read returns 409 `connection_changed`, and any failure of the storage read other than the caller's own denial fails it as it fails `/api/helm/releases`. A request naming only namespaces outside the `--namespace-scope` pin gets an empty answer with no `storageDenied`.

## Release Detail

The drawer includes:

- Overview: chart metadata, status, resource health, notes, dependencies, Flux ownership, and operation insight when available.
- History: revision status, description, update time, and operation classification.
- Manifest and values: rendered output and values for the selected release, with values redacted in MCP responses.
- Resources: live status for resources rendered by the current release. An object the caller can't read shows "No access" instead of a status, carries `statusUnread` with the read code, and doesn't open. When Radar couldn't check the caller's access it shows "Unknown" instead, since that isn't a denial.
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

Flux-owned Helm releases defer to Flux. Radar shows the owning `HelmRelease` only when the caller can read its namespace; otherwise it shows a generic Managed by Flux label without the controller name or link. It does not synthesize native Helm operation insight for releases managed by Flux's helm-controller, because the GitOps controller is the authoritative reconciler.

Active native Helm failures and stuck pending operations (including uninstalls) also appear in the global Issues stream as `kind=HelmRelease`, `group=helm.sh`. Recovered rollbacks are deployment history, not live issues; use Helm detail or `get_changes` for those.

Uninstalls that remain `uninstalling` for at least ten minutes appear as `HelmReleaseUninstallStuck` warnings. Radar times them from Helm's `Info.Deleted`, the recorded uninstall start, rather than the last deployment. Revision history preserves `Info.Deleted` separately from the deployment update time; stuck-operation and issue onset timestamps use the uninstall start. A missing start time does not produce an age-based stuck issue.

Helm rewrites `Info.Deleted` at the start of every uninstall attempt, before the pre-delete hook runs, so a retry that fails on the same blocker would otherwise restart the clock and hide the issue for another ten minutes. Radar keeps the earliest uninstall start it has read for the release revision (per context, storage namespace and release name) and uses it for both the elapsed time and the issue onset; it resets when the release leaves `uninstalling` or moves to another revision. Revision history still shows Helm's latest `Info.Deleted`. The anchor lives in the Radar process: after a restart, or once Radar has not read the release for a day, it re-anchors to Helm's latest timestamp.

The issue suggests checking pre-delete hook Jobs and surviving resources with finalizers before retrying the uninstall (Helm re-runs an uninstall stuck in this state). When a hook is the blocker, `helm uninstall --no-hooks` completes the uninstall, but Helm does not remove the Jobs and Pods the hook created, and resources held by finalizers stay Terminating. Radar no longer ties either to the release afterwards, so the issue tells the operator to delete the hook's leftovers and check for Terminating resources. These are investigation steps, not verified blocker diagnoses.

To find them, Radar first lists only the labels of the Helm release Secrets the caller can read. Helm labels each one with the release name, revision and status. Radar then reads in full only the releases whose newest revision is failed or pending, plus any release with the same name in the same read. If any release Secret lacks those labels, Radar reads them all in full, as before. Releases stored with Helm's ConfigMap or SQL drivers are not read for issues. `/api/issues` waits for this read, and the read stops as soon as the caller disconnects.

When that read fails, for example because the API server returns an error or Radar's 60-second request limit runs out, `/api/issues` still answers, with `"unchecked_sources": [{"source": "helm", "reason": "read_failed"}]`: failed or stuck Helm releases may be missing from the response. The MCP `issues` tool returns the same field. A caller denied every release Secret it asked about gets no Helm issues and `{"source": "helm", "reason": "caller_access_denied"}`, because the Helm view still lists releases for them and an empty answer would read as those releases being fine. A caller denied only some namespaces keeps the issues from the others, and the entry names the denied ones, `{"source": "helm", "reason": "caller_access_denied", "namespaces": ["team-b"]}`, whether Radar tried them and was refused or left them out up front because the caller can't list Secrets there. When a permission check behind that choice goes unanswered, the namespaces it left out are reported as `read_failed` instead, since they may not be denied.

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

Use `list_helm_releases` first for broad Helm deployment triage. It returns release status, health, storage namespace, Flux ownership, and operation signals. When the caller can't read all of Helm's storage it returns an object instead of an array: `{summary, releases, observed, storageDenied, objectCoverage}`, with the same meanings as [the inventory](#without-access-to-secrets). The `summary` says in words what's unknown and what the object read missed, and `storageDenied` names at most 16 namespaces. For such a release, `get_helm_release` returns `{summary, observed, unavailable}`: the same observed release as the drawer, with its objects, Flux history and object coverage. The `summary` says what's unknown, and `unavailable` lists every `include` requested, since each needs Helm's records. Pass the observed row's `storageNamespace` when it has one, as for any release: a readable Flux HelmRelease that stores the release there decides which release it is. Passing the release's own namespace for a release Flux stores elsewhere returns an error naming the namespace to pass. When none of the objects the caller can read belong to the release, the error still says the release isn't known to be missing.

Use `get_helm_release` for detail:

- The default response includes owned resources, resource health, Flux ownership, current operation signal, hooks, and hook diagnostics when present. Flux ownership uses `managedByFlux: true`; `managedByFluxHelmRelease` is included only when the controller namespace is readable. Resources the caller can't read have no status and a `statusUnread` code, and `statusUnreadCount` counts only unread workloads and Pods; health covers the readable workloads and Pods, as in the list.
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
- Flux-managed releases should be changed through Flux. Radar links them to the owning `HelmRelease` only when its namespace is readable. Otherwise it shows Managed by Flux without a controller name or link. Direct MCP Helm writes are refused in either case. This filters the derived controller reference and UI link, not Flux metadata already present in a release manifest the caller can read.
- Without sign-in, Radar asks on each read whether it can list Secrets cluster-wide. When it can't, it skips the namespaces where its permission probe saw Secrets refused and reads the rest, including any the probe didn't answer. On a default install that is one cluster-wide list, whose denial covers every namespace, instead of one denied list per namespace on every load. A Secrets grant in a single namespace made after the probe isn't seen until Radar probes again, on a context switch or restart.
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

### Repository credentials and redirects

For HTTP repositories, Radar sends repository passwords and client certificates only to the configured address's scheme, hostname and port. A redirect or a chart URL on another origin is fetched without those credentials by default. Public chart hosts are fetched without repository credentials. By default, on a different origin, Radar verifies the server certificate using system trust plus any configured CA, even if verification was skipped for the repository.

If a private repository deliberately shares credentials with its chart host, its Helm configuration must explicitly enable `pass_credentials_all`. This also permits sending the client certificate to that host. Use it only for hosts you trust. Redirected Basic authentication still follows Go's hostname restrictions; Radar does not restore a header Go removed.

Custom CA certificates, client certificates and `insecure_skip_tls_verify` remain supported, including configurations with both a CA file and skipped certificate verification. If a repository address redirects from HTTP to HTTPS, configure its final HTTPS address to keep authenticated requests on the configured origin.

### Chart download checksums

When an HTTP repository index advertises a SHA-256 checksum, Radar checks the downloaded chart archive before reading or using it. An invalid checksum or a mismatch stops chart details, installation, source recording and any upgrade or values operation that downloads that chart. The release stays unchanged.

If the download does not match, refresh the repository and retry. If it still fails, contact the repository maintainer. A stale index and a changed archive can both cause a mismatch.

Existing local Helm flows still accept repositories that omit checksums. Prepared installs for Cloud enrollment continue to require one. A checksum checks agreement with the index; it does not prove who published the chart. OCI pulls keep their registry digest handling. If an HTTP index lists an OCI chart URL, Radar also checks its advertised SHA-256 against the downloaded chart archive.

### Agent operations

External MCP clients can install, upgrade, roll back and uninstall releases with
[`manage_helm`](mcp.md#manage_helm), as the caller and behind the same permission
preflight as the UI. Upgrade and rollback have an advisory preview, and uninstall
is a preview followed by a confirmation. `get_helm_release` adds the stored chart's
defaults and schema, upgrade versions and install notes.

MCP can't register sources. An MCP install needs a source already configured for
Radar (locally, `helm repo add NAME URL`). A version change needs a confirmed
source on the release; a same-version values change can reuse a complete stored
chart.
