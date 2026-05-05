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
| `argocd/guestbook-drift` | Application | Stable OutOfSync: auto-sync on but `selfHeal: false` — run `make gitops-demo-drift` to induce and it stays |
| `argocd/guestbook-manual` | Application | Auto-sync off → exercises ManualDriftWithoutAutoSync detector once drifted |
| `argocd/guestbook-suspended` | Application | Suspended via Radar's annotation pattern → Resume button + suspended chip |
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

### State coverage matrix

The fixtures collectively cover (after a successful first sync):

- ✅ Synced + Healthy (default)
- ✅ Stable OutOfSync / drift (Argo, `selfHeal: false` — run `make gitops-demo-drift`)
- ✅ Suspended (both tools, both annotation styles)
- ✅ Manual sync mode (Argo)
- ✅ Auto-sync with prune + selfHeal (Argo)
- ✅ Dependency chain (Flux dependsOn)
- ✅ App-of-apps nesting (Argo)
- ✅ ApplicationSet list-generator (Argo)
- ✅ Custom AppProject (Argo)
- ✅ Healthy controllers (both `argocd` and `flux-system` namespaces populated)

To add **OutOfSync (live drift)** state, run after the cluster is fully synced:

```bash
./scripts/gitops-demo.sh drift
```

This scales `demo-healthy/guestbook-ui` to 3 replicas while Git declares 1.
Argo will report OutOfSync; `selfHeal: true` will eventually revert it,
which is itself a useful timing window to catch.

## Scenarios NOT covered (intentional gaps)

Adding these would require either a controlled fault injection or a
staged failure setup that adds maintenance burden:

- **Stuck-drift-loop** (mutating webhook persistently changes a synced resource) — needs a custom mutating-webhook deployment
- **ComparisonError** (unreachable Git repo) — would need a private repo or revoke-token dance
- **Failed operation phase** (sync failed mid-flight) — transient; reproduce by `kubectl apply`-ing an invalid manifest then triggering sync
- **Stuck terminating** (zombie + finalizer-owning controller down) — reproduce manually via `kubectl scale -n flux-system deploy/source-controller --replicas=0` then `kubectl delete kustomization podinfo-overlay`
- **Cross-cluster Argo** (Application's destination is a remote cluster) — kind doesn't support multi-cluster easily

The first three are worth manually reproducing during pre-release QA.
The Stuck-terminating scenario is what we built the lifecycle work
against and is easy to recreate via the controller-scale trick above.

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
- Demo namespaces (`demo-healthy`, `demo-suspended`, etc.) are
  pre-created in `01-namespaces.yaml` so apps don't race namespace
  creation. Set `CreateNamespace=false` in syncOptions for Argo apps
  for the same reason.
