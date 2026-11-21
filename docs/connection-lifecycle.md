# Connection lifecycle

How Radar names the cluster connection it serves, and how a write is bound to the connection the browser tab is on. This is the first slice of the connection lifecycle model; later slices move every entry point onto one owner and give each connection its own caches.

## The guarantee

A write from a Radar tab runs against the connection the tab is on, or is refused before any Kubernetes work. In particular, a tab still showing cluster A after another tab switched Radar to B can't restart or scale B's same-named workload.

Covered today (the routes wrapped with `bindWrite` in `internal/server/server.go`):
- Workloads: **restart** and **scale** (single and bulk), Deployment **pause** and **resume**, **set image**, **rollback** (including Argo Rollouts), and the Argo Rollouts actions (abort, retry, promote, promote-full, skip-step).
- CronJobs: **trigger**, **suspend**, **resume**.
- Resources: **apply**, **update** (YAML edit) and **delete**.
- Nodes: **cordon**, **uncordon** and **drain**. A drain keeps running on the connection it was admitted to, even if Radar switches meanwhile.
- GitOps: Argo CD **sync**, **refresh**, **rollback**, **terminate**, **suspend**, **resume**; Flux **reconcile**, **sync with source**, **suspend**, **resume**.
- Velero: **back up now**, schedule **pause** and **resume**, **delete backup**. Their checks of the Velero server's state read the shared cache, which a switch completing mid-request could replace; the write itself stays on the admitted connection.

- CloudNativePG: Cluster, ScheduledBackup and Pooler **actions**. The CNPG service captures its clients and caches in one step; it also checks there that the connection is still the bound one (`k8s.StillBound`), so the whole capture belongs to it.

- Pods and nodes: **exec** (the terminal WebSocket names the connection in its URL, `?connection=`, since a browser can't set headers on a WebSocket), **file upload**, pod **debug containers**, node **debug pods** (create and clean up), and **port-forward** start. Sessions already end when Radar switches.

- Helm: **install**, **upgrade** (including from a source and by editing values), **rollback**, **uninstall**, streaming variants included. Helm builds its clients from the serving connection when the action starts and keeps them for the whole action, so the window between the binding check and that build is the only gap.

- CAPI **connect**: a tab that still shows the previous management cluster can't read a same-named CAPI Cluster's kubeconfig from the new one and switch Radar to it.

- Reachability: the in-cluster trace test, which creates probe Jobs. CloudNativePG **restore validation** notes.

Code that only has a context (MCP tools, reachability) gets its clients from `k8s.ClientFromContext` / `DynamicClientFromContext`, which return the bound connection's clients when the request was admitted with one.

Diagnose (the in-product agent) is fenced by its runs instead: every cluster switch marks all runs stale (`RunManager.OnContextSwitch`), and a stale run takes no further turns, so an investigation of one cluster can't continue, or apply a fix, on another.

Not covered: external MCP callers name no connection, so each tool call uses the connection serving when it starts, as before. Integration writes that look like reads (Velero's download requests behind run messages, contents and logs) aren't bound. An interrupted Helm action is not recovered (the plan's "outcome recovery" for Helm). They still resolve the current connection when they run.

**Only when Radar serves from a kubeconfig.** An in-cluster Radar can't change clusters (no kubeconfig contexts; CAPI connect is refused in-cluster), and during a rolling update two of its pods, each with its own IDs, answer the same browser. It publishes no ID and ignores one a request names; its writes still run on one captured set of clients.

## Connection identity

- **Admission.** When Radar starts serving a cluster (startup, a switch, Retry, auth recovery, a namespace rescope) it admits a connection with an ID `<bootNonce>-<n>`. IDs never repeat within a process, and the boot nonce keeps them distinct across restarts.
- **Retirement.** The point of no return of any destructive path (switch, rescope, rebuild, auth demotion, shutdown) clears the serving ID before tearing anything down. Between retirement and the next admission nothing is serving.
- **Not health.** A disconnect without teardown (a failed liveness check, a Helm transport error) keeps the ID: it says which connection was admitted, not whether it is healthy.
- **Shutdown** closes admissions, so a late startup publication can't admit a connection after shutdown began.
- **Publication.** The ID is published as `connectionId` on `/api/connection`, the Retry result, SSE `connection_state` frames and the SSE initial snapshot.

Code: `internal/k8s/connection_state.go` (admission, retirement, publication), `internal/k8s/connection_binding.go` (capture).

## Binding

**Server** (`internal/server/connection_binding.go`):
- A bound write captures the serving connection's ID, clients and discovery in one step, under the lock every client install and teardown takes. The handler runs on that capture, so a switch that lands mid-request can't redirect it.
- The request header `X-Radar-Connection` names the connection the caller is on. A different ID gets **409 `context_changed`**. No serving connection, or a context operation holding the lock, gets **503 `cluster_unavailable`** (retryable). No header: admitted against the current connection (API and MCP callers, older UIs). Both refusals say nothing was changed.

**Browser** (`web/src/api/connectionBinding.ts`):
- The tab keeps the latest connection ID it heard (status poll, SSE frame, Retry result) and `apiFetch` sends it on every request. Writes that can't use `apiFetch` send it themselves: the file upload's XHR sets the header, and the terminal adds `?connection=` to its WebSocket URL (`withConnectionParam`). A status with no ID (mid-switch) keeps the last one; the server refuses writes then anyway.
- Views reset when the tab learns the context changed, as before this slice: from `context_changed`, or from a status poll (every 30s while connected, also in background tabs and on focus) that shows a different context.
- After a refusal, the toast says nothing was changed; the tab catches up on its next status poll or when it regains focus.
- A server without connection IDs (older or in-cluster Radar) publishes none, and the browser sends no header.

### Tradeoffs taken

The browser doesn't try to keep the ID in lockstep with what each view displays. That leaves short windows where a view still shows A while the tab already sends B's ID, for example a drawer's 300ms exit animation after a reset, or a status reply that arrives out of order. A write sent from such a view in that window runs on B. These windows are well under a second and need a click inside them; closing them took ordering rules, view generations and response labelling that proved hard to get right, so they are accepted. A tab that has been stale for longer (background, SSE dropped) still sends A's ID and is refused.

Also accepted:
- **A write arriving during shutdown** still runs on the connection the tab is on; admissions are closed first so nothing new is admitted.
- **Reads aren't bound.** A stale tab can display data from the new cluster before it resets; it can't write with it, since it still sends the old ID.
- **A context repointed to another cluster in the kubeconfig, then Retry,** keeps the same context name, so views refetch rather than reset; writes name the new connection from then on.
- **A host QueryClient that retries mutations** (Radar's own doesn't) could resend a refused restart after the tab adopted the new ID.

## Scenario checklist

Run these on major changes to connection, switching or binding code. **Live** scenarios use `scripts/connection-lifecycle-check.sh` against the radar-test EKS clusters (see the script's header for setup); **browser** scenarios use `scripts/connection-lifecycle-ui-check.mjs` against a Radar started on the same fixtures. Unit coverage is named where it exists.

| ID | Scenario | Expected | Live | Unit |
|----|----------|----------|------|------|
| L1 | Restart sent mid-switch by a tab still showing the old cluster | 503 `cluster_unavailable`, no write on either cluster | ✓ | `TestBoundWriteRefusedWhileNothingServes` |
| L1b | Same, with no connection header (API caller) | 503, no write | ✓ | same |
| L2 | Stale tab after the switch completed | 409 `context_changed`, no write | ✓ | `TestBoundWriteRefusesStaleConnection` |
| L2b | Current tab after the switch | 200, write on the new cluster only | ✓ | |
| L3 | A→B→A: the first A's ID after returning to A | 409, no write; IDs never repeat | ✓ | `TestBoundWriteRefusesStaleConnection`, `TestConnectionIdentityAdmitAndRetire` |
| R1 | Each bound write route, through the router, with a stale ID | 409, no write on either cluster | | `TestBoundWriteRoutesRefuseStaleConnection` |
| — | Switch lands between admission and execution of a write | Write runs on the admitted cluster; the new one gets nothing | | `TestBoundRestartStaysOnAdmittedConnectionAcrossSwitch` (+ control `TestUnboundRestartFollowsTheSwitch`) |
| L4 | Status names the serving connection | `connectionId` set | ✓ | `TestConnectionIdentityAdmitAndRetire` |
| L5 | Status mid-switch | No serving ID, state `connecting` | ✓ | `TestConnectionIdentityAdmitAndRetire` |
| L6 | Unreachable target, failing exec plugin, stuck exec plugin | Refused before leaving; serving ID unchanged; old cluster still writable | ✓ | |
| L7 | Tracked switch with the first Pod list held 40s | Succeeds | ✓ | |
| L8 | ID from a previous Radar process | 409 | ✓ | |
| L9 | Same context name repointed to another cluster, then Retry | New ID; old ID 409; new ID writes to the new cluster | ✓ | |
| L10 | Stop during warm-up | Nothing serving; writes 503 | ✓ | |
| U1 | Browser: a tab with SSE and status frozen restarts from a stale drawer after another tab switched | Refused, message says nothing changed, no write on either cluster; the tab resets to the new cluster on its next status poll; the switching tab's restart writes the new cluster only | `connection-lifecycle-ui-check.mjs` | |
| I1 | In-cluster Radar | No ID published, a named connection ignored | | `TestInClusterRadarBindsNoConnections` |
| B1 | Statuses naming IDs, then one with none (mid-switch); another API base | Sends the latest ID; keeps it through the gap; each base its own | | `connectionBinding.test.tsx` |
| B2 | Server publishes no ID (older or in-cluster Radar) | No header sent | | `connectionBinding.test.tsx` |
| B3 | Server refuses a stale restart | The server's "Nothing was changed" message reaches the user | U1 | `connectionBinding.test.tsx` |
| S1 | Rescope (`--namespace-scope`) rebuild or rollback | New ID after the rebuild; Disconnected if the rollback fails | | `PerformNamespaceRescope` (manual) |
| S2 | Shutdown | No admission after shutdown begins | | `TestConnectionAdmissionsClosedAtShutdown` |

Known scenarios outside this slice: a lost `context_changed` with no later status, stalls outside a switch's time budget, and the write families listed above.

## Follow-ups

- **Diagnostics.** The diagnostics report keeps its sections on one cluster by checking that the cache and context didn't change during collection. Once connections have their own caches, it should capture one connection the way a bound write does, report its ID and the tracked switch, and drop those checks.
