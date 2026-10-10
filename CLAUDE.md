# CLAUDE.md

## Project Overview

Radar is a modern Kubernetes visibility tool — local-first, no account required, no cloud dependency, fast. It provides topology visualization, event timeline, service traffic maps, resource browsing, Helm management, cluster audit (best-practices scanning), and Kubernetes upgrade impact analysis. Runs as a kubectl plugin (`kubectl-radar`) or standalone binary and opens a web UI in the browser. Open source, free forever. Built by Skyhook.

## Code comments

- Default to writing no comments. Only add one when the WHY is non-obvious — a hidden constraint, a subtle invariant, a workaround for a specific bug, or behavior that would surprise a reader.
- Don't explain WHAT the code does — well-named identifiers already do that.
- **Don't reference tickets, PRs, bug numbers, or diff history** in code comments (e.g. "fixes SKY-123", "Bugbot caught this on PR #584", "used to read X, now…"). Those belong in the PR description and rot as the codebase evolves. The WHY of the change should stand on its own.
- This applies to comments written by any tool (Cursor, Bugbot, Copilot) as well as humans — strip ticket/PR references before merging.

## Release and publishing authorization

- Never create, move, or delete Git tags or GitHub Releases; dispatch release or publish workflows; or publish binaries, container images, npm packages, package-manager artifacts, Helm charts, or other distribution artifacts without explicit user approval naming the exact artifact, version, and action.
- Approval to implement a change, open or merge a PR, or prepare release changes is not authorization to publish. If release authorization is ambiguous, stop and ask.

## Reference Docs — MUST READ before making changes

Not everything is in this file. The following files contain critical details that are **not duplicated here** — this file keeps the cross-cutting rules; per-area depth (gating rationale, lifecycle rules, scope semantics) lives in the files below. You MUST read the matching row's files **before editing** in that area — do not guess or rely on memory. If your area has no row, the handler's or package's own doc comments are the spec; read them first.

| When you are... | Read this file FIRST |
|-----------------|---------------------|
| Adding or modifying **HTTP endpoints** | `internal/server/server.go` — all routes are defined here — **plus** the handler's doc comments (why the route is gated the way it is lives there; copy the gate of the closest sibling only after reading it) and the integration's section in [docs/integrations.md](docs/integrations.md) |
| Adding or modifying **CLI flags** | `cmd/explorer/main.go` — flag definitions and defaults |
| Adding a **new CRD integration** (renderer, topology, discovery) | [docs/INTEGRATION_GUIDE.md](docs/INTEGRATION_GUIDE.md) — full checklist with collision gotchas |
| Working on the **CloudNativePG workspace** (`/cnpg`) | [docs/cnpg.md](docs/cnpg.md) — destinations, navigation (drawer trail, return label, `ctx` guard) and the certainty table: which source each fact comes from and what it reads when unknown. Data from `/api/cnpg/workspace` (per-kind coverage); derivations in `packages/k8s-ui/src/components/cnpg/workspace.ts` + `relations.ts`; screens in `web/src/components/cnpg/` |
| Working on **local per-cluster integration settings** (Metrics, Argo CD, Cost in `~/.radar/clusters.json`) | [docs/configuration.md](docs/configuration.md#local-integration-connections) — store `internal/config/profiles.go`, resolve/update `internal/connections`, activation `internal/connectionruntime`, routes `GET/PUT /api/integrations/connections`. In local mode the older `PUT /api/integrations/{prometheus,argocd,cost}` return 409 |
| Working on **GitOps** (Argo CD / Flux detail pages, operations, Terminating lifecycle, drift, per-resource health, remote destinations) | [docs/gitops.md](docs/gitops.md) — detail-page tabs, operation semantics, the Terminating severity ramp, nested navigation, single-cluster scope. Engine in `pkg/gitops/`, handlers `internal/server/gitops_handlers.go` |
| Working on an **integration's reverse-lookup or actions** (Velero, CloudNativePG, Kyverno, Argo Rollouts, …) | That integration's section in [docs/integrations.md](docs/integrations.md) + the doc comments in `internal/server/<name>_handlers.go` — both carry the per-integration gating and scope rules this file only summarizes |
| Working on **RBAC visibility** (Permissions / blast radius, SA/Role/Binding reverse lookups) | `pkg/rbac/index.go` (index, implicit groups, `MaxFlatRules`), `internal/server/rbac_handlers.go`, and the header of `packages/k8s-ui/src/utils/rbac-blast-radius.ts` — the rule that resource-only wildcards must NOT trigger is load-bearing |
| Working on **SSE / live updates** or anything the **topology** emits to a browser | `Server.handleSSE` in `internal/server/server.go` (per-user filtering), `internal/server/sse.go` (per-client RBAC grouping, `clientCanSeeChange`) + `pkg/topology/cluster_scoped_kinds.go` |
| Working on the **informer cache** (new typed kind, field stripping, scope) | `pkg/k8score/cache.go` + `pkg/k8score/transform.go` (what is stripped and why), `internal/k8s/capabilities.go` (probe-based scope) |
| Working on the **timeline / workload history** | `pkg/timeline/store.go` (`ResourceScope`: owner UID, `OwnerlessRefs`) + the `handleWorkloadHistory` doc comment in `internal/server/workload_history.go` |
| Working on **network trace / reachability** | [docs/reachability.md](docs/reachability.md) + `internal/server/trace_handlers.go` |
| Working on **authentication, auth-enabled mode, impersonation or per-user authorization** | [docs/authentication.md](docs/authentication.md) — auth modes, how per-user RBAC is enforced, ServiceAccount RBAC, session cookies. For Radar Cloud's default permission tiers and the integration-read policy, [docs/cloud-rbac-baseline.md](docs/cloud-rbac-baseline.md) |
| Changing the **Helm chart** or **in-cluster deployment** (chart values, chart RBAC, ingress) | [deploy/helm/radar/README.md](deploy/helm/radar/README.md) — the canonical chart source (the `skyhook-io/helm-charts` copy is overwritten on release, never edit it there); [docs/in-cluster.md](docs/in-cluster.md) for deployment paths |
| Working on the **Helm releases view** (release list, revisions, rollback, operation insight) | [docs/helm.md](docs/helm.md) |
| Working on the **Metrics tab** (workload HTTP rate/errors/latency, CPU/memory/throttling) | [docs/workload-metrics.md](docs/workload-metrics.md) — metric sources, attribution, per-run-mode overrides; test with `make workload-metrics-demo` |
| Working on **application grouping** ("these workloads are one app", cross-cluster app identity) | [docs/app-grouping.md](docs/app-grouping.md) — the model, then the code files it names |
| Touching **usage stats / telemetry** | [docs/usage-stats.md](docs/usage-stats.md) — it is a public promise of what is and isn't collected; a change that breaks it breaks the promise |
| Working on **performance at high object counts** | [docs/loadtest.md](docs/loadtest.md) — synthetic fake-cluster UI load testing |
| Working on the **Capacity (Karpenter) views** | [docs/capacity.md](docs/capacity.md) — the four screens, the per-value certainty contract (`= ≥ ≤ ?`, unavailable ≠ zero, partial ≠ exact, declared ≠ actual), demand-evaluation semantics, and the real Karpenter failure model. Wire types in `pkg/capacityapi`, engine in `internal/capacity`, handlers in `internal/server/capacity*` |
| Working on **Node status or removal diagnostics** | [docs/nodes.md](docs/nodes.md) — cross-surface contract; policy in `pkg/health/node_lifecycle.go`, raw-node UI mirror in `packages/k8s-ui/src/utils/node-lifecycle.ts`, shared vectors in `pkg/health/testdata/node_lifecycle.json` |
| Working on **resource renderers** | `packages/k8s-ui/src/components/resources/renderers/` — all existing renderers live here |
| Understanding **cluster connection behavior** or the **namespace picker** | [docs/configuration.md](docs/configuration.md) — kubeconfig precedence, multi-context, in-cluster, and the Namespace Picker section (persistence, kubectl-parity default, `--namespace-scope`) |
| Working on **MCP tools or AI context** | [docs/mcp.md](docs/mcp.md) + `internal/mcp/tools.go` — tool definitions and design rationale |
| Working on the **investigation verdict, story or Findings pane** | [docs/mcp.md](docs/mcp.md) ("The story contract") — the verdict JSON, `[[radar:evidence=N]]` placement grammar, run-scoped citation, and the assessment-turn rule. The contract (prompts, verdict, parser, binder) lives in `pkg/investigation`, **shared with Radar Hub**; OSS orchestration and ref eligibility stay in `internal/ai/runs.go`; frontend in `web/src/components/diagnose/` (`investigationStory.ts`, `AnalysisStory.tsx`, `investigationState.ts`) |
| Writing or modifying **frontend UI / styling** | [DESIGN.md](DESIGN.md) — theme tokens, do's/don'ts, component patterns |
| Syncing k8s-ui to **Claude Design** (`/design-sync`) | [.design-sync/NOTES.md](.design-sync/NOTES.md) + [.design-sync/conventions.md](.design-sync/conventions.md) (the usage guide shipped to Design) — build pipeline (`build-pkg.mjs`), config, re-sync command, known warns and size budget. `.design-sync/previews/*.tsx` are the authored preview cards; update them when a previewed component's props change |
| Touching anything library consumers import | `web/package.json` + `web/src/index.ts` (and the consumer-facing `web/README.md` / `packages/k8s-ui/README.md` — e.g. the k8s-ui YAML editors must stay CDN-free for air-gapped installs) — `web/` IS the `@skyhook-io/radar-app` npm package. Public surface: `RadarApp`, runtime-config setters (`setApiBase` etc.), `NavCustomization`. Breaking it breaks all downstream consumers. |
| Adding or changing **api/fetch call sites** | `web/src/api/config.ts` — all fetches go through `getApiBase()`, `apiUrl()`, `getWsUrl()`, `getAuthHeaders()`, `getCredentialsMode()`. New fetch sites must use these helpers so library consumers (Radar Hub) can override per-cluster. |
| Embedding Radar inside another app | `web/src/RadarApp.tsx` + `web/src/context/NavCustomization.tsx` — `apiBase`, `basename`, `router`, `navSlots` props. Check Radar Hub call sites when changing this interface. |

## Library distribution

Radar's frontend is also published as **`@skyhook-io/radar-app`** (source-only npm package, same model as `@skyhook-io/k8s-ui`). `web/` IS the package — `web/src/index.ts` is the library entry, and Radar's own binary entry (`web/src/main.tsx`) consumes the same source. Consumers get `<RadarApp apiBase basename router navSlots queryClient />`, the runtime-config setters (`setApiBase`, `setBasename`, `setAuthHeadersProvider`, `setCredentialsMode`) for non-React code paths, and the `NavCustomization` type. Published via tag `radar-app-v<semver>` (`.github/workflows/publish-radar-app.yml`) — only with explicit authorization, per the rule above.

Known consumers: Radar Hub (`skyhook-dev/radar-hub-web`).

`pkg/` is a separate Go module (`github.com/skyhook-io/radar/pkg`) that Radar Hub imports (`issuesapi`, `investigation`, `k8score`, `checks`, `auth`, …): keep it free of `internal/` imports and coordinate exported-API changes with Hub, same as the npm packages.

**Consumer scope:** Radar OSS and Radar Hub are the supported consumers. Coordinate interface changes with Hub; do not preserve unused modes or add migration machinery for hypothetical consumers. `navSlots.embedded` hides all Radar chrome; Hub owns its sidebar and top bar.

## Architecture

A single Go binary talks directly to the Kubernetes API through kubeconfig (`~/.kube/config`) or the in-cluster ServiceAccount — there is no Radar backend service in between. The same process serves the embedded React UI (HTTP/SSE/WebSocket) and AI tools over MCP (`/mcp`).

## Project Structure

See [docs/STRUCTURE.md](docs/STRUCTURE.md) for the full directory map + tech-stack snapshot. The load-bearing concerns (caching, topology, MCP, error handling, renderers) have their own sections below — the directory tree exists to orient, not to drive behavior.

## Development Commands

### CRITICAL: Frontend Embedding Pipeline

The Go binary serves the frontend via `go:embed` from `internal/static/dist/`, NOT from `web/dist/`. The build pipeline is:

```
web/src → (npm run build) → web/dist → (make embed) → internal/static/dist → (go build) → binary
```

**ALWAYS use `make build` to build the full application.** Running `cd web && npm run build` followed by `go build` will NOT update the served frontend — the embed step (`make embed`) that copies `web/dist/*` to `internal/static/dist/` will be skipped, and the binary will serve stale frontend assets.

### Build / test / run

`make help` lists every target — read the Makefile when uncertain. The day-to-day set: `make build` (frontend + embed + binary), `make restart` (full rebuild + restart) / `make restart-fe` (frontend + embed + restart), `make watch-backend` / `make watch-frontend` (Air :9280 + Vite :9273), `make test`, `make tsc`. `go run ./cmd/explorer --dev` serves the frontend from `web/dist` without the embed step.

### Visual Testing
```bash
./scripts/visual-test-start.sh          # Build + launch on random port (9300-9399)
./scripts/visual-test-start.sh --skip-build  # Relaunch without rebuilding
source .playwright-mcp/visual-test-state.env # Load $RADAR_URL, $SCREENSHOT_DIR, etc.
./scripts/visual-test-stop.sh           # Kill process, open screenshot folder
```
Use `/visual-test` command for the full workflow (cluster check, Playwright MCP, screenshots, report). Screenshots go under `.playwright-mcp/visual-test/`.

### Demo clusters (scripted test fixtures)

Scripted `kind` clusters under `scripts/*-demo.sh` reproduce the states each integration needs — states that are hard or impossible to conjure by hand (frozen controllers holding all phases at once, configurations that fail in ways that look like success, connection lanes toggled on demand).

**Before using one, read its `scripts/<name>-demo/README.md` — this is not optional.** Each README is the only complete account of what the scenarios cover, which modes are NOT interchangeable, and why the cluster is shaped the way it is; the shape encodes hard-won constraints that look like bugs if you don't know them. Don't improvise against the fixtures or "fix" what looks broken before reading it.

After `make <name>-demo`, run `kubectl config use-context kind-radar-<name>-demo` before `./scripts/visual-test-start.sh` — otherwise you're testing against whatever cluster the current context points at (often a customer cluster lacking the fixture variety).

| Demo | Target | Use when working on |
|------|--------|---------------------|
| GitOps | `make gitops-demo` | Argo CD / Flux UI. `-drift` induces live OutOfSync |
| Kyverno | `make kyverno-demo` | Policy renderers, report-family selection, admission attribution. Scenarios `openreports` / `modern-only` |
| Velero | `make velero-demo` | Backup/restore surfaces — all 13 Backup phases at once. `-live` for states the controller actually produced |
| CloudNativePG | `make cnpg-demo` | CNPG renderers/badges. `-live` for real failovers; fixtures have strict ordering constraints |
| Beyla | `make beyla-demo` | `internal/traffic/beyla.go` — which labels exist depends on Beyla config, not code. Modes `attrs` / `no-network` |
| Cilium | `make cilium-demo` | `internal/traffic/hubble.go` — every Hubble connection lane. Modes `tls` / `netpol` / `install-radar` |
| Kubecost | `make kubecost-demo` | Kubecost 3 current costs — real allocation/assets, local port-forward and in-cluster Service DNS. Modes `query` / `install-radar` / `radar-smoke` |
| Calico | `make calico-demo` | Calico surfaces — both API groups, staged policies, tiers |
| Crossplane | `make crossplane-demo` | Crossplane renderers and spec-shape dispatch |
| Rollouts | `make rollouts-demo` | Argo Rollouts progression. `-roll` advances a rollout |
| GPU ecosystem | `make gpu-ecosystem-demo` | All 37 curated GPU, batch, distributed-training, and inference resource identities. `install-radar` verifies default chart RBAC and group-aware discovery |
| Kueue admission | `make kueue-demo` | Real Kueue reconciliation: admitted/running, quota-blocked with no Pod, and held-queue with no Pod |
| JobSet | `make jobset-demo` | Real JobSet reconciliation: role/index Job-to-Pod lineage, dependency gating, and explicit terminal failure |
| KubeRay | `make kuberay-demo` | Real RayService reconciliation: healthy active Serve revision plus an intentionally failed pending NewCluster revision |
| Workload metrics | `make workload-metrics-demo` | Metrics tab (HTTP rate/errors/latency + CPU/memory/throttling) across Beyla, cAdvisor/KSM and Istio sources. Builds on the Beyla demo; uses its own kubeconfig and the `radar-workload-metrics-demo` context rather than switching yours |

`scripts/rbac-demo.sh` is the odd one out: it seeds RBAC scenarios into the *current* context (no cluster of its own).

**Before calling a UI feature done — consider visual-test.** `make tsc` + `make test` check code; `/visual-test` checks the *screen*. Run it yourself for self-contained UI changes where you can predict what should render; ask the user when the change is broad or the right capture set isn't obvious; skip for pure refactors / Go-only / type-only / doc edits.

### Dev ports
**9280** Go backend · **9273** Vite (proxies `/api` → 9280).

## API Endpoints & CLI Flags

**You MUST read `internal/server/server.go` before adding or modifying any endpoint** — it is the single source of truth for all routes. CLI flags live in `cmd/explorer/main.go`. Key URL patterns — one line per family, plus the gate wherever it differs from the default (a new sibling route should copy the gate, not guess it):
- REST resources: `/api/resources/{kind}`, `/api/resources/{kind}/{ns}/{name}`, `/api/resources/apply` (POST), `/api/resources/preview` (POST, server-dry-run review), `/api/resources/schemas` (POST, connected-cluster OpenAPI schemas)
  - `/api/resources/{kind}` returns a **bare array**; `?table=1` switches to the printer-column envelope `{items, kind, group, columns, cells}`, returned for every **200** with null `columns`/`cells` when there is no table (errors stay `{"error"}`). Don't combine with `?include=summary` — the strip runs first and mutates in place, so a column reading a stripped subtree resolves to null.
- SSE streaming: `/api/events/stream`, `/api/traffic/flows/stream`
- WebSocket: `/api/pods/{ns}/{name}/exec`
- MCP: `/mcp` (Streamable HTTP — POST for JSON-RPC, GET for SSE)
- Helm: `/api/helm/releases/...`
- Workloads: `/api/workloads/{kind}/{ns}/{name}/...` (logs, restart, scale, revisions, rollback, images, history). `revisions`/`rollback` accept Deployment, StatefulSet, DaemonSet and **Rollout**, gated by `rollbackableWorkloadKinds` in `server.go`; `images` accepts the same four through a separate map, `workloadImageRoots` in `pkg/k8score/workload_images.go` (a new kind needs both), uses compare-and-swap JSON Patch, and follows a Rollout's `workloadRef`. `history` is the workload's own timeline and never includes sibling workloads — scope rules live in `workload_history.go` and `pkg/timeline`'s `ResourceScope`
- Argo Rollouts: `/api/rollouts/{ns}/{name}/{abort,retry,promote,promote-full,skip-step}` (POST) + `/capabilities` (GET); rollback/history deliberately live on the `/workloads` routes. The status verbs patch the `rollouts/status` subresource, so capabilities SAR `rollouts` **and** `rollouts/status` separately — `patch rollouts` does not imply `patch rollouts/status`. Promotion waits for the controller to observe the current pod template, so `promote-full` can return **503** (`ErrControllerNotCaughtUp`) and is safe to retry; for a `workloadRef` Rollout the caller must be able to `get` the referenced workload. Engine `pkg/rollouts`, handlers `internal/server/rollouts_handlers.go`
- GitOps controller actions: `/api/argo/applications/...` (sync, refresh, terminate, suspend, resume, rollback, selective-sync), `/api/flux/{kind}/...` (reconcile, suspend, resume, sync-with-source)
- Argo CD API integration: `PUT /api/integrations/argocd` (non-local config only — local mode uses `/api/integrations/connections`; URL/token, probe-before-persist, token preserved across GET-redaction round-trips); `/api/argo/applications/{ns}/{name}/resource-diff` (Git-rendered desired vs live via argocd-server managed-resources; dual RBAC gate + structural Secret redaction; see docs/gitops.md)
- GitOps detail data: `/api/gitops/{tree,insights,destination}/{kind}/{ns}/{name}`. `destination` says where a remote Application or `spec.kubeConfig` Flux object deploys; the object and any kubeconfig Secret are read as the caller, and full server URLs never leave the server
- Nodes: `/api/nodes/{name}/...` (cordon, uncordon, drain, drain-plan (read-only estimate: per-pod evict/skip/may-block with reasons), debug)
- Audit: `/api/audit`, `/api/audit/resource/{kind}/{ns}/{name}`, `/api/settings/audit` (GET/PUT)
- Network trace: `/api/trace/{kind}/{ns}/{name}` (Service/Ingress/HTTPRoute/GRPCRoute/Gateway). `?probe=true` runs DNS/TCP/TLS/HTTP probes — direct in-cluster, via the API server proxy from a laptop — gated by the user's `services/proxy` + `pods/proxy` RBAC
- Capacity (Karpenter): `/api/capacity` (+ `/pools`, `/pools/{name}`, `/pools/{name}/members`, `/demand`, `/activity`) — read-only, gated on the caller's ability to list NodePools, deliberately cluster-wide (no namespace view-filter forwarding). See [docs/capacity.md](docs/capacity.md)
- Upgrade impact: `/api/upgrade-readiness?target={major.minor}` (`?refresh=true` bypasses the scan memo) — cluster-wide (ignores the namespace picker), bounded by the caller's RBAC and the cache scope. Engine `pkg/upgradereadiness`; the service layer in `internal/upgrade` is shared by the HTTP handler and the `get_cluster_upgrade_readiness` MCP tool
- CAPI: `/api/capi/clusters/{ns}/{name}/kubeconfig` (GET), `/api/capi/clusters/{ns}/{name}/connect` (POST)
- Prometheus: the curated chart routes (`/api/prometheus/{resources,namespace,hpa,pvc,rightsizing}/...`) gate (auth-enabled mode) on reading the resource they chart, through the `AuthGate` seam in `internal/prometheus/auth.go`. The unbounded surfaces — `/api/prometheus/cluster`, `/api/prometheus/query`, and the MCP `query_prometheus` / `discover_metrics` / `get_prometheus_rules` tools — gate on an any-namespace `list pods` SubjectAccessReview. The raw query route applies the same response bounding as the MCP tool. No-auth local mode is a passthrough
- Cloud Connect driver lane: `/api/cloud/install/{prepare,start,status,cancel,dismiss}` — the in-product device-flow install. Enabled ONLY local + auth-disabled + no `--cloud-url` (deliberately NOT gated on a loopback listener; a shared listener instead requires an explicit acknowledgement); every other configuration routes to the Hub wizard. The cluster token never serializes through these endpoints
- RBAC reverse-lookup: `/api/rbac/subject/{kind}/{namespace}/{name}` (ServiceAccount, plus `usedByPods`) and `/api/rbac/subject/{kind}/{name}` (User/Group) — direct + group-inherited bindings and flattened effective rules; `/api/rbac/role/{kind}/{namespace}/{name}` (`_` for a ClusterRole's namespace) — the bindings that reference it; `/api/rbac/namespace/{namespace}` — backs the Namespace RBAC section (group-only ClusterRoleBindings deliberately excluded); `/api/rbac/whoami` — `SelfSubjectRulesReview` pass-through. All gate on `list rolebindings` AND `list clusterrolebindings`: **403 when either is denied, never a silent partial view**
- Policy (Kyverno): `/api/policy/resource/{kind}/{ns}/{name}` (one resource's findings), `/api/policy/policies/{policy}` (every resource one policy recorded an outcome for). Report families are authorized **per subject scope** (`policyreports` cluster-wide ≠ `clusterpolicyreports`); findings from an unreadable family are dropped from lists AND counts, with the withheld count reported; `counts` describe the cluster while subject lists are capped and view-filtered. `/api/policy/policies/{policy}/queued` reads Kyverno's `UpdateRequest`s cluster-wide, gated on `list updaterequests`
- Velero: `/api/velero/backupstoragelocations/{ns}/{name}/backups` (what a location holds; gated on `list backups`); `POST /api/velero/{backups|restores}/{ns}/{name}/messages` (a run's warnings/errors via a `DownloadRequest` — impersonated; needs a running Velero controller and object storage reachable from Radar, and reports which one failed)
- CloudNativePG: `/api/cnpg/...` — the workspace, operator, catalog reverse-lookups, Cluster logs and activity. Routes, gates and coverage states are listed in [docs/cnpg.md](docs/cnpg.md#api); each is gated on the caller's own access, and a partial answer says what it withheld

## Key Patterns

### Resource identity

`pkg/resourceid` is the one model of resource identity: `Ref` (group + Kind + namespace + name; version is not identity), `Reference` (a possibly-partial reference that records where its group came from, so the core group `""` is never confused with "not recorded"), the `Builtins` table, `GroupFromAPIVersion` / `NormalizeGroup`, and the two resolution policies — `ResolveCurrent` (live joins and user requests; may infer a built-in or uniquely-served group) and `ResolveHistorical` (stored observations; never infers). Use it instead of parsing apiVersions or defaulting groups locally. Controller-recorded groups (Argo CD `status.resources`, Flux inventory) are exact: an omitted group there is core, not unknown.

### K8s Caching
- Core informer logic lives in `pkg/k8score` (no internal/ imports — reused outside Radar); `internal/k8s/cache.go` wraps it as a singleton and injects Radar behavior (timeline recording, noise filtering, diffs) through `CacheConfig` callbacks. Critical informers block startup, deferred ones (events, secrets, configmaps…) sync in the background, CRDs cache dynamically via discovery, and managed fields / last-applied annotations are stripped. GitOps drift needs last-applied, so it reads through `GetDirectPreserveLastApplied` (a direct GET) — never make the cache retain it (memory, and it would leak a full JSON copy into every payload); stripping rules live in `pkg/k8score/transform.go`
- **Per-kind scope decisions** via `CacheConfig.ResourceScopes` — each kind can independently be cluster-wide, namespaced, or disabled based on what the SA can list. `pkg/k8score/cache.go`'s `pickFactory` routes each informer to the matching factory; cluster-only kinds (Nodes, Namespaces, PVs, StorageClasses, IngressClasses) always use the cluster-wide factory regardless of caller intent
- **Probe-based RBAC gating** (`internal/k8s/capabilities.go`): at startup, Radar runs a real list call against each typed kind (using the SA / kubeconfig identity) to decide if it goes cluster-wide, namespace-scoped, or off. List probes are authoritative because they ARE the operation the informer will perform — SSAR is one indirection too many and can disagree with reality on clusters using webhook authorizers (e.g. GKE IAM). When cluster-wide list is denied, the probe falls back to a capped set of candidate namespaces (`buildScopeCandidates`)
- **In-app namespace switcher = per-user view filter**: the header's `NamespaceSwitcher` POSTs to `/api/cluster/namespace`, which the server stores as a per-user preference in `Server.nsPreferences` (key: `username\x00contextName`). It does NOT mutate the shared cache — except under `--namespace-scope`, where the picker sets the process-wide cache scope (local mode rebuilds the cache; auth/cloud mode locks it to the startup namespace; see docs/configuration.md). The pick is intersected with the user's RBAC-allowed namespaces on every read in `parseNamespacesForUser` (REST) and `filterNamespacesForUser` (MCP). Locally the pick persists per context in `settings.ActiveNamespaces`; with no saved entry it defaults to the kubeconfig context's namespace (kubectl parity), and an explicit "All namespaces" persists as an empty entry that suppresses that default. On context switch, all users' picks are dropped — they reference the previous cluster's namespaces
- **Per-user RBAC filtering** (auth enabled): namespaced reads filter via `parseNamespacesForUser` → `getUserNamespaces` → `auth.DiscoverNamespaces` (SubjectAccessReview-based, "list pods" / "list deployments" sentinel). Cluster-scoped reads gated per-kind via `Server.canRead` / MCP `canReadClusterScopedKind` — both run a SAR for the exact (group, resource, verb) and cache on `UserPermissions.canI`. Cluster-wide pod visibility does NOT imply cluster-scoped reads; this is the load-bearing security distinction. Static cluster-only kinds map via `k8s.ClusterOnlyKindGVR`; dynamic CRDs use discovery's `GetResourceWithGroup`. MCP write tools / exec / logs impersonate via `DynamicClientFromContext`, so the apiserver enforces full RBAC there directly
- **SSE is the third read surface**, alongside REST and MCP. `Server.handleSSE` (`server.go`) does the per-user work — intersects the requested namespaces with RBAC (deliberately NOT the saved picker) and rewrites the query, resolves denied cluster-scoped kinds, builds the change-frame authorizer — and the broadcaster's `HandleSSE` (`sse.go`) trusts it, so never subscribe a client any other way. Topology frames are built per group of clients sharing the same filtered namespaces + denied kinds, then regrouped by the per-node tuples each client may see; change frames are SAR-gated per (group, resource, namespace) in `clientCanSeeChange`, and an unresolved kind fails closed. A new cluster-scoped topology kind with a fixed (group, resource) must be registered in `topology.ClusterScopedKinds` (`pkg/topology/cluster_scoped_kinds.go`) or it leaks on all three surfaces; a kind whose (group, resource) varies per node (NodeClass providers, Calico's two API groups, cluster-scoped Crossplane) is instead gated per node through the `Strip*Except` helpers, on all three surfaces

### Topology Builder
- Constructs directed graph from K8s resources via owner references + selector matching
- Two view modes: `traffic` (network flow: Ingress/Gateway → HTTPRoute → Service → Pod) and `resources` (hierarchy: Deployment → ReplicaSet → Pod)
- **Edge type semantics** (drive UI grouping): `EdgeManages` (owner), `EdgeUses` (HPA/VPA/KEDA), `EdgeProtects` (PDB/NetworkPolicy), `EdgeConfigures` (ConfigMap/Secret/DestinationRule), `EdgeExposes` (Service/Ingress/Gateway). Choose the right type — don't reuse.
- **CRD collision pattern**: When a CRD kind collides with core K8s (e.g., Knative Service, CAPI Cluster), use `GetGVRWithGroup("Kind", "group")` and prefix node IDs (`knativeservice/`, `capicluster/`). Frontend disambiguates via `data?.apiVersion?.includes('group.name')`.
- Supported topology integrations are tracked by the **Topology** column in `docs/integrations.md`; keep that table and the implementation in sync.
- GitOps nodes: Application (Argo CD), Kustomization, HelmRelease, GitRepository (Flux). Detail-page behavior — tabs, operations, the Terminating lifecycle, nested navigation, single-cluster limits — is in [docs/gitops.md](docs/gitops.md). The rules that bite:
  - Every mutating GitOps operation runs the `assertNotTerminating` pre-flight; sentinel errors (`ErrOperationInProgress`, `ErrResourceTerminating`) map to HTTP status via `errors.Is`.
  - **Severity vocabulary**: `critical` (0, red) → `alert` (1, orange) → `warning` (2, amber) → `info` (3, blue). A new severity must update both Go `severityRank` and the TS union in `gitops-insights.ts`.
  - **Per-resource health**: read a tree node's resolved `health` + `healthSource` (`controller` | `controllerApi` | `radar`), never `status.resources[].health` directly — Argo CD 3 doesn't persist it, and `overlayRadarHealth` (`internal/server/gitops_handlers.go`) fills the gap from the issues engine. See [docs/gitops.md](docs/gitops.md#per-resource-health)
  - **Per-resource drift** comes from the `kubectl.kubernetes.io/last-applied-configuration` annotation, so SSA/Helm-installed resources have none.

### Timeline + resource relationships

Timeline (`pkg/timeline/`): in-memory or SQLite (`--timeline-storage`), default 10k-event ring. Lane grouping (owner, app, topology) happens in the frontend (`packages/k8s-ui/src/utils/resource-hierarchy.ts`); the stores only filter and page. Resource relationships (`pkg/topology/relationships.go`): computed at query time — parent/children/deployment-grandparent/config/network/scalers/policies/storage — used for both detail views and topology edges.

### RBAC Visibility

`pkg/rbac/` is a pure index over typed `rbacv1` listers (no K8s API calls, no internal/ imports) behind a 5s memo; `finalizePostContextSwitch` invalidates it so a context switch never serves the previous cluster's RBAC. Renderers (`ServiceAccountRenderer`, `RoleRenderer`, `RoleBindingRenderer`, `PodRenderer`, `WorkloadRenderer`, `NamespaceRenderer`) take optional `rbacData` / `rbacRoleData` / `roleRules` props and render the reverse-lookup sections only when the host wires the fetch (`useRBACSubject` / `useRBACRole` / `useRBACNamespace` in `web/`) — Radar Hub can skip the fetch and nothing breaks. Blast-radius detection (Pod/Workload **Permissions**) lives only in `packages/k8s-ui/src/utils/rbac-blast-radius.ts` so the renderers can't drift; RBAC badge classes live in `utils/rbac-badges.ts` (hand-rolled `bg-*-500/20` strings wash out in light mode).

### AI Context Minification

`pkg/ai/context/` collapses K8s resources for LLM consumption at three verbosity levels (`Summary` for `list_resources`, `Detail` for `get_resource`, `Compact`). Secret safety is structural: it never emits `.data`/`.stringData` and redacts env values matching API-key/token/password/base64 patterns — keep it that way for any new resource shape.

### MCP Server

Stateless HTTP at `/mcp` (JSON-RPC). Read tools use `readOnlyHint`, write tools use `destructiveHint: true`. Respects cluster RBAC (impersonates via `DynamicClientFromContext` for write/exec/logs). Enabled by default; `--no-mcp` to disable. Tool catalogue + design rationale lives in `internal/mcp/tools.go` + [docs/mcp.md](docs/mcp.md) — don't restate it here. **When adding/removing a tool in `registerTools`, also update the user-facing setup dialog catalog `web/src/components/home/mcpToolCatalog.ts`** — `TestSetupDialogCoversAllTools` fails CI if the two diverge. A **read** tool additionally needs adding to `investigation.ReadOnlyTools` in `pkg/investigation/prompt.go` — the allowlist Radar's own Diagnose agent (and Radar Hub's) calls through; a tool missing there reaches every external client but not the product's own agent (`TestDiagnoserAllowlistCoversAllReadTools` fails CI). A **write** tool instead needs adding to both write-tool lists in `internal/mcp/tools_catalog_test.go` (`writeTools` in `TestRegisteredToolAnnotations` and `writeToolNames`) — the second is what keeps it out of the read-only mount, and it belongs in `investigation.WriteTools`, which gates it to confirmed apply turns (not CI-enforced — a write tool missing there is silently unavailable to the Diagnose agent). New tools also consume the `maxCatalogBytes` description budget; raise it deliberately rather than gutting routing guidance.

### Error Handling (Backend)

Handlers emit `{"error": "..."}` via `s.writeError(w, status, msg)`. Status conventions:
- **400** invalid input (missing params, bad YAML, unknown kind)
- **403** RBAC denied (nil lister or apiserver Forbidden)
- **404** resource doesn't exist — check via `apierrors.IsNotFound(err)`
- **409** operation already in progress (sync running, etc.)
- **413** request body over the route's cap — the body is bounded *before* it is read (`readBoundedTextBody` for raw YAML, `decodeBoundedJSONBody` for JSON), so nothing has parsed it yet and 400 would wrongly blame the content. Reserve 400 for input that was read and found invalid — including caps counted after parsing, like the YAML document limit
- **503** cache/connection not ready — most cluster-touching handlers call `s.requireConnected(w)` at the top
- **500** unexpected — always `log.Printf("[module] Failed to <action> %s/%s: %v", ns, name, err)` before returning

Namespace filters accept both `?namespace=X` (single) and `?namespaces=X,Y` (preferred). Use `parseNamespaces()` to handle both.

### Version skew (newer UI, older Radar)

Radar Hub embeds the newest `@skyhook-io/radar-app` against whatever Radar each cluster runs, so a frontend call to a new endpoint can hit a Radar that predates it. When adding an endpoint the UI calls: advertise a flag for it in `FeatureCapabilities` (`internal/k8s/capabilities.go`) in the same PR, add an entry with that flag and `flagShippedWithEndpoint: true` to `web/src/api/radarFeatures.ts`, and guard the hook with `useRadarFeature`. An older Radar then gets a "needs a newer Radar" note instead of a red error. `TestFeatureFlagsHaveFrontendGates` fails if the flag and the entry drift apart.

### Error Handling (Frontend)

React Query mutations carry `meta: { errorMessage, successMessage }` — the global toast handler reads those. Server errors arrive as `{"error": "..."}` and surface unchanged. Don't add per-mutation `onError` toasts that would duplicate the meta-driven path.

### Shared UI Package (@skyhook-io/k8s-ui)

`packages/k8s-ui/` is the shared presentation layer — components are pure, data hooks live in `web/` and inject via props/callbacks. `web/src/components/resources/ResourcesView.tsx` is the canonical wrapper pattern. Linked via npm workspaces; Vite source-aliases `@skyhook-io/k8s-ui` → `../packages/k8s-ui/src` (no build step). Key exports: `ResourcesView`, `ResourceRendererDispatch`, `ResourceActionsBar`, `EditableYamlView`, renderers, resource-utils, `categorizeResources`, `getKindLabel`, `getKindPlural`.

**Badges + status tones.** `components/ui/Badge.tsx` owns the canonical color strings as literal class names (Tailwind's scanner can't see template literals); `utils/badge-colors.ts` re-exports and derives the per-domain maps. One health vocabulary everywhere — `HealthLevel` = `healthy | degraded | alert | unhealthy | neutral | unknown`, shared by `resource-utils.ts`, the `.status-*` CSS classes and `components/ui/status-tone.tsx` (`StatusDot`, `mapHealthToTone`); normalize raw API strings via `mapHealthToTone`, never add a parallel vocabulary. `alert` (orange) sits between `degraded` (amber) and `unhealthy` (red) for 3-step severity gradients.

Centralized `@layer components` classes in `theme/components.css` (Tailwind utilities can override): `.badge` / `.badge-sm`, `.btn-brand*`, `.card-inner` / `.card-inner-lg`, `.selection*`, `.dialog`.

### Frontend Styling Rules
**Use theme tokens — never hardcode colors.** See [DESIGN.md](DESIGN.md) for the full reference. Quick rules:
- Backgrounds: `bg-theme-base/surface/elevated/hover` — not `bg-white`, `bg-gray-*`, `bg-slate-*`
- Text: `text-theme-text-primary/secondary/tertiary` — not `text-gray-*`
- Borders: `border-theme-border` — not `border-gray-*`
- Buttons: `.btn-brand` — not hand-rolled `bg-blue-*`
- Badges: `<Badge severity="...">` or `<Badge kind="...">` — never hand-write color strings
- Shadows: `shadow-theme-sm/md/lg` — not raw Tailwind shadows
- Motion: `<Collapse>` / `<CollapseChevron>` / `useDisclosure` for anything that expands in place; `useAnimatedUnmount(open, overlayExitMs(kind))` + `overlayTransitionStyle` for menus, dialogs, sheets. All timing comes from `packages/k8s-ui/src/utils/animation.ts` — never an inline duration or curve, never `open && (...)` for a disclosure, no native `<details>`. See DESIGN.md §7 Motion.

### Printer columns (uncurated CRDs)

CRDs Radar hasn't curated fill their table from the kind's own
`spec.versions[].additionalPrinterColumns` — the columns `kubectl get` shows.
Engine in `internal/server/printer_columns.go`, frontend helpers in
`packages/k8s-ui/src/components/resources/printer-columns.ts`; both carry the
detail at the point of use. Three things that are easy to get wrong:

- **Exclusive, never merged.** A kind gets the curated set *or* the printer set, and curated always wins — several curated sets encode why the vendor-obvious field is the wrong one to show. `hasCuratedColumns` is the single definition of curated.
- **Never hand-roll the JSONPath.** Evaluation goes through `apiextensions-apiserver`'s `tableconvertor`: real CRDs use filter expressions, wildcards and escaped keys, and it also owns first-match semantics and type coercion.
- **Use the served version, not the storage version.** Printer columns are per-version and the API server converts objects into the version being listed.

### Resource Renderers

**Adding or modifying a CRD integration? Read [docs/INTEGRATION_GUIDE.md](docs/INTEGRATION_GUIDE.md) first** — full checklist with collision gotchas. Renderers live in `packages/k8s-ui/src/components/resources/renderers/` (100+ components, 20+ integrations); register in that folder's `index.ts` plus `packages/k8s-ui/src/components/shared/ResourceRendererDispatch.tsx` (KNOWN_KINDS, render line, `getResourceStatus()`). Use `AlertBanner` / `ProblemAlerts` / `ConditionsSection` for problem surfaces and `LabelSelectorDisplay` for selectors — never hand-roll. Sections default to `defaultExpanded={true}` unless empty/low-priority.

**Kind collision rule:** When a CRD kind shadows core (Knative Service vs core Service) or two CRDs share a kind (CNPG Cluster vs CAPI Cluster), guard THREE places in `ResourceRendererDispatch.tsx`: the renderer line, `getResourceStatus()`, and action buttons (Port Forward, etc.). Use `data?.apiVersion?.includes('group.name')`. Missing any one produces dual-render bugs.

**Crossplane renderers are spec-shape detected, not kind-enumerated.** Managed Resources / Composites / Claims have unbounded plurals (one CRD per provider service), so dispatch falls through to `isManagedResource` / `isComposite` / `isClaim` from `resource-utils-crossplane.ts`; the other Crossplane kinds are kind-dispatched. v1↔v2 path differences stay inside those resource-utils accessors.

## Tech stack + server config

Tech stack snapshot lives in [docs/STRUCTURE.md](docs/STRUCTURE.md#tech-stack-snapshot). `go.mod` and `web/package.json` are the source of truth. Server middleware (Logger, Recoverer, 60s timeout, CORS for `localhost:*` / `127.0.0.1:*`) and the Vite dev proxy (`/api` → `:9280`, `ws: true`) are configured inline in `internal/server/server.go` and `web/vite.config.ts` — read those when changing them.
