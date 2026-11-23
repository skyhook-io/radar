# kagent workspace demo

This seeds an **existing cluster running kagent 1.x and Agent Substrate**.
It creates `radar-kagent-demo` and `radar-kagent-demo-consumers`; every object
it submits carries `app.kubernetes.io/part-of=radar-kagent-demo`.

```bash
make kagent-demo
./scripts/kagent-demo.sh --status
./scripts/kagent-demo.sh --cleanup
# Explicit opt-in to another context, without switching current-context:
./scripts/kagent-demo.sh --context my-test-cluster
./scripts/kagent-demo.sh --context my-test-cluster --cleanup
```

Without `--context NAME`, **every mode** refuses unless current-context starts
with `radar-test-`. `KUBECONFIG` is honored. Commands pin that context for the
whole run and never switch the shared current-context. The script checks the
served `api.kagent.dev/v1alpha3` resources and Substrate API before creating
anything, and refuses to overwrite an object without its ownership label.

The source runtime defaults to Harness `kagent/kagent-default`. Override with
`KAGENT_DEMO_RUNTIME_NAMESPACE` and `KAGENT_DEMO_HARNESS`. Its kagent Harness
spec and referenced WorkerPool spec are copied into each demo namespace,
using one worker replica and an isolated ServiceAccount per namespace. Each
pool carries kagent’s worker-pool-name and worker-pool-namespace labels with its
new coordinates: prepared templates select these labels; omitting them leaves
active workers that cannot place any golden build (`noMatchingPool`). The
source sandbox classes, worker image and snapshot location must already work
on this cluster. This consumes two additional workers and preparation/snapshot
storage while installed. `WAIT_SECONDS` defaults to 600 per preparation wait;
a first image pull can take several minutes. If a wait fails, use `--status`
and inspect the Agent conditions and WorkerPool Pods; cleanup remains available.

## Screens and scenarios

Open Radar on this context, select **both demo namespaces**, then **kagent →
Overview**. The Configuration sections below are on each Agent’s Overview tab.
The namespace picker narrows inventory and findings; selecting only
the consumer namespace keeps cross-namespace references visible on Agent pages,
but the referenced model's own finding is outside the selected inventory.

| Object (main namespace unless stated) | What to inspect |
|---|---|
| Agent `missing-model` | Overview Needs attention and Agents: missing ModelConfig, latest change blocked, no prepared revision. Agent → Configuration names `absent-model`. |
| ModelConfig `missing-secret`, Agent `missing-secret` | Overview folds the Agent's matched cause beneath the model; Models → `missing-secret` names absent Secret `absent-key`, impact and next step. Agent's configuration names the same dependency. |
| ModelConfig `missing-key`, Agent `missing-key` | Models and Overview report missing `OPENAI_API_KEY` in existing Secret `wrong-key`; dependency and Agent pages show coordinates, never the fake Secret value. |
| RemoteMCPServer `unreachable`, Agent `unreachable-mcp` | Overview and MCP servers show failed discovery, unknown tools offered, and a prepared consumer that may run without these tools. Agent → Configuration shows the failing server separately from its prepared revision. |
| RemoteMCPServer `tools`, Agent `unoffered-tool` | MCP page offers `demo_echo`; the Agent asks for `demo_not_offered`. Agent → Overview → Configuration calls out the unoffered tool even though the Agent prepares. |
| Agent `blocked-edit` | Script first applies a working Agent and waits for Ready, then changes its model to `absent-model`. Overview/Agents and the Agent page show the blocked latest change alongside the previous successful revision for new sessions. |
| Agent `cross-namespace` in consumer namespace | Agents and Configuration resolve `shared-model` in the main namespace; Models → `shared-model` shows the cross-namespace consumer. `allowedNamespaces.from: All` permits this reference. |
| Agent `sharing-refused` in consumer namespace | Overview/Agents and Configuration show `restricted-model` exists but refuses the cross-namespace use (`allowedNamespaces.from: Same`), independently of the model's own accepted status. |
| ModelConfig `staged-model` | Overview's unused configuration section and Models show a broken, unused model (missing `staged-key`), with no affected Agents; it may be staged. |

Conditions, discovered tools and revisions are written by the real controllers;
the script never patches status. Waits prove `blocked-edit`, `unreachable-mcp`,
`unoffered-tool` and `cross-namespace` prepared before finishing. Other failures
may take another controller pass to settle. Rerunning restores the working
blocked-edit first and then breaks it again. The demo does not create sessions,
so the Sessions view should not gain conversation outcomes or crash rows.

## Why this shape

The main namespace holds all credentials and shared models. The second makes
sharing permission observable independently of object existence. The refusal
uses its own `restricted-model`, keeping that finding off the otherwise working
model used by the tool scenarios. Two local Harnesses and WorkerPools satisfy the controller's same-namespace runtime
references without editing the installation. A tiny `python:3.12-slim`
Deployment serves OpenAI-compatible chat completions and a streamable HTTP MCP
endpoint. The latter offers exactly `demo_echo`; the unreachable server points
to a closed port on the same Service. This avoids external model/MCP services
and real API keys. All Secret literals are fake. Readiness probes and real
controller waits keep the prepared-versus-blocked distinction reproducible.

This follows the lab stub's standard-library HTTP server and chat completion
streaming approach, with destructive prompt triggers deliberately omitted.

## Boundaries and cleanup

The script does **not** install CRDs, kagent, Substrate, Helm releases, change
Helm values, runtime installation defaults, existing namespaces or `spike-*`
fixtures. It does not grant its workers Kubernetes cluster-wide permissions,
invoke tools, create real model requests, manufacture runtime state or induce
memory crashes. Worker setup still relies on the installed Substrate controller
and its existing infrastructure and sandbox configuration.

`--status` lists labelled fixture objects in both namespaces. `--cleanup`
checks ownership, refuses objects without the label or a verified ownership
chain back to a labelled object, then deletes only the two named, labelled namespaces and their controller-created children. Kubernetes-generated
`default` ServiceAccounts, root CA ConfigMaps and Events are expected. Cleanup
never deletes CRDs or shared installation objects. Substrate owns its external
snapshot/actor-template retention; deleting Kubernetes fixtures does not claim
to garbage-collect object-store snapshots.

### Manual memory-crash lane

Use an isolated test cluster and a separate manually owned Agent/Harness. First
prepare it with a working model and record its revision. Enable an execution
tool in that Agent's Harness, start a session, and record the session actor ID.
Run an allocation larger than that actor's configured memory limit **inside the
session sandbox**, never on the worker host (for example, Python allocating a
bytearray larger than the recorded actor limit). Observe the real actor crash
and its worker log evidence. In Radar inspect Agent → Sessions, the crashed
session details and Agent Substrate's worker evidence; latest configuration can
remain prepared while runtime has crashed. Remove the manual session and its
separately owned Agent/Harness afterward. Do not change installation-wide memory
defaults or reuse an existing user's session. This lane is intentionally not
part of `up` or its cleanup.
