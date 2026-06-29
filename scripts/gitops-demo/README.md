# GitOps Demo Cluster

Bootstraps a `kind` cluster with Argo CD + Flux installed and a curated
set of GitOps fixtures covering the scenarios Radar's GitOps tab needs
to render correctly. Use it for visual-testing changes to the GitOps UI
or for catching regressions across multiple controller states without
needing a real production cluster.

## Quick start

```bash
# Prerequisites: kind, kubectl
./scripts/gitops-demo.sh up        # ~5 minutes on first run
./scripts/gitops-demo.sh status    # inventory what's installed

# Run Radar against it
kubectl config use-context kind-radar-gitops-demo
./scripts/visual-test-start.sh

# When done
./scripts/gitops-demo.sh down
```

## What's in the cluster

### Argo CD scenarios

| Resource | Kind | What it exercises |
|---|---|---|
| `argocd/guestbook-healthy` | Application | Synced + Healthy, auto-sync on, default success path |
| `argocd/guestbook-drift` | Application | Stable OutOfSync: auto-sync on but `selfHeal: false` — run `make gitops-demo-drift` to induce multi-field drift |
| `argocd/guestbook-manual` | Application | Auto-sync off → exercises ManualDriftWithoutAutoSync detector once drifted |
| `argocd/guestbook-suspended` | Application | Suspended via Radar's annotation pattern → Resume button + suspended chip |
| `argocd/guestbook-broken-path` | Application | Stable `ComparisonError`: targetRevision points at a non-existent path. Drives the argoApplicationConditions detector. |
| `argocd/guestbook-broken-sync` | Application | Stable `Failed` operation phase: target namespace doesn't exist + `CreateNamespace=false`. Drives GitOpsFailureCard + parseArgoOperationError + retry/Cause/Stuck. |
| `argocd/guestbook-rollback` | Application | Manual mode + 2 distinct history entries (orchestrated by the script). Rollback button enabled on the older entry. |
| `argocd/app-of-apps-parent` | Application | App-of-apps: parent that manages 3 child Applications → portal-node + lineage breadcrumb |
| `argocd/radar-demo-set` | ApplicationSet | List generator → 3 child Applications (`set-vanilla`, `set-kustomize`, `set-helm`) |
| `argocd/radar-demo` | AppProject | Custom project (non-default) for fleet view's Project filter |

### Flux scenarios

| Resource | Kind | What it exercises |
|---|---|---|
| `flux-system/podinfo` | GitRepository | Source for the Kustomization scenarios |
| `flux-system/podinfo` | HelmRepository | Source for the HelmRelease scenario |
| `flux-system/podinfo-base` | Kustomization | Healthy Kustomization, applied first |
| `flux-system/podinfo-overlay` | Kustomization | Healthy Kustomization with `dependsOn: podinfo-base` (dependency chain) |
| `flux-system/podinfo-suspended` | Kustomization | `spec.suspend: true` → Suspended chip + Resume button |
| `flux-system/podinfo` | HelmRelease | Helm chart from HelmRepository → exercises helm-controller path + Sync-with-source verb |
| `flux-system/podinfo-broken` | Kustomization | Stable Flux failure: `targetNamespace: demo-flux-missing` doesn't exist → kustomize-controller leaves it `Ready=False` (`ReconciliationFailed`). Flux equivalent of `guestbook-broken-sync`. |
| `flux-system/podinfo-broken` | HelmRelease | Stable HelmRelease failure: `version: '>=99.0.0'` matches no chart → helm-controller leaves it `InstallFailed` with `spec.install.remediation.retries: 3`. Drives Helm-specific UI (`lastAttemptedRevision`, retry state). |
| `flux-system/zombie-kustomization` | Kustomization | Stuck `Terminating` via fake finalizer (no controller scaled down). Drives lifecycle severity ramp + finalizer attribution + `[Terminating]` chip + Audit `stuckTerminating`. Severity tier ramps with cluster age: info → warning at 5min → alert at 30min. |

### State coverage matrix

The fixtures collectively cover (after a successful first sync):

- ✅ Synced + Healthy (default)
- ✅ Stable OutOfSync / drift (Argo, `selfHeal: false` — run `make gitops-demo-drift` for the rich multi-field case)
- ✅ Suspended (both tools, both annotation styles)
- ✅ Manual sync mode (Argo)
- ✅ Auto-sync with prune + selfHeal (Argo)
- ✅ ComparisonError (Argo, broken-path app)
- ✅ Failed sync phase (Argo, broken-sync app)
- ✅ Multi-entry history with rollback button enabled (Argo, rollback app)
- ✅ Dependency chain (Flux dependsOn)
- ✅ App-of-apps nesting (Argo)
- ✅ ApplicationSet list-generator (Argo)
- ✅ Custom AppProject (Argo)
- ✅ Stuck Terminating with finalizer attribution (Flux zombie)
- ✅ Healthy controllers (both `argocd` and `flux-system` namespaces populated)

The drift inducer adds a stable OutOfSync state after the cluster is up:

```bash
./scripts/gitops-demo.sh drift
```

It mutates three field types on `demo-drift/guestbook-ui` — replicas
(scalar), an annotation (added entry on a nested map), and a resource
limits block (added entry on a deeper nested map) — so the Changes tab
exercises the full diff renderer, not just one scalar.

## Journey coverage matrix

How the fixture set maps to the top GitOps user journeys. "Covered" =
the fixture exercises the journey end-to-end through Radar's UI today.
"Data shape present" = the fixture cluster contains the resources a
future feature would consume; UI work is what's missing.

| # | Journey | Status | What's used / what's still needed |
|---|---|---|---|
| 1 | Did my deploy land healthy? | ✅ covered | `guestbook-healthy`, `set-*`, podinfo Kustomizations + HelmRelease |
| 2 | Why did this sync fail? | ✅ covered | `guestbook-broken-path` (ComparisonError), `guestbook-broken-sync` (Failed phase) |
| 3 | What's drifted between Git and live? | ✅ covered | `./scripts/gitops-demo.sh drift` — multi-field drift on `guestbook-drift` |
| 4 | Roll back to a known-good revision | ✅ covered | `guestbook-rollback` orchestrated to Manual mode + 2 history entries |
| 5 | Fleet health right now | ✅ covered | 15-app mix across all sync/health states |
| 6 | Sync now | ✅ covered | Sync button on every Argo + Flux fixture |
| 7 | Why is this resource Missing? | ✅ covered | `guestbook-broken-sync` produces "namespaces 'demo-broken-sync' not found" — RBAC-style failure with parseable cause |
| 8 | Suspend / resume auto-sync | ✅ covered | `guestbook-suspended` (Argo annotations), `podinfo-suspended` (Flux `spec.suspend`) |
| 9 | What's stuck / needs attention? | ✅ covered | `zombie-kustomization` — fake finalizer, severity ramps with age |
| 10 | What will Sync actually do? | ✅ covered | `guestbook-manual` is unsynced → Sync Plan populated |
| 11 | Who synced what when? | ✅ covered | History panel on `guestbook-rollback` (3 entries) and any synced app |
| 12 | Search fleet by repo / path / chart | ✅ covered | 2 source repos, 4 paths, 1 helm chart in fleet |
| 13 | Show desired manifest preview | ⚠️ data shape present | Any healthy app provides the data; no UI yet |
| 14 | Resource → owning-app reverse lookup | ⚠️ data shape present | Argo writes `argocd.argoproj.io/instance` labels on every synced resource; no reverse-lookup UI yet |
| 15 | Hard refresh / cache bust | ✅ covered | Hard Refresh button on every Argo app |
| 16 | Compare environments side-by-side | ⚠️ partial proxy | The `set-vanilla` / `set-kustomize` / `set-helm` ApplicationSet trio gives "multiple apps, same source repo, different paths/destinations" — close to but not literally "same path, two environments." If/when a side-by-side feature gets specced and "literally same overlay across envs" is the right shape, add a `demo-multi-env-staging` + `demo-multi-env-prod` pair pointing at the same path. |
| 17 | dependsOn / sync wave ordering | ✅ covered | `podinfo-overlay dependsOn podinfo-base` |
| 18 | Get paged when an app degrades | ⚠️ data shape present | `guestbook-broken-sync` and the zombie give degraded targets to fire alerts on; no in-UI alerting hookup yet |
| 19 | Inspect HelmRelease values + chart version | ✅ covered | podinfo HelmRelease |
| 20 | Onboard a new app | N/A | Write-only feature; no fixture concept applies |

## Scenarios NOT covered (intentional gaps requiring real engineering effort)

- **Stuck-drift-loop** (mutating webhook persistently changes a synced resource) — would need a custom mutating-webhook deployment in the kind cluster. Worth reproducing manually during pre-release QA: deploy a webhook that mutates `spec.replicas` on every admission, point an Argo Application at a Deployment, watch the StuckDriftLoop detector fire.
- **Cross-cluster Argo** (Application destination is a remote cluster) — kind doesn't support multi-cluster easily.

## Implementation notes

- **Argo CD pinned to `v2.13.2`**, **Flux pinned to `v2.4.0`**. Bump in
  `scripts/gitops-demo.sh` (top of file) when the demo should track a
  newer release.
- The fixtures rely on **public Git repos** (`argoproj/argocd-example-apps`,
  `stefanprodan/podinfo`) that are stable, MIT-licensed, and used as
  reference points by Argo + Flux upstream. If we ever need offline
  operation, mirror them into an in-cluster gitea pod and update the
  `repoURL` fields.
- The Argo `radar-demo` AppProject scopes destinations to `demo-*`
  namespaces. Adding new demo Applications outside that pattern
  requires extending `02-argo-appproject.yaml`.
- The zombie Kustomization uses a fake finalizer (`radar-demo.io/intentional-zombie`)
  rather than scaling Flux's source-controller down — this keeps the
  zombie isolated, so the healthy Flux fixtures keep reconciling
  normally alongside it. Removing the zombie requires
  `kubectl patch -n flux-system kustomization zombie-kustomization
  --type json -p '[{"op":"remove","path":"/metadata/finalizers"}]'`.
- Demo namespaces (`demo-healthy`, `demo-suspended`, etc.) are
  pre-created in `01-namespaces.yaml` so apps don't race namespace
  creation. Set `CreateNamespace=false` in syncOptions for Argo apps
  for the same reason.
