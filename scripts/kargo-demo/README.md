# Kargo Demo Cluster

Builds a cluster with a Kargo pipeline in a mixed state, for visual-testing
Radar's Kargo surfaces. It installs cert-manager, Argo CD, Argo Rollouts,
Kargo 1.12.7 and an in-cluster Gitea, then runs a promotion story against a
project called `kargo-demo` (stages `test` -> `uat` -> `prod`).

## Quick start

```bash
# Prerequisites: kind, kubectl, helm, git, curl, python3, internet access
./scripts/kargo-demo.sh up        # about 10 minutes on first run
./scripts/kargo-demo.sh status

kubectl config use-context kind-radar-kargo-demo
./scripts/visual-test-start.sh

./scripts/kargo-demo.sh down
```

To use a cluster you already have instead of kind, set `KUBE_CONTEXT`. `up`
then installs into that cluster and `down` uninstalls everything from it
(it never deletes the cluster):

```bash
KUBE_CONTEXT=my-dev-cluster ./scripts/kargo-demo.sh up
```

## End state

| Where | State |
|-------|-------|
| Freight 1.31.9 (added by hand) | Verified in test, uat and prod. Current freight of prod. |
| Freight newer (found by the warehouse, nginx 1.31.10 at the time of writing) | Current freight of test and uat. Verified in test only. |
| Stage uat | Verification of the newer freight Failed. Healthy but not Ready. |
| Stage test | Latest promotion is Errored (git push refused over plain HTTP). Health Unknown, not Ready. |
| Stage prod | One Failed promotion (a `release-gate` step refused it), then a Succeeded one. |
| ProjectConfig | Auto-promotion on test is off, on purpose. |

## How it works

- The `Promotion` objects are created with kubectl, with the steps copied from
  each stage's promotion template. The Kargo CLI is not needed.
- The Failed prod promotion comes from a `fail` step added to the prod
  template for one attempt, then removed.
- The Errored test promotion comes from turning off the Kargo controller's
  `allowCredentialsOverHTTP` for one promotion. Gitea is plain HTTP, so the
  git push step is refused. The flag goes back on afterwards.
- The newer freight depends on what `public.ecr.aws/nginx/nginx` publishes
  (`^1.29.0`). Tags may move on over time. The phases do not.
- If the project already has promotions, `up` leaves them alone. Run `down`
  and `up` to rebuild the story.

## Demo-only values

The Argo CD and Kargo admin password is `admin`. The Gitea user and the
database password are fixed strings in the script. None of them are safe
outside a throwaway cluster.

Open the UIs with the port-forwards `up` prints at the end.
