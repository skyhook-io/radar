# Application grouping — the mental model

A reader-first map of how Radar decides "these workloads are one app" and "these
apps across clusters are the same logical app." This is the *model*, in English;
the code lives in the files named at the end.

---

## 0. The complexity budget — read this before adding anything

This system tends toward complexity (the inputs are a mess), so it has a **hard
design constraint.** Honor it on every change:

1. **The spine stays three sentences.** An app is a *release unit*; *identity* is
   "same app elsewhere," distinct from composition; *declared origins fold across
   clusters, names don't.* If a change makes one of these three harder to state,
   it's probably wrong. Everything else is edge-case handling around this spine —
   it must never *become* the model.
2. **Prefer declaration over inference.** Before adding a heuristic, ask "can the
   user just *say it*?" The `app.skyhook.io/app` annotation exists precisely so
   that when inference gets hairy, we hand the user a one-word escape hatch instead
   of getting cleverer. Reach for that first.
3. **The explainability test.** Every heuristic must be stateable in **one plain
   sentence a non-k8s-expert nods at** (it goes in a tooltip). If it can't be —
   simplify it, or replace it with a declaration. A heuristic you can't explain is
   a heuristic the operator can't trust.

Heuristics are fine; opacity is not. The current **watch-item is env-token
discovery** (§12) — the one piece that fails the one-sentence test today and the
first thing to put on the chopping block if the model needs to lose weight.

---

## 1. Why this is hard

A "deployable application" is **not a Kubernetes object.** There is no `App` kind.
What a human calls "the billing app" is scattered across Deployments, a Worker, a
CronJob, a Service, maybe an Ingress — possibly across several namespaces, and
(in a fleet) across several clusters. Grouping is the act of **reconstructing the
human's notion of an app from the rubble of K8s objects.** Every bit of
complexity below exists because we get only indirect, inconsistent hints.

---

## 2. Two questions, two levels — keep them separate

Almost all confusion comes from conflating two genuinely different problems:

| | Question | Example |
|---|----------|---------|
| **Composition** | Which workloads are **deployed/released as one unit** — that unit *is* the app, in one cluster | the workloads in one Helm release / one Argo Application / one Flux Kustomization |
| **Identity** | Which app-instances are the **same logical app**, across envs/clusters | billing@dev + billing@staging + billing@prod = **billing**, in three environments |

Composition is "what ships and versions together." Identity is "is this the same
app as that one over there." Different layers solve them. When you're lost, first
ask: *am I thinking about composition or identity?*

### What an "app" is — the unit of independent release

An app is **the thing that ships together and is versioned together** — *not* a
logical umbrella over services that happen to be related. This is the single most
important judgment in the whole system, and it is deliberate:

> Radar finds an app by climbing each workload's ownership to the **GitOps/Helm
> manager that deploys it** (an Argo Application, a Flux Kustomization/HelmRelease,
> a Helm release) and **stops there.** It does **not** climb up to the shared git
> repo, or to a parent app-of-apps, even though it could.

That "stop" is the whole point. If `backend` and `frontend` are deployed as **two
separate Argo Applications**, they are **two apps** — even when they live in the
same repo — because each is released and versioned on its own. Climbing to the
shared repo/parent would collapse every separately-released service into one giant
"shop" blob, which has no operational value. We refuse to do that. (Workloads with
**no manager** resolve instead to their **topmost ownerRef** — a Deployment, or an
operator CR like a CNPG `Cluster`.)

The weaker labels (`part-of`, `name`) are a fallback for loose/raw workloads, not
an umbrella over independently-released ones. **One honest caveat to the absolute
claim:** apps are assembled by union-find over *atoms* — each workload contributes
its structural root **and** its single strongest overlay label (§4). In the normal
case a GitOps-managed workload carries its manager's label, so that label *is* the
high-tier atom and the boundary holds. But the union also runs on the overlay
atom, so if two workloads with **distinct manager roots** happen to expose only a
shared weak label (e.g. a manager that doesn't stamp its own label, leaving only
`part-of`), that shared label can still union the two. Precedence makes a stronger
signal win *as the key*, but the union-find can cross a structural boundary via a
shared weak overlay. And note: a shared **satellite** (Service/Ingress/ConfigMap)
never merges two apps — only the workload atoms do.

**The honest edge case:** one Helm *umbrella* chart that bundles many services is
a single release unit, so Radar calls it one app — even if you think of the
services as standalone. Where "released together" and "feels standalone" diverge,
Radar follows the *release unit.* (If that proves too coarse, the refinement is to
split an umbrella by independently-versioned image — a future call, not today's
behavior.)

---

## 3. The pipeline — three layers

A workload flows left-to-right; each layer answers part of the question.

```
  workload ──▶ [1] structuralRoot + ──▶ [2] applications_identity ──▶ [3] hub + fleet fold
                  pkg/subject            app-instances → logical        logical apps →
              workload → release unit    app + env (identity,           one fleet row
              (composition)              per cluster)                   (identity, x-cluster)
```

1. **Composition — `structuralRoot` (topology graph) + `pkg/subject` overlay (per
   cluster).** Climbs each workload's ownership to its **release unit** (the GitOps/
   Helm manager that deploys it) and stops there (§2). The `pkg/subject` overlay
   then *consolidates* roots the graph can't connect — hub-spoke Argo (controller
   in another cluster), native-Helm release annotations — by reading the workload's
   labels via a **precedence cascade** (§4). Output: every workload tagged with an
   **overlay key** (which release unit / app it belongs to) and a **tier** (how
   strong that signal is).

2. **`applications_identity.go` (per cluster, OSS) — identity, within a cluster.**
   Takes the per-cluster app rows and decides which are **env-variants of one
   logical app** (`billing-dev`, `billing-staging` → "billing" spanning dev +
   staging). It attaches an **identity** to each row:
   - `key` — the logical app name (the display name of the group)
   - `env` — this instance's environment (dev | staging | prod | …)
   - `source` — *how* we concluded it (the provenance tier; see §5)
   - `portable` — *may this fold across clusters?* (see §6)

3. **Hub + fleet fold (cross cluster) — identity, across clusters.** The hub gathers
   every cluster's rows and merges them in **two stages** (see §6): first it merges
   any rows sharing the same overlay key (`app.Key`) into one fleet row — a
   *structural* join that can span clusters even for non-declared apps; then the
   fleet fold (`foldAppGroups`, a shared k8s-ui util that Radar Cloud renders)
   collapses the remaining differently-keyed rows that share a **portable**
   identity. Non-portable, differently-keyed rows stay per-cluster.

---

## 4. The composition cascade (the 9 tiers)

When the topology graph can't connect two roots, `pkg/subject` consolidates them
from the workload's labels, walking signals in strict precedence — the **first**
(lowest-numbered) that matches wins. The ranking is **"how strongly does this
signal indicate the release unit"**: a GitOps/Helm manager *is* a release unit (it
deploys a set of workloads together), so it outranks any membership label.

| Tier | Signal | Kind |
|------|--------|------|
| 1 | Flux HelmRelease (`helm.toolkit.fluxcd.io/*`) | **Release unit** (declared GitOps) |
| 2 | Flux Kustomization (`kustomize.toolkit.fluxcd.io/*`) | **Release unit** (declared GitOps) |
| 3 | Argo tracking-id (`argocd.argoproj.io/tracking-id`) | **Release unit** (declared GitOps) |
| 4 | Argo instance (`argocd.argoproj.io/instance`) | **Release unit** (declared GitOps) |
| 5 | Helm release (`meta.helm.sh/release-name`) | **Release unit** (Helm) |
| 6 | `app.kubernetes.io/instance` | Membership label |
| 7 | `app.kubernetes.io/part-of` | Membership label |
| 8 | `app.kubernetes.io/name` | **Name** |
| 9 | bare `app` / name heuristic | **Name** — *disabled in Applications today* |

Confidence: **tiers 1–4 are high** (GitOps controllers), **tiers 5–8 are medium**
(`meta.helm.sh/release-name`, `instance`, `part-of`, `name`), the rest low. So a
Helm-release (tier 5) is a release *unit* but only *medium* confidence, not high.
**Tier 9 (bare `app`) is off by default** — the Applications path calls the
resolver with `allowBareApp=false`, and the overlay is dropped entirely when bare
`app` would be the winner. So in practice the active cascade is tiers 1–8.

**Two consequences of the ranking:** a workload released by its own Argo App
(tier 3) wins on tier over a shared `part-of` (tier 7), so `part-of` doesn't *name*
its app (§2's rule, with the union-find caveat there). And the most important split
for the *cross-cluster* layer is **declared GitOps origin (1–4) vs. a name (6–8)**
(see §6).

---

## 5. The identity tiers (the provenance `source`)

Layer 2 re-expresses the same declared-vs-name split as a `source` on the wire, so
the cross-cluster layer can make a safe decision from a machine-readable field
(not a human evidence string). Three families:

- **Explicit** — `app.skyhook.io/app` annotation. The user *deliberately* declares
  "this is app X." Authoritative; overrides everything. *The escape hatch (§8).*
- **Declared origins** — `argo-path`, `argo-appset`, `flux-source`. A shared
  *upstream*: a GitOps source path with an env overlay, an ApplicationSet that fans
  one app across envs, a Flux Kustomization path.
- **Names** — `label` (`app.kubernetes.io/name`), `name-stem` (inferred from the
  workload name + a shared image repo), `namespace`.

---

## 6. Cross-cluster — there are TWO joins, not one

This is the part most people (including an early draft of this doc) get wrong.
Two rows from two clusters can end up as one fleet row via **two different
mechanisms**, and only the second is "declared-only":

**Join A — the hub merges by overlay key (`app.Key`), first.** The hub stores
rows under `rows[app.Key]` — the per-cluster overlay key. If two clusters produce
the **same overlay key**, they become **one fleet row with a cell per cluster**,
*before identity or portability is even considered.* For a declared app (Argo)
the key is cluster-specific (the Argo identity), so this rarely fires. But for a
**raw app with no GitOps signal, the key is `<ns>/<kind>/<name>`** — so two
clusters each running `default/Deployment/redis` **merge into one `redis` row.**
This join is **structural, not declared-only** — it's a name+namespace match, and
the frontend cannot un-merge what the hub already merged. *(See the open question
at the end — this is arguably an over-merge that should be cluster-scoped for
non-declared rows.)*

**Join B — the fleet fold by portable identity, second.** For rows that did *not*
key-merge (different `app.Key` per cluster — the normal case for declared apps
named per-env, e.g. `billing-dev` vs `billing-prod`), `foldAppGroups` collapses
those that share a **portable identity**. *This* is the declared-only join:

- A **declared origin is collision-free** — same Argo path / ApplicationSet / Flux
  source / explicit annotation ⇒ the same app ⇒ `portable = true` ⇒ fold.
- A **name collides** (two teams' `redis`) ⇒ `portable = false` ⇒ **not** folded by
  Join B; the row stays scoped to its cluster (`localScope`).

So the precise rule is: **`portable = true` only for explicit + declared origins,
and that governs Join B. Join A (the hub key-merge) is a separate, structural
match that declared-only does not currently constrain.** Join B is why grouping
looks "less aggressive" than naive name-matching for *differently-keyed* apps —
that's the point; naive matching merged unrelated apps and reported their health
as one. Join A is the gap where a name-based cross-cluster merge can still slip
through.

---

## 7. Two subtleties that trip people up

**`pathKey` — display the name, fold by name **and** path.** A co-located
declared-path identity (`argo-path`/`flux-source`) *shows* its name stem as `key`
— so that within one cluster a declared instance (`billing-staging`, from
`apps/billing/overlays/staging`) folds with a raw-but-corroborated sibling
(`billing` in dev, no path) — and carries the path stem in `pathKey`. The fleet
fold (Join B) keys portable rows on **`key` + `pathKey`** (literally
`portable:${key}:${pathKey}`): same name *and* same path → fold; same name but
*different* path (`teamA/billing` vs `teamB/billing`) → stay apart. Name for
display, name+path for the join.
*(Caveat: this is the co-located shape. A **hub-spoke** Argo claim instead puts the
path stem directly in `key` and leaves `pathKey` empty — so it's already
collision-safe by construction, but its `key` looks different from a co-located
row's display-name key. Two identical apps that are co-located in one cluster and
hub-spoke in another therefore won't fold together — a known seam.)*

**Add-ons are the one exception to "no name folds."** Platform inventory (coredns,
kube-proxy, cert-manager…) — classified as add-ons, not *your* apps — may fold
across clusters **by name when a shared helm chart or dominant image corroborates**
it's the same software. The harm of a wrong add-on merge is low (it's inventory,
not app health), so the bar is corroboration, not a declared origin. Your apps
never get this latitude.

---

## 8. The user's escape hatch — "how do I force grouping?"

When an app has only a name (no GitOps origin), it stays per-cluster — *and the
operator needs to know why and how to fix it.* The answer:

> Set **`app.skyhook.io/app: <name>`** on the app's workloads, the **same value in
> every cluster**, and Radar folds them. (Or deploy via Argo/Flux with the
> environment in the source path.)

Because the user sets it deliberately, it's a true cross-cluster declaration
(zero collision) — `source = explicit`, `portable = true`, highest precedence.
The in-product tooltip on each row says exactly this when a row is per-cluster.

---

## 9. The hub-spoke twist (centralized Argo)

In hub-spoke Argo the Application *object* lives on a **controller** cluster while
its **workloads** run on a **member** cluster — so the member's own rows never see
the Application, and only the fleet hub sees both. The hub reads each Application's
declared identity + its `spec.destination`, resolves the destination to a member
cluster, and **stamps** the declared identity onto that cluster's matching
workload rows. After that, the normal portable fold (§6) takes over.

---

## 10. Worked examples

**Composition (what is one app):**

0. **`backend` and `frontend`, each its own Argo Application, same repo, both
   labeled `part-of: shop`.** → Each climbs to its *own* Argo App and stops →
   **two apps**, not one "shop." The shared `part-of` (tier 7) never overrides the
   per-Application release boundary (tier 3). This is the anti-umbrella rule. ✓

**Identity (same app across envs/clusters):**

1. **`billing` deployed dev + prod via the same Argo path** `apps/billing/overlays/<env>`.
   → Tier 3 (Argo) → `source=argo-path`, `key=billing`, `pathKey=apps/billing`,
   `portable=true`. Both clusters' rows fold into one **billing · dev prod** row. ✓

2. **Two teams' `redis`, no GitOps, in *different* namespaces** (`team-a/redis`,
   `team-b/redis`), different clusters.
   → different overlay keys → **no Join-A merge**; tier 8 (name) → `portable=false`
   → **no Join-B fold**. They **stay per-cluster** — two `redis` rows. ✓
   *But* if both were `default/redis` (same `ns/kind/name`), they'd share an
   overlay key and **Join A would merge them at the hub** regardless of
   declared-only (the §6 caveat). To fold reliably *if* they're really one app:
   add `app.skyhook.io/app`.

3. **`coredns` in `kube-system` on both clusters.**
   → add-on, same name + same chart → folds across clusters as one add-on row,
   *because* the chart corroborates it's the same software (§7). ✓

---

## 11. Where the code lives

| Concern | File |
|---------|------|
| **Composition: the boundary** — `structuralRoot`, `rootOf`, `groupApplications`, `inputAtoms`, the union-find over atoms | `internal/server/applications.go` |
| Composition: the overlay signal cascade (the 9 tiers, `ResolveOverlay`) | `pkg/subject/overlay.go` |
| Identity within a cluster: env grouping + `source`/`portable`/`pathKey` | `internal/server/applications_identity.go` |
| Hub: Join A (key-merge), forward identity, declared-only promotion, hub-spoke claim stamping, add-on fold | `radar-hub: internal/server/fleet_applications.go` → `ComputeFleetApplications` |
| Cross-cluster Join B (fold by `key`+`pathKey` when `portable`) | `packages/k8s-ui/src/utils/applications.ts` → `foldAppGroups` |
| The "why grouped / how to fold" tooltip | `packages/k8s-ui/src/components/applications/AppTooltips.tsx` |

**One-line summary:** *compose workloads into apps by the strongest deploy/release
signal (stopping at the manager, never the shared repo); across clusters, the hub
first merges identical overlay keys (structural — watch for raw-name over-merge),
then folds the rest only on collision-free declared origins.*

---

## 12. Known gaps / open questions

- **Join A undercuts declared-only for raw apps (§6).** The hub merges rows by
  overlay key *before* portability. Raw apps key on `<ns>/<kind>/<name>`, so two
  clusters' `default/redis` merge into one fleet row even though "names shouldn't
  fold." Declared-only governs Join B but not Join A. *Open: should the hub scope
  non-declared overlay keys by cluster, so only declared identities cross?*
- **Co-located vs hub-spoke key shape (§7).** A path-declared app keys on its
  display name + `pathKey` when co-located, but on the path stem (no `pathKey`)
  when stamped from a hub-spoke claim — so the same app in both topologies won't
  fold. Rare, but a real seam.
- **Umbrella Helm charts (§2).** A chart bundling many independently-versioned
  services is one release unit → one app. If that's too coarse, the refinement is
  to split by independently-versioned image — not today's behavior.
- **⚠️ Complexity watch-item: env-token discovery.** To put a non-trio app on the
  env ladder, the identity resolver *discovers* whether a token like `loadtest` is
  an environment by structural inference — "it recurs across ≥3 repo-corroborated
  name-stem groups spanning ≥2 namespaces." That buys zero-config custom-env
  detection, but it's the **one heuristic that fails the §0 one-sentence test** —
  no operator can predict it. It's the first candidate to simplify if the model
  grows: fall back to the trio (dev/staging/prod) + explicit env labels
  (`app.kubernetes.io/environment`) + GitOps overlay paths, and make custom envs a
  label. Flagged, not yet cut (the zero-config value is real).
