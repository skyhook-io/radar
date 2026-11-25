# Workload actions

## Reviewed restarts

Deployments, StatefulSets, DaemonSets, and Argo Rollouts use `POST /api/workloads/{kind}/{namespace}/{name}/restart`. Omit the body or send zero bytes for the existing unconditional restart. Every 200 response includes `message: "Workload restart initiated"`, `restartedAt`, and `changed`.

Radar’s single-workload **Restart** button sends the displayed workload’s UID when the connected Radar advertises `features.workloadRestartPreconditions: true`. It omits the resource version so routine status updates can proceed between opening the workload and clicking Restart. If the capabilities response omits or disables the flag, the button sends an empty POST. Bulk Restart sends an empty POST for each selected workload.

API callers should also confirm the flag before sending a reviewed restart. Older Radars ignore the body and restart unconditionally. Send a single JSON object with the reviewed `uid`. Omit `resourceVersion` for a UID-only restart, or supply the exact version as a nonempty string. A missing or blank UID, a null or empty version, an unknown field, or a repeated field returns 400 before Radar contacts Kubernetes.

```json
{"uid":"…","resourceVersion":"…"}
```

Each patch is bound to the workload UID and the resource version Radar just read. After a Kubernetes conflict, Radar reads the workload again. With only a UID, it retries up to three times if the version moved, checking identity, deletion state, and update strategy before every patch. If the version stayed the same, Radar returns the original conflict. Replaying a UID-only request can restart pods again.

In reviewed mode, a successful patch returns the stored pod-template annotation or Rollout `spec.restartAt` with `changed:true`. Pod replacement happens asynchronously. An existing equivalent marker returns its stored value and `changed:false`. A Rollout cutoff in the same controller second or later already covers the request and also returns `changed:false`.

For an unconditional restart, `restartedAt` is the marker Kubernetes returned, empty if admission removed it. `changed` is true when the patch stored the requested marker and it differs from the prior marker, when known. StatefulSet and DaemonSet unconditional restarts need only patch permission. They have no prior read, so `changed:true` can also describe a repeat in the same second.

Reviewed requests can return the following workload-specific refusals.

| HTTP | Code | Reason |
| --- | --- | --- |
| 404 | `workload_not_found` | The workload no longer exists. |
| 409 | `workload_changed` | The UID or pinned version changed, or all optimistic-lock attempts conflicted with concurrent updates. |
| 409 | `workload_terminating` | Kubernetes has started deleting the workload. |
| 409 | `workload_paused` | The Deployment is paused. |
| 409 | `restart_not_applicable` | The update strategy is OnDelete or the StatefulSet has a positive rolling-update partition. |
| 409 | `restart_not_stored` | Admission changed or removed the marker. The response includes the stored `restartedAt`, empty when absent. |

Connection checks can return 409 `context_changed` or 503 `cluster_unavailable`. Invalid requests return 400, bodies over 1 KiB return 413, and reviewed requests preserve Kubernetes admission and permission 4xx statuses.
