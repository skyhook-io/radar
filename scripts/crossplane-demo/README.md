# Crossplane Demo Cluster

Bootstraps a `kind` cluster with Crossplane installed (core + provider-kubernetes +
function-patch-and-transform) and a curated set of Crossplane fixtures covering the
resource kinds Radar's Crossplane UI needs to render. Use it for visual-testing
changes to the Crossplane surfaces (resource list, MR/XR/Composition/XRD/Function
renderers, audit `crossplaneStuck` check) without needing a real cluster running a
cloud provider.

## Quick start

```bash
# Prerequisites: kind, kubectl, helm
./scripts/crossplane-demo.sh up        # ~3-5 minutes on first run
./scripts/crossplane-demo.sh status    # inventory what's installed

# Run Radar against it
kubectl config use-context kind-radar-crossplane-demo
./scripts/visual-test-start.sh

# When done
./scripts/crossplane-demo.sh down
```

The Make target wraps the same command:

```bash
make crossplane-demo            # = ./scripts/crossplane-demo.sh up
make crossplane-demo-status     # = ./scripts/crossplane-demo.sh status
make crossplane-demo-down       # = ./scripts/crossplane-demo.sh down
```

## What's in the cluster

| Resource | Kind | What it exercises |
|---|---|---|
| `crossplane-system/crossplane` | Deployment | Core Crossplane controller — Healthy controller path |
| `provider-kubernetes` | `Provider` (pkg.crossplane.io/v1) | Provider list view + provider detail renderer + Healthy=True condition + revision tracking |
| `function-patch-and-transform` | `Function` (pkg.crossplane.io/v1) | Function list view + function detail renderer (used inside Composition Pipeline) |
| `default` | `ProviderConfig` (kubernetes.crossplane.io/v1alpha1) | ProviderConfig renderer + InjectedIdentity credential source path |
| `appbundles.demo.example.io` | `CompositeResourceDefinition` (apiextensions.crossplane.io/v2) | XRD renderer + cluster-scoped XRD path + Established condition + offered/served version display |
| `appbundles.demo.example.io` | `Composition` (apiextensions.crossplane.io/v1) | Composition renderer + Pipeline mode + function reference + composed resources list |
| `appbundles.demo.example.io-<hash>` | `CompositionRevision` | CompositionRevision renderer + revision number + spec hash + auto-generated history |
| `standalone-configmap`, `standalone-namespace` | `Object` (kubernetes.crossplane.io/v1alpha1) | **Standalone MR renderer** — Managed Resources created directly (no XR), wrapping a ConfigMap and a Namespace. Drives the Synced=True/Ready=True healthy MR path. |
| `hello-world`, `greeter` | `AppBundle` (demo.example.io/v1alpha1, XR) | **Composite (XR) renderer** — XR instances referencing the Composition. Drive the Synced=False unhealthy path + `crossplaneStuck` audit check (see limitation below). |

### Resource kind coverage

The fixtures collectively cover the Crossplane resource kinds Radar renders:

- ✅ Core Crossplane controller (Deployment in `crossplane-system`)
- ✅ Provider (Healthy=True, with a Revision)
- ✅ Function (Healthy=True, referenced from a Composition Pipeline)
- ✅ ProviderConfig (kubernetes.crossplane.io, InjectedIdentity)
- ✅ CompositeResourceDefinition (XRD, v2, cluster-scoped)
- ✅ Composition (Pipeline mode + function-patch-and-transform input)
- ✅ CompositionRevision (auto-generated from Composition)
- ✅ Standalone Managed Resource — Synced=True, Ready=True (healthy MR path)
- ✅ Composite (XR) — Synced=False (intentionally unhealthy, see below)
- ✅ Audit `crossplaneStuck` finding (the two broken XRs trip the check)

## Known limitation: Crossplane v2 cluster-scoped XR composition is broken

The two `AppBundle` XR instances (`hello-world`, `greeter`) are **expected to stay
`Synced=False`**. They will not produce composed `Object` resources, and you'll see
`Synced=False` on `kubectl get appbundles`.

Why: in Crossplane 2.2.x, a cluster-scoped XR (`scope: Cluster` on a v2 XRD)
cannot compose `provider-kubernetes` `Object` resources via
`function-patch-and-transform`. The composite reconciler refuses to create the
composed objects because of a scope mismatch in v2's namespacing rules — XR is
cluster-scoped but the composed MRs are also cluster-scoped, and the v2 reconciler
doesn't currently handle that combination cleanly. The fix is upstream; tracking
it isn't this fixture's job.

**We keep these broken XRs in the fixture set on purpose** — they are *useful* test
material:

- They exercise Radar's **unhealthy XR rendering path** (Synced=False badge,
  condition message surfacing, last-reconcile timestamp).
- They feed the **`crossplaneStuck` audit check** — the check looks for Managed
  Resources, Composites, and Composite Resource Claims that have been
  `Synced=False` or `Ready=False` for an extended period, and the broken XRs are
  exactly that shape.
- They exercise the **Composite renderer** with realistic spec/status, not just
  an "all green" happy-path render that hides edge cases.

If you specifically need a healthy XR-with-composed-resources fixture, the
workaround is to use a namespace-scoped XR (`scope: Namespaced` on the XRD) — but
that's a different rendering path and a separate fixture concern.

The standalone MRs (`standalone-configmap`, `standalone-namespace`) are healthy
and reconcile normally, so the MR renderer's healthy path is covered.

## Scenarios NOT covered (intentional gaps)

- **Healthy cluster-scoped XR with composed resources** — blocked on the
  upstream Crossplane v2 bug above. A namespace-scoped XRD variant would
  unblock this but adds complexity; skipped for now.
- **Cloud providers (AWS / GCP / Azure)** — using a cloud provider in the demo
  cluster would require real credentials and create real cloud infrastructure.
  `provider-kubernetes` is the right choice for an offline-friendly demo.
- **Composite Resource Claims** — v2 XRDs no longer offer claims; the v2-native
  flow is XR-direct. If we need to render Claims (v1 XRD path), add a v1 XRD
  fixture later.
- **Provider revision conflicts / pinned revisions** — provider rev-history UI
  isn't a planned surface; add a fixture if/when it is.

## Implementation notes

- **Crossplane Helm chart pinned to `1.17.3`**, **provider-kubernetes pinned to
  `v0.18.0`**, **function-patch-and-transform pinned to `v0.9.0`**. Bump in
  `scripts/crossplane-demo.sh` (top of file) when the demo should track a newer
  release.
- The script grants the provider's auto-generated ServiceAccount `cluster-admin`
  via a `ClusterRoleBinding`. provider-kubernetes generates the SA name with a
  revision-hash suffix (`provider-kubernetes-d7f6a...`), so the script discovers
  the SA name dynamically rather than hard-coding it. This binding is what lets
  the provider's `Object` MRs manage arbitrary in-cluster resources.
- The `ProviderConfig` uses `source: InjectedIdentity` so the provider uses its
  own SA token to talk to the apiserver — no external kubeconfig needed, no
  secrets to manage.
- The Composition uses **Pipeline mode** with `function-patch-and-transform`
  (the v2-native composition shape). Patches use `type: FromCompositeFieldPath`
  with `string.type: Format` transforms — that exact shape is what the function
  expects; using shorthand patches (no `type:` field) or string transforms
  without `string.type: Format` will silently no-op.
- The XR composites are intentionally broken (see limitation above) and that's
  *desired* — re-running the script does not "fix" them; reset the cluster if
  you need clean state.
