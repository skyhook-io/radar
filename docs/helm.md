# Helm Support

Radar treats Helm as a release system, not just a set of Kubernetes objects. The Helm view combines release metadata, rendered resources, revision history, operation inference, and live Kubernetes evidence around failed hooks.

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

- Overview: chart metadata, status, resource health, notes, dependencies, and Flux ownership.
- History: revision status, description, update time, and operation classification.
- Compare: revision-to-revision summary plus values diff, manifest diff, notes diff, and rendered resource set diff.
- Manifest and values: available to member-level Cloud users because Helm manifests and values can contain inline Secret material.
- Resources: live status for resources rendered by the current release.
- Hooks: hook events, path, weight, status, run times, delete policies, output-log policies, and diagnostics for failed/running hooks.

## Failed Upgrades And Atomic Rollback

Helm history does not persist whether `helm upgrade --atomic` was set. Radar infers an atomic-style rollback from the revision sequence:

- A failed upgrade revision.
- Followed by a deployed rollback revision.
- With revision descriptions/statuses that point back to the previously deployed release.

Radar surfaces that as a Helm operation instead of making the operator infer it from raw revision rows. The UI and MCP response include the failed revision, rollback revision, target revision, and failure description when Helm recorded one.

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
- `include=values_diff` returns redacted user-supplied values diff.
- `include=notes_diff` returns release notes diff.
- `include=resource_diff` returns added/removed/unchanged rendered resource identities between revisions.

`get_changes` includes the Kubernetes-resource timeline plus recent native Helm release deployment and operation history (`source: helm`). Use it when an agent asks what changed before an incident. For full Helm revision history, operation details, hook diagnostics, values, and diffs, use the Helm MCP tools above.

## Known Limits

- Atomic rollback detection is inferred from Helm history because Helm does not persist the `--atomic` flag.
- Resource diff is identity-level: added, removed, unchanged rendered resources. Use manifest/values diff for field-level changes.
- Hook evidence can disappear after Helm records the failure. Radar reports the absence and likely reasons, but cannot reconstruct deleted Job/Pod logs unless Kubernetes or Helm retained them.
- Flux-managed releases should be changed through Flux. Radar links them to the owning `HelmRelease` and warns that direct `helm upgrade` changes may be reconciled back.
