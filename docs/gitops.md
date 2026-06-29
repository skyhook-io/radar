# GitOps (Argo CD & Flux)

Radar's GitOps workspace gives Argo CD and Flux first-class treatment. Instead of treating Applications and Kustomizations as generic CRDs, you get a typed fleet view, a per-app detail page that diagnoses *why* something is misbehaving, and the controls you'd otherwise reach for `argocd` / `flux` CLI to run.

The hard part of GitOps tooling isn't sync — it's diagnosis. Radar surfaces drift, recent events, controller-failure attribution, and lifecycle state inline so you don't have to context-switch between `kubectl get`, `argocd app diff`, controller logs, and a YAML viewer to understand a stuck reconcile.

<p align="center">
  <img src="screenshots/gitops-view.png" alt="GitOps fleet view" width="800">
  <br><em>Fleet view — Argo + Flux applications side-by-side with sync, health, source, destination, and lifecycle state</em>
</p>

## Supported CRDs

| Tool | Kinds |
|------|-------|
| **FluxCD** | `GitRepository`, `OCIRepository`, `HelmRepository`, `Bucket`, `Kustomization`, `HelmRelease`, `Alert` |
| **ArgoCD** | `Application`, `ApplicationSet`, `AppProject` |

## Fleet view

Open the **GitOps** tab in the sidebar. Argo + Flux rows mix in the same table or tile view with resolved source URLs (`github.com/owner/repo`) for both ecosystems — not the CRD-internal source name.

- **Filters**: Sync, Health, Project, Namespace, Labels, Automation (auto-sync / manual / suspended), Lifecycle (active / terminating)
- **Modes**: Applications / Sources / Projects / Alerts
- **Default sort**: smart-tiered by urgency — Failed > Terminating > Degraded > Missing > OutOfSync > Suspended > Progressing > Synced

## Per-app detail page

Click any row to open a detail page with three top-level tabs.

<p align="center">
  <img src="screenshots/gitops-detail-drift.png" alt="GitOps detail page with stuck-drift-loop diagnosis" width="800">
  <br><em>Detail page — the diagnosis pipeline names the cause; field-level drift renders inline</em>
</p>

### Topology tab

Graph or table sub-modes. The graph shows the application root and every managed resource, with ownership edges (Service → Deployment → ReplicaSet → Pod chains). Filter by kind, sync, health, role, namespace, or search. Group large pod sets to keep the graph scannable; click a group to expand.

GitOps CRs that are themselves managed (a child Application from an app-of-apps parent, a Kustomization referenced by another Kustomization, ApplicationSet children) render with a small `Argo` or `Flux` tool badge and a `→` chevron — click to open that CR's own GitOps detail page. The child page's breadcrumb shows the lineage `GitOps / parent-ns/parent / child-ns/child`; the parent segment is clickable to navigate back.

### Changes tab

Per managed resource, in one row:

- **Sync status** chip (`Synced`, `OutOfSync`, etc.) with the health chip beside it
- **Field-level drift** computed inline from each resource's `kubectl.kubernetes.io/last-applied-configuration` annotation vs live cluster state. You see actual `spec.X removed / spec.Y added` entries — no `argocd app diff` round-trip needed. Live objects are pulled via a dedicated direct-GET path so the dynamic informer cache doesn't have to retain the annotation cluster-wide
- **Recent events** (5 most recent, namespace-RBAC-filtered) so `ImagePullBackOff`, `FailedScheduling`, webhook denials, PVC pending, etc. show up next to the resource that caused them
- **Open** button — drops into the standard resource drawer

OutOfSync alerts in the Issues band at the top of the page are clickable — they jump to the affected row and highlight it for ~4 seconds.

### Activity tab

Operation history with deploy timestamps, the git revision deployed, who initiated the sync (human user or automation), an outcome chip per row, and per-revision **Rollback** buttons for Argo. In-progress operations pin to the top of the timeline.

## Diagnosis pipeline

The Issues band at the top of the detail page surfaces six classes of problems:

- **Operation failures** (Argo) — the parser recognizes 11 patterns (annotation-too-large, label-too-long, hook failure, admission webhook denial, RBAC, conflict, immutable field, schema migration, connectivity, etc.) and rewrites each into a plain-English cause
- **Stuck-drift loop** — when sync succeeded but the app is *still* OutOfSync with auto-sync on and a recent reconcile, something is mutating resources after each apply. The Issue calls out likely culprits (mutating webhook, sibling controller, schema migration)
- **Manual drift without auto-sync** — drift exists but auto-sync is disabled. The Issue tells you "nothing will reconcile until you click Sync" so you stop waiting
- **Argo Application conditions** — `ComparisonError` (verify repo creds), `OrphanedResourceWarning`, `InvalidSpecError`, etc. extracted into typed-severity Issues
- **Per-resource health** — Degraded / Missing children get a critical Issue each, deduped against any operation failure that already named the same resource (no triplicate rendering)
- **Pending deletion** (lifecycle) — see [Lifecycle awareness](#lifecycle-awareness) below

**Structured remediation** — when the diagnosis pipeline recognizes a fixable failure (e.g. Argo operation error "namespace X not found"), the Issue carries a primary-blue action button that performs the fix in one click. Duplicate per-resource Missing issues + SyncError condition rows are then suppressed so the user sees one clear "create the namespace and retry" path instead of three.

While an operation is running, the page polls every 2s; otherwise on-demand.

## Lifecycle awareness

When a GitOps resource is being deleted (`metadata.deletionTimestamp` set), its Sync and Health values are leftovers from the last reconcile *before* deletion was triggered. Showing them as if they're current produces contradictory state ("Syncing · Progressing · Terminating") that misleads operators into trying to fix routine sync problems on a resource the cluster is actively tearing down.

Radar treats Terminating as a distinct lifecycle phase that dominates other status:

- **Detail header**: orange `[Terminating]` chip replaces Sync/Health badges. Source / Revision / Last reconcile / Sync mode metadata swaps to `Pending deletion · Finalizers`, with the original fields behind a "Show pre-deletion metadata" toggle
- **Action buttons**: Sync, Reconcile, Suspend / Resume, Rollback, Sync-with-source disable with a tooltip explaining why. Refresh and Terminate stay enabled — they're read-only / cleanup-only verbs
- **Lifecycle banner**: a dedicated orange banner above the Issues band; pre-deletion failures collapse behind a `Pre-deletion issues (N)` disclosure
- **Severity ramp**: info <5min, warning 5-30min, alert >30min. Past 30min the Issue's Cause line names the controller responsible for the finalizer and reports its pod state ("helm-controller is not running in flux-system")
- **Fleet view**: `—` in Sync/Health columns, orange row stripe, `[TERMINATING]` chip in the leftmost slot, `Pending Nago` instead of "Last Sync"
- **Topology**: orange left-stripe on the root + children; stale sync/health chips suppressed
- **Cluster Audit**: `stuckTerminating` check across all typed K8s resources with the same warning/alert thresholds
- **Mutating ops** (`Sync`, `Reconcile`, `Rollback`, `SetAutoSync`, `SyncWithSource`) return `ErrResourceTerminating` (HTTP 409) on zombies, including over MCP

## Operations

### Argo CD

| Operation | What it does |
|---|---|
| **Sync…** | Opens a dialog with prune / dry-run / apply-only / force / replace / server-side apply / sync-options. Force-only routes via `syncStrategy.hook.force` so PreSync / PostSync hooks still run; ApplyOnly uses `syncStrategy.apply` |
| **Refresh** | Re-fetches the source repo |
| **Hard refresh** | Sets `RefreshType=hard` to bypass repo-cache |
| **Terminate** | Cancels an in-flight operation |
| **Suspend / Enable auto-sync** | Toggles automated sync; remembers the prior `prune` / `selfHeal` settings on suspend so resume restores them |
| **Rollback** | Pick a prior history entry by ID. Force / DryRun flags honored |
| **Selective sync** | From the Topology tab, mark individual resources and sync only those |

### Flux

| Operation | What it does |
|---|---|
| **Reconcile** | Annotates `reconcile.fluxcd.io/requestedAt` to trigger a single reconcile |
| **Sync with source** (Kustomization / HelmRelease) | Reconciles the source first, then the resource itself |
| **Suspend / Resume** | Toggles `spec.suspend` |

### Keyboard shortcuts (per-app detail page)

| Key | Action |
|---|---|
| `s` | Sync (opens the options dialog for Argo) / Reconcile (Flux) |
| `r` | Refresh (Argo) |
| `Shift+R` | Hard refresh (Argo) |
| `t` | Terminate running sync (Argo, only when an op is in flight) |

## Cross-linking from the rest of Radar

The GitOps tab isn't the only place Argo/Flux ownership matters. Surfaces across Radar know about GitOps and route into the right detail page when they should:

- **K8s resource drawer** — every resource that carries Argo's tracking-id annotation or Flux's owner labels gets a `Managed by <app>` chip in the drawer header. Click → owning Application/Kustomization/HelmRelease detail page. The generic `app.kubernetes.io/instance` label is intentionally *not* used as a signal — it's stamped by virtually every Helm chart and would false-positive plain Helm installs
- **Topology** — clicking an Argo / Flux CR node opens its GitOps detail page directly, not the generic drawer
- **Timeline** — lane labels for Argo / Flux CRs route to detail
- **Helm view** — releases installed by Flux's helm-controller (detected via a HelmRelease CR lookup keyed by `<storageNamespace>/<releaseName>`, since Flux's labels live on the *managed* resources, not the release Secret) carry a `Flux` badge in the list and an amber `Managed by Flux · ns/name` link in the drawer, warning that `helm upgrade` would be reverted at the next reconcile
- **Flux source CR drawers** — `GitRepository`, `HelmRepository`, `OCIRepository`, `Bucket` drawers carry a `Consumed by` panel listing every Kustomization + HelmRelease whose `spec.sourceRef` points at the source. Answers "if I edit this, what gets affected on the next reconcile?" without guessing

## MCP integration

`manage_gitops` MCP tool exposes the same actions to AI assistants with per-action input validation:

- Argo: `sync`, `suspend`, `resume`
- Flux: `reconcile`, `suspend`, `resume`

`get_resource` Summary carries lifecycle signal (`terminating`, `finalizers`) so AI assistants can distinguish a zombie from a live resource and won't suggest `Sync` on something pending deletion.

See [MCP server](mcp.md) for the full tool list and security model.

## Demo cluster

`make gitops-demo` bootstraps a `kind` cluster pre-loaded with Argo CD + Flux + a curated set of fixtures covering every UI state the GitOps tab needs to render. Pinned to Argo CD `v2.13.2` + Flux `v2.4.0`.

```bash
make gitops-demo              # bootstrap + apply fixtures
make gitops-demo-drift        # induce stable drift on guestbook-drift
make gitops-demo-down         # teardown
make gitops-demo-status       # inventory what's installed
```

Fixtures cover healthy + drifted + broken-sync + broken-path + manual-sync + suspended + rollback history + ApplicationSet → 3 children + Flux Kustomization with `dependsOn` chain + HelmRelease (managed-resources tree) + Flux zombie (Terminating lifecycle) + broken Kustomization (missing namespace) + broken HelmRelease (no-such-version, retry counter). See `scripts/gitops-demo/README.md` for the full matrix.

## RBAC

Reading status only needs the default ClusterRole's read access on `argoproj.io` / `*.toolkit.fluxcd.io` (enabled by default in the Helm chart's `rbac.crdGroups.argo` / `.flux`).

Triggering Sync / Reconcile / Suspend / Rollback needs `patch` on the parent CRDs. The chart enables the right verbs when `rbac.helm: true` is set, or you can scope it more tightly via `rbac.additionalRules`.

The lifecycle controller-health probe (Home dashboard `GitOps Controllers` card, finalizer-owner attribution in lifecycle Issues) reads pods in `argocd` and `flux-system`. Operators with no access there still get the chip and the basic Issue; the controller-status enrichment is omitted gracefully.

## Single-cluster scope

Radar shows GitOps connections only when the controller and managed resources live in the same cluster. ArgoCD's hub-spoke pattern (controller in one cluster, workloads in another) means Application → resource edges won't render when you're connected to the ArgoCD hub. Flux typically deploys to its own cluster, so connections usually work.

## See also

- [MCP server](mcp.md) — how AI assistants drive GitOps operations and read lifecycle signal
- [Integrations & CRDs](integrations.md) — full CRD support matrix for Argo CD, Flux, and everything else
- [Configuration](configuration.md) — cluster connection, multi-context kubeconfig handling
