# Connection lifecycle

How Radar names the cluster connection it serves, and how requests are bound to the connection a browser tab is showing. This is the first slice of the connection lifecycle model; later slices move every entry point onto one owner and give each connection its own caches.

## The guarantee

A write from a Radar tab runs only against the connection that tab is showing, or is refused before any Kubernetes work. A read the tab receives comes from the connection it is showing, or is dropped.

Covered today: Deployment, StatefulSet, DaemonSet and Rollout **restart** and **scale**, and the **resource list and detail reads** (`/api/resources/{kind}`, `/api/resources/{kind}/{ns}/{name}`). Other write families (apply, delete, Helm, exec, port-forward, integration actions, MCP) still resolve the current connection when they run.

## Connection identity

- **Admission.** When Radar starts serving a cluster (startup, a switch, Retry, auth recovery, a namespace rescope) it admits a connection with an ID `<bootNonce>-<n>`. IDs never repeat within a process, and the boot nonce keeps them distinct across restarts.
- **Retirement.** The point of no return of any destructive path (switch, rescope, rebuild, auth demotion, shutdown) clears the serving ID before tearing anything down. Between retirement and the next admission nothing is serving.
- **Not health.** A disconnect without teardown (a failed liveness check, a Helm transport error) keeps the ID: it says which connection was admitted, not whether it is healthy.
- **Shutdown** closes admissions, so a late startup publication can't admit a connection after shutdown began.
- **Publication.** Every status publication carries `connectionId`, a sequence number `seq`, and the process start time `bootAt`, on `/api/connection`, SSE `connection_state` frames and the SSE initial snapshot. Browsers order publications by `seq` within one process and by `bootAt` across processes.

Code: `internal/k8s/connection_state.go` (admission, retirement, publication), `internal/k8s/connection_binding.go` (capture).

## Binding

**Server** (`internal/server/connection_binding.go`):
- A bound write captures the serving connection's ID, clients and discovery in one step, under the lock every client install and teardown takes. The handler runs on that capture, so a switch that lands mid-request can't redirect it.
- The request header `X-Radar-Connection` names the connection the caller is showing. A different ID (or `unknown`) gets **409 `context_changed`**. No serving connection, or a context operation holding the lock, gets **503 `cluster_changing`** (retryable). No header: admitted against the current connection (API and MCP callers, older UIs).
- Bound reads are labelled with `X-Radar-Connection: <id>`, or `none` when nothing was serving or the serving connection changed while the response was built.

**Browser** (`web/src/api/connectionBinding.ts`):
- Every status the tab receives (poll, SSE, retry result) feeds the tab's connection, in publication order.
- `apiFetch` sends the tab's connection ID (or `unknown` once the server is known to bind but the tab has none yet). A response labelled with another connection is answered as `context_changed` and the tab re-reads its status.
- `fetchJSON` drops a labelled result if the tab moved to another connection while it was in flight.
- The tab adopts a new connection together with resetting its views: after a switch's `context_changed` the next ID is adopted quietly; otherwise adopting it resets the views. Resource views are keyed on the connection, so dialogs opened on the previous connection close.

A server without connection IDs (older Radar, e.g. behind Radar Hub) reports none; the browser then sends no header and drops nothing.

## Scenario checklist

Run these on major changes to connection, switching or binding code. **Live** scenarios use `scripts/connection-lifecycle-check.sh` against the radar-test EKS clusters (see the script's header for setup). Unit coverage is named where it exists.

| ID | Scenario | Expected | Live | Unit |
|----|----------|----------|------|------|
| L1 | F12: restart sent mid-switch by a tab still showing the old cluster | 503 `cluster_changing`, no write on either cluster | ✓ | `TestBoundWriteRefusedWhileNothingServes` |
| L1b | Same, with no connection header (API caller) | 503, no write | ✓ | same |
| L2 | Stale tab after the switch completed | 409 `context_changed`, no write | ✓ | `TestBoundWriteRefusesStaleConnection` |
| L2b | Current tab after the switch | 200, write on the new cluster only | ✓ | |
| L3 | A→B→A: the first A's ID after returning to A | 409, no write; IDs never repeat | ✓ | `TestBoundWriteRefusesStaleConnection`, `TestConnectionIdentityAdmitRetireAndOrder` |
| — | Switch lands between admission and execution of a write | Write runs on the admitted cluster; the new one gets nothing | | `TestBoundRestartStaysOnAdmittedConnectionAcrossSwitch` (+ control `TestUnboundRestartFollowsTheSwitch`) |
| L4 | Read labelled with the serving connection | Label = serving ID | ✓ | `TestStampReadLabelsTheServingConnection` |
| L5 | F3: status mid-switch | No serving ID, state `connecting` | ✓ | `TestConnectionIdentityAdmitRetireAndOrder` |
| L5b | Read mid-switch | Labelled `none` | ✓ | `TestStampReadLabelsTheServingConnection` |
| L5c | Publication order | `seq` advances | ✓ | same |
| L6 | O1: unreachable target, failing exec plugin, stuck exec plugin | Refused before leaving; serving ID unchanged; old cluster still writable | ✓ | |
| L7 | F1: tracked switch with the first Pod list held 40s | Succeeds | ✓ | |
| L8 | ID from a previous Radar process | 409 | ✓ | browser: `connectionBinding.test.ts` (boot ordering) |
| L9 | Same context name repointed to another cluster, then Retry | New ID; old ID 409; new ID writes to the new cluster | ✓ | |
| L10 | Stop during warm-up | Nothing serving; writes 503 | ✓ | |
| B1 | Tab that missed `context_changed` learns the new connection from a status | Views reset once | | `connectionBinding.test.ts` |
| B2 | Tab that saw `context_changed` then the new ID | No second reset | | `connectionBinding.test.ts` |
| B3 | Response from another connection | Dropped as `context_changed`; status re-read | | `client.connectionBinding.test.ts` |
| B4 | Server without connection IDs | No header sent, nothing dropped | | `client.connectionBinding.test.ts` |
| S1 | Rescope (`--namespace-scope`) rebuild or rollback | New ID after the rebuild; Disconnected if the rollback fails | | `PerformNamespaceRescope` (manual) |
| S2 | Shutdown | No admission after shutdown begins | | `TestConnectionAdmissionsClosedAtShutdown` |

Known scenarios outside this slice (from the switch trace): O7 (a lost `context_changed` with no later status — now covered by B1 for bound views), F10 (stalls outside the switch's budget), and the write families listed above.
