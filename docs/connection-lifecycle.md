# Connection lifecycle

How Radar names the cluster connection it serves, and how requests are bound to the connection a browser tab is showing. This is the first slice of the connection lifecycle model; later slices move every entry point onto one owner and give each connection its own caches.

## The guarantee

A write from a Radar tab runs only against the connection that tab is showing, or is refused before any Kubernetes work. A read the tab receives comes from the connection it is showing, or is dropped.

Covered today: Deployment, StatefulSet, DaemonSet and Rollout **restart** and **scale**, and the **resource list and detail reads** (`/api/resources/{kind}`, `/api/resources/{kind}/{ns}/{name}`). Other write families (apply, delete, Helm, exec, port-forward, integration actions, MCP) still resolve the current connection when they run.

**Only when Radar serves from a kubeconfig.** An in-cluster Radar can't change clusters (no kubeconfig contexts; CAPI connect is refused in-cluster), and during a rolling update two of its pods, each with its own IDs, answer the same browser. It publishes no identity and labels nothing, so its browsers bind nothing; its writes still run on one captured set of clients.

## Connection identity

- **Admission.** When Radar starts serving a cluster (startup, a switch, Retry, auth recovery, a namespace rescope) it admits a connection with an ID `<bootNonce>-<n>`. IDs never repeat within a process, and the boot nonce keeps them distinct across restarts.
- **Retirement.** The point of no return of any destructive path (switch, rescope, rebuild, auth demotion, shutdown) clears the serving ID before tearing anything down. Between retirement and the next admission nothing is serving.
- **Not health.** A disconnect without teardown (a failed liveness check, a Helm transport error) keeps the ID: it says which connection was admitted, not whether it is healthy.
- **Shutdown** closes admissions, so a late startup publication can't admit a connection after shutdown began.
- **Publication.** Every status publication carries `connectionId`, a sequence number `seq`, and the process start time `bootAt`, on `/api/connection`, SSE `connection_state` frames and the SSE initial snapshot. Browsers order publications by `seq` within one process; a different `bootAt` means the process restarted, and the browser follows the process now answering (only one local process serves a tab, so wall clocks are never compared).

Code: `internal/k8s/connection_state.go` (admission, retirement, publication), `internal/k8s/connection_binding.go` (capture).

## Binding

**Server** (`internal/server/connection_binding.go`):
- A bound write captures the serving connection's ID, clients and discovery in one step, under the lock every client install and teardown takes. The handler runs on that capture, so a switch that lands mid-request can't redirect it.
- The request header `X-Radar-Connection` names the connection the caller is showing. A different ID (or `unknown`) gets **409 `context_changed`**. No serving connection, or a context operation holding the lock, gets **503 `cluster_changing`** (retryable). No header: admitted against the current connection (API and MCP callers, older UIs). Refusals are labelled (the serving ID, or `none`) so the caller knows to catch up; their messages say nothing was changed.
- Bound reads are labelled with `X-Radar-Connection: <id>`, or `none` when nothing was serving or the serving connection changed while the response was built.

**Browser** (`web/src/api/connectionBinding.ts`):
- Every status the tab receives (poll, SSE, retry result) feeds the tab's connection, in publication order.
- Publications older than one the tab already saw are ignored for display too (poll and SSE), not only for identity.
- `apiFetch` sends the tab's connection ID (or `unknown` once the server is known to bind but the tab has none yet). A response labelled with another connection is answered as `context_changed` and the tab re-reads its status. A `none` label drops data for a tab that shows a connection, lets errors through with their own code, and also prompts a status re-read.
- `fetchJSON` drops a labelled result if the tab moved to another connection while it was in flight.
- Restart and scale capture the API base and connection when the user confirms (`mutate()`), run at once (never paused offline) and are never retried automatically, so nothing between the click and the request retargets them.
- A write belongs to the view that offers it: a component mounted before the tab moved to another connection (a drawer closing after a reset, a dialog it portaled, a page that outlived the reset) is refused locally with "Nothing was changed" and never sent. Such a page refuses until it is reopened.
- A late reply from a Radar process the tab has seen replaced is ignored.
- A Retry result older than a publication the tab already saw is not displayed; the tab re-reads its status instead.
- Only the mounted API base's views are reset; status identity is recorded under the base the request went to (Radar Hub shares one query client across clusters).
- The tab adopts a new connection together with resetting its views: after a switch's `context_changed` the next ID is adopted quietly; otherwise adopting it resets the views. Resource views are keyed on the connection, so dialogs opened on the previous connection close.

A server without connection IDs (older or in-cluster Radar) reports none; the browser then sends no header and drops nothing.

### Tradeoffs taken

- **Admission at Connected, not at warm-up start.** A tab that shows the old cluster doesn't display the new cluster's progressively warming data until the switch completes (those reads are labelled `none` and dropped). Admitting earlier would make every switch reset the views twice. A tab with no connection yet (startup) shows warm-up data as before.
- **Data from before a tab's first status** is kept if the first connection adopted is the one that served it; a different one resets the views. A switch completing in the first seconds of a tab's life, with `context_changed` lost, is the narrow case left.
- **A write arriving during shutdown** still runs on the connection the tab shows; admissions are closed first so nothing new is admitted.
- **Writes name the tab's connection, refused from views older than it.** A view mounted under an earlier connection can't write, so a write is sent only from a view mounted since the tab adopted its current connection. The stricter design (next iteration) labels each query result with the connection that served it and has an action send the label of the data it acts on; that needs the label carried through React Query's structural sharing, which replaces result objects.
- **A tab that adopts its first connection mid-read** keeps warm-up data labelled `none`; rejecting it would fail startup's in-flight reads with nothing to refetch them.

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
| U1 | Browser: a tab with SSE and status frozen restarts from a stale drawer after another tab switched | Refused, message says nothing changed, no write on either cluster; the tab re-reads status and resets to the new cluster; the switching tab's restart writes the new cluster only | browser (headless Chrome) | |
| U2 | Browser: same, status poll live | The stale tab resets on its own before the user can act | browser | |
| I1 | In-cluster Radar | No identity published, nothing labelled, a named connection ignored | | `TestInClusterRadarBindsNoConnections` |
| B5 | Publication older than one already seen (SSE after poll) | Not displayed | | `connectionBinding.test.ts` |
| B6 | Late status from another API base (Hub remount) | Recorded under its own base; doesn't reset the mounted views | | `connectionBinding.test.ts` |
| B7 | Restart while the browser reports offline | Sent at once, naming the confirmed connection, never retried | | `client.boundMutation.test.tsx` |
| B8 | A host's onMutate holds the restart while the tab moves to another connection | Sent once, naming the confirmed connection | | `client.boundMutation.test.tsx` |
| B9 | Restart from a view mounted before the tab moved to another connection | Refused locally, not sent, "Nothing was changed" | | `client.boundMutation.test.tsx` |
| B10 | Late status from a replaced Radar process; a restarted process with an earlier clock | Ignored; followed | | `connectionBinding.test.ts` |
| B11 | Server refuses a stale write (409 labelled with the new connection) | The server's "Nothing was changed" message reaches the user; the tab re-reads status | U1 | `client.connectionBinding.test.ts` |
| B1 | Tab that missed `context_changed` learns the new connection from a status | Views reset once | | `connectionBinding.test.ts` |
| B2 | Tab that saw `context_changed` then the new ID | No second reset | | `connectionBinding.test.ts` |
| B3 | Response from another connection | Dropped as `context_changed`; status re-read | | `client.connectionBinding.test.ts` |
| B4 | Server without connection IDs | No header sent, nothing dropped | | `client.connectionBinding.test.ts` |
| S1 | Rescope (`--namespace-scope`) rebuild or rollback | New ID after the rebuild; Disconnected if the rollback fails | | `PerformNamespaceRescope` (manual) |
| S2 | Shutdown | No admission after shutdown begins | | `TestConnectionAdmissionsClosedAtShutdown` |

Known scenarios outside this slice (from the switch trace): O7 (a lost `context_changed` with no later status — now covered by B1 for bound views), F10 (stalls outside the switch's budget), and the write families listed above.
