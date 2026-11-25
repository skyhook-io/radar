# Longhorn workspace

`GET /api/longhorn/workspace` is the read behind the Longhorn workspace: every Longhorn `Volume` the caller can read, with its health, its healthy replicas and the PV it backs now. It is read-only. Rules live in `pkg/longhorn` (pure), the service in `internal/longhorn`, the handler in `internal/server/longhorn_workspace.go`; the wire types are mirrored in k8s-ui's `components/longhorn/types.ts` and fetched by `useLonghornWorkspace` (`web/src/api/longhorn.ts`), gated on the `longhornWorkspace` capability. The Volume drawer and the Issues themselves are described in [integrations.md](integrations.md#longhorn).

## Scope and access

- The namespace picker is not applied: Longhorn keeps its objects in its own namespace (usually `longhorn-system`), while the picker selects application namespaces. `--namespace-scope` still bounds the read (`forcedNamespace`).
- Volumes and Engines are each authorized on the caller's own `list` access. A caller without the cluster-wide list is checked only in the namespaces where Radar holds Longhorn objects, and its read is always `partial`, so the answer never shows whether those namespaces held more. No namespace the caller did not name is reported as denied.
- PVs are read only when the caller may list them cluster-wide, and only when some Volume was read. A PV's claim is named only to a caller who may list PVCs in the claim's namespace.

## Values and their sources

| Value | Source | When it can't be known |
|---|---|---|
| `coverage.volumes`, `coverage.engines` | the caller's list access and Radar's cache (`integration.KindCoverage`) | `denied`, `uncached`, `syncing`, `error`, `notInstalled` |
| `coverage.persistentVolumes` | the caller's cluster-wide `list persistentvolumes` and Radar's PV cache | `denied`; `syncing` while the cache loads; `uncached` when Radar doesn't watch PVs; the Volumes' state when no Volume was read |
| `counts` | `health` of each Volume read | `null` when no Volume was read. `complete: false` when the Volumes read are not all of them: each count is then a lower bound, never the cluster's total |
| `health` | `status.robustness`, and `status.state` for a detached Volume (`longhorn.VolumeHealth`) | `unknown` |
| `state`, `robustness`, `lastDegradedAt` | `status` as Longhorn writes it | absent |
| `size` | `spec.size` (bytes, a decimal string) | absent |
| `actualSize` | `status.actualSize` | absent |
| `numberOfReplicas` | `spec.numberOfReplicas`, the requested count | absent |
| `healthyReplicas` | the Volume's Engine (`spec.volumeName`; while it migrates, the one with `spec.active`): replicas in `status.replicaModeMap` with mode `RW` | `null`, never the requested count; `healthyReplicasUnknown` says why: `engineUnread` (no Engine access in that namespace), `noEngine`, `engineAmbiguous`, `noModes` (the Engine reports none, as when it is stopped) |
| `use.persistentVolume` | the live PV whose `spec.csi.driver` is `driver.longhorn.io` and `volumeHandle` is the Volume's name, never `status.kubernetesStatus` | `use.state: unknown` when the PVs weren't read, the Volume's name is shared by another Volume read, or several PVs name it; `none` only when every PV was read and none names it |
| `use.claim` | the PV's `spec.claimRef`, while the PV is `Bound` | absent when the PV isn't Bound; `claimWithheld: true` when the caller can't list PVCs in its namespace |
| `issues`, `issueTotal` | the Issues engine's Longhorn rules over the Volumes read: faulted is critical, degraded a warning whose `first_seen` is `lastDegradedAt` | only Volumes the caller read raise rows; `first_seen` is absent when the onset isn't recorded. The first 100 are returned, `issueTotal` counts all |

Degraded is routine while Longhorn rebuilds replicas after a drain or restart; `lastDegradedAt` says how long it has lasted. Detached is not a problem and raises no Issue.

## Screens

`/longhorn` (Overview) and `/longhorn/volumes` live inside Resources, under the sidebar's Integrations as **Block storage / Longhorn**; screens in `web/src/components/longhorn/`, gated on `longhornWorkspace` (an older Radar shows the "needs a newer Radar" note). The namespace picker is disabled there, with a tooltip saying why. Live updates: any `longhorn.io` change refetches the workspace.

- **Overview**: the health counts (faulted, degraded, healthy, detached, unknown). With `counts.complete: false` each carries `≥`; with `counts: null` each reads unknown, never 0. Each count opens Volumes filtered to that health. **Needs attention** lists faulted Volumes (red), then degraded ones (amber), the longest degraded first, each with how long it has been degraded (`lastDegradedAt`, or that Longhorn didn't record it), its healthy replicas when known, its claim (a link; "a claim you can't read" when withheld; "can't tell" when the use is unknown) and a next step. Detached raises no row. An empty list says "No volume Radar can read is faulted or degraded" when the read was partial.
- **Volumes**: every Volume read, with health, state, robustness, size (and actual size), healthy / requested replicas, and claim; health chips (`?health=`) and a search over Volume, namespace, claim and PV names. Healthy replicas Radar couldn't read show `?`, with `healthyReplicasUnknown` as the reason on hover. A row opens the Volume drawer.
- **Sidebar badge**: faulted + degraded from `counts`; a lower bound on a partial read, and unknown rather than 0 when a partial read found none.
