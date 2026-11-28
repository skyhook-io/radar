# Kargo

Radar reads [Kargo](https://kargo.io) promotion pipelines (`kargo.akuity.io`, built against Kargo v1.12) and shows, per project, which version each stage holds, whether it is healthy and verified, how its last promotion went, and why the newest version hasn't reached it yet. It is read-only: promoting, approving, aborting and refreshing stay in Kargo.

Where it shows up:

- **Pipelines** (`/kargo`), as **Kargo pipelines** in the Resources sidebar's Integrations ("Akuity Kargo" beneath it) and in the Cmd+K palette (typing "kargo" or "pipeline"), both only when Kargo is discovered. The row's count is the number of Kargo findings in Issues, each counted once.
- **Drawers** for Stage, Freight, Promotion and Warehouse, from the pipelines screen or the ordinary Resources lists. Outside the pipelines screen, Stage, Promotion and Freight drawers open with "Open in pipeline": the object's project, at the Stage's card (a Promotion's Stage; a Freight's Stage when it is in exactly one).
- **Issues**: a stage's last promotion that failed or errored, and a failed verification of the version a stage holds, both critical. They also show under the namespaces the stage's Argo CD Applications deploy to. See [integrations.md](integrations.md#kargo).
- **Argo CD Applications** carrying `kargo.akuity.io/authorized-stage`, and the workloads and pods they manage, name the Kargo stage that can set their version, linked to its pipeline card. The annotation is permission, not proof the stage has updated the app, so the copy says "can set".
- **Related Resources**: Promotion to Stage and Freight, Freight to Warehouse, Stage to its upstream stages and Warehouse, and Stage to the Argo CD Applications its last promotion updated.

## The pipelines screen

Pick a project at the top. Radar opens the project named in the URL (`?project=`); else a project whose stages' Argo CD Applications deploy to a namespace in the namespace filter (Applications the caller can read, deploying to this cluster); else a namespace in the filter that is itself a project; else a project with problems; else one with stages; else the first. When the URL names a project that isn't listed, a notice says so and names the project shown instead. The namespace filter doesn't narrow the page: a Kargo project is a namespace, and the picker chooses it. `?stage=` scrolls to that stage's card and highlights it briefly.

When Kargo is discovered but Radar's own identity can't list Stages, the page says so and points at the chart's `rbac.crdGroups.kargo`, instead of reading as a cluster without projects.

Stages are columns in promotion order, left to right; stages at the same depth stack in one column. On a narrow window the columns stack top to bottom. Each card shows:

| Row | What it says | Where it comes from | When it can't be read |
|---|---|---|---|
| Version | The Freight the stage holds, one per Warehouse it takes versions from: its alias (such as `singing-octopus`) and versions (`nginx:1.31.9`, `app@3f2a9c1`, `podinfo 6.5.0`) | `Stage.status.freightHistory[0].items` for name and versions; the alias from the Freight object (`alias`, else the `kargo.akuity.io/alias` label) | Without Freight access the versions still show and the alias reads as the short Freight name |
| since | When the stage received it | `Freight.status.currentlyIn[<stage>].since` | "since unknown". Verification and promotion times are never used in its place |
| Health | Kargo's health check result and its issues | `Stage.status.health` | "Not reported" when Kargo hasn't run a health check |
| Verification | The latest verification of the version the stage holds, with Kargo's message and a link to the AnalysisRun | `Stage.status.freightHistory[0].verificationHistory[0]` | "Not set up" when the stage declares no verification, whatever Kargo recorded (it records arrival as verified); "Not run yet" when it does and none ran |
| Last promotion | Result, when it finished, and for Failed or Errored why and the step it stopped at. The reason leads with the step's own error: Kargo's wrappers (`step "<alias>" met error threshold of <n>:`, `error running step "<alias>":` and the like) are dropped, and a long error chain keeps its outer and innermost errors. Kargo's full message is in the tooltip and the promotion drawer | `Stage.status.lastPromotion`; a running one from `status.currentPromotion` | "Kargo recorded no reason" when the promotion failed without a message |
| Newest version | The newest version the stage's sources offer that it doesn't hold, and why | Upstream stages' current Freight, and for a direct source the Warehouse's newest Freight | See below |
| Argo CD apps | The Applications the last promotion updated, with health and sync as Kargo's health check read them | The stage's `kargo.akuity.io/argocd-context` annotation; health from `status.health.output` | Left out when the annotation is absent |

Promotion failures and failed verifications also appear above the pipeline as callouts, the same findings the Issues page shows, each with a link to it.

Below the pipeline: the project's **Warehouses** (status, discovery error, newest version, how much Freight it has) and **Recent Freight** (newest ten, with the stages each is in).

Click a stage name, an alias, a promotion's **Open** or a table row to open its drawer.

### Why the newest version isn't here

Radar answers only what the objects establish:

- **Not verified upstream**: the version isn't verified yet in the upstream stage it must pass (any one of them, or all of them when the stage requires all). Per upstream stage, from that stage's verification of this version in its freight history: **failed** ("Failed verification in uat (error-rate). It can't reach prod until it passes there.", in red with a link to the AnalysisRun; the metric comes from the AnalysisRun's message when it names one), **verifying** (pending or running), or **waiting** (no verification recorded, or one that was aborted or inconclusive). It reads as failed when every stage it could pass failed, or, when the stage requires all, when any did. Approving the version for the stage would bypass verification; Radar doesn't suggest it.
- **Soaking**: verified upstream, but it hasn't stayed there for the stage's required soak time. Says how much longer.
- **Ready to promote**: available, and auto-promotion is off for the stage, so it waits for someone to promote it in Kargo.
- **Last promotion failed**: the last promotion of this version to the stage failed.

Anything else reads **Why: unknown**, with what is missing: a promotion is running, the stage has no promotion steps (Kargo moves versions through it on its own), auto-promotion is paused, the stage only promotes what its upstream holds (`MatchUpstream`), Radar can't read the project's Freight or an upstream stage, or the version is available with auto-promotion on and Kargo hasn't promoted it yet. Promotion windows are not read.

The version a stage waits for is the newest one its upstream stages hold. Once the stage holds that, it is the newest version Kargo makes available to the stage (verified upstream, approved for it, or straight from a Warehouse), in Kargo's order: discovery time, then name. A version someone created by hand can therefore count as newer than one the Warehouse found earlier, and a stage reads "behind" it exactly when Kargo would promote it.

## Access

Each kind is authorized on its own, as the caller:

| Caller can read | What they see |
|---|---|
| Stages in the project | The pipeline, versions, health, verification, last promotion |
| Freight in the project | Aliases, "since", Freight-dependent answers to "why", Recent Freight, and the Freight renderer's verification runs |
| Warehouses in the project | The Warehouses table |

Stages readable and Freight denied: versions still show (they are recorded on the stage), aliases and "since" don't, and a note says so. Warehouses denied reads "No access to warehouses", never "no warehouses". A project appears in the picker when the caller can read Stages in its namespace, including a project with no stages yet.

In Radar Cloud, the default integration-read baseline grants every Kargo kind the pipelines screen reads, and withholds ProjectConfig and ClusterConfig: their status shows each webhook receiver's URL, which works as that receiver's shared secret (see [cloud-rbac-baseline.md](cloud-rbac-baseline.md#intentional-exceptions)).

Radar's own read access comes from the chart's `rbac.crdGroups.kargo` (on by default).

## Kargo v1.12 fields Radar depends on

Kargo plans to serve these from its API instead of the Freight object. Radar reads them in one place (`pkg/kargo/freight_status.go`), and without them the values read as unknown:

- `Freight.status.currentlyIn`: "since" on stage cards and drawers, and the stages a Freight is in.
- `Freight.status.verifiedIn`: whether a version is verified upstream, and soak time.
- `Freight.status.approvedFor`: whether a version was approved for a stage.

What a stage holds, and whether an upstream verification of a version failed or is running, is read from `Stage.status.freightHistory`, which stays on the Stage. Radar doesn't use Kargo's Kubernetes Events.

## Open in Radar from Kargo's UI

Kargo shows `stageLinks` on each stage. Add one to a `ProjectConfig` (one project) or the `ClusterConfig` (every project) to open the stage in Radar, with the project picked and the stage's drawer open. Replace `https://radar.example.com` with your Radar address.

```yaml
apiVersion: kargo.akuity.io/v1alpha1
kind: ProjectConfig
metadata:
  name: kargo-demo
  namespace: kargo-demo
spec:
  stageLinks:
  - title: Open in Radar
    description: Pods, logs and events behind this stage
    url: https://radar.example.com/kargo?project={{ .stage.metadata.namespace }}&drawer=stages:kargo.akuity.io:{{ .stage.metadata.namespace }}:{{ .stage.metadata.name }}
```

For `ClusterConfig`, put the same `stageLinks` entry under the `ClusterConfig` named `cluster`. In Kargo v1.12 `url` is a Go template rendered against the stage; Kargo expects this syntax to change in v2.0.

## API

`GET /api/kargo/workspace`: the projects the caller can read Stages in, `{installed, projects: [{name, stages, problems, deployNamespaces?}], coverage: {stages}}`. `deployNamespaces` are the in-cluster namespaces its stages' Argo CD Applications deploy to, from the Applications the caller can read. `coverage.stages` is `uncached` when Radar's own identity can't list Stages. The namespace picker is not applied; the `--namespace-scope` pin is.

`GET /api/kargo/projects/{project}`: one project, `{name, coverage: {stages, freight, warehouses}, freightRead, stages, warehouses, freight, freightTotal, freightByWarehouse?, verificationRuns?, issues}`. Stages come in pipeline order, each with `depth`, `current` (per origin: `name`, `origin`, `alias?`, `versions`, `since?`), `health`, `verification`, `lastPromotion` and `currentPromotion` (`failedStep`, `failure` and `failureCause` for a failed one), `argoCDApps` and `whyNotHere` (per origin: `state` `current|behind|unknown`, `reason`, `cause?`, `message`, `newest?`, `analysisRuns?` for failed upstream verifications). `freight` is each Warehouse's newest 50 Freight plus any a stage holds, without annotations; `freightByWarehouse` is how many each Warehouse has in all, so a Warehouse drawer lists its newest and counts the rest. Every coverage entry is an `integration.KindCoverage`.

`GET /api/kargo/freight-aliases?namespaces=`: `{aliases: {"<namespace>/<freight>": alias}, coverage}` for the Stage and Promotion lists.

All three are behind the `kargoWorkspace` capability. Handlers: `internal/server/kargo_workspace.go`. Rules: `pkg/kargo`. Screens: `web/src/components/kargo/`; drawer enrichment: `web/src/components/kargo/host.tsx`.
