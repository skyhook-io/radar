# Phase 1: Radar Surface Controls

## Objective and Success Criteria

Add install-wide controls that let operators disable native Cost and Helm
surfaces and restrict Resources and Topology to GitOps-managed objects.

Success criteria:

- Existing installs keep current behavior when no new values or flags are set.
- Operators can disable Cost and native Helm through Helm values or CLI flags.
- Disabled features are absent from navigation, routes, background clients,
  dashboards, and MCP registration where applicable.
- Resources and Topology can show only Argo CD- or Flux-managed objects plus
  descendants generated through Kubernetes ownership.
- Resource counts, REST topology, and SSE topology use the same managed-only
  predicate.
- Direct resource GET, Search, Issues, Applications, and Kubernetes RBAC
  behavior remain unchanged.
- Chart rendering, Go tests, frontend checks, and focused runtime smoke tests
  pass.

## Audience

Radar operators running shared in-cluster deployments who need a smaller,
GitOps-focused surface without maintaining a private Radar fork.

## Scope

In scope:

- CLI, application, server, capabilities, and Helm chart configuration.
- Cost/OpenCost and rightsizing route registration and frontend visibility.
- Native Helm client, REST routes, MCP tools, dashboard data, and frontend
  visibility.
- Managed-only filtering for Resources lists/counts and Topology REST/SSE.
- Documentation and tests for the new public configuration.

Out of scope:

- Kubernetes RBAC changes or per-object authorization.
- Filtering Search, Issues, Applications, Packages, resource GET, or generic
  MCP resource tools.
- Removing Flux `HelmRelease` support from GitOps views.
- Changing Prometheus-backed Traffic behavior.
- Release publication or downstream GitOps adoption; those happen after the
  upstream pull request is merged and released.

## Public Interfaces and Defaults

Add Helm values with backward-compatible defaults:

```yaml
features:
  cost: true
  helm: true
  gitOpsManagedOnly: false
```

Add CLI flags:

```text
--disable-cost
--disable-helm
--gitops-managed-only
```

Add resolved feature state to `/api/capabilities`:

```json
{
  "features": {
    "cost": true,
    "helm": true,
    "gitOpsManagedOnly": false
  }
}
```

Older servers or missing `features` fields retain frontend defaults of Cost
enabled, Helm enabled, and managed-only disabled.

## Behavior and Data Flow

### Cost

`features.cost=false` renders `--disable-cost`. The server does not register
OpenCost, application-cost, trend, or rightsizing routes. The frontend omits
Cost navigation, shortcuts, home cards, rightsizing, and resource/workload cost
tabs. Direct Cost URLs replace-navigate to Home. Prometheus Traffic remains
enabled and unchanged.

### Native Helm

`features.helm=false` renders `--disable-helm`. Application bootstrap does not
register Helm subsystem callbacks, so the native Helm client is not initialized
or restarted on context switches. The server does not register native Helm or
Helm-dashboard routes. MCP does not register native Helm tools. Combined
surfaces omit Helm-client data without failing their non-Helm sources. The
frontend omits Helm navigation, home summary, shortcuts, drawers, compare
routes, and direct Helm routes. Flux `HelmRelease` remains available in GitOps.

### GitOps-managed-only Resources and Topology

Managed seeds are:

- resources with `argocd.argoproj.io/tracking-id`
- resources with `argocd.argoproj.io/instance`
- resources with Flux Kustomization or HelmRelease ownership labels
- Argo CD and Flux controller resources already modeled as GitOps nodes

Legacy `app.kubernetes.io/instance` counts as Argo ownership only when it
resolves to a live Argo Application, avoiding false positives from ordinary
native Helm releases.

From those seeds, recursively retain targets of topology `manages` edges. This
keeps generated ReplicaSets, Pods, Jobs, and synthetic grouped descendants even
when only their parent carries the GitOps marker. Plain native Helm metadata,
untracked cluster resources, and GKE/Autopilot system objects are excluded.

Use one shared predicate/index for:

- `/api/resources/{kind}` list filtering
- `/api/resource-counts`
- `/api/topology`
- topology payloads sent through SSE

Keep existing namespace and per-user RBAC filtering first. Managed-only is an
additional visibility filter, not an authorization boundary. Resource GET and
all out-of-scope surfaces retain existing behavior.

The frontend shows a small `GitOps-managed only` scope indicator in Resources
and Topology and uses a specific empty state when no managed objects match.

## Expected Change Areas

- CLI and lifecycle: `cmd/explorer`, `internal/app`, and server configuration.
- API and ownership projection: `internal/server`, `internal/k8s`, and
  `pkg/topology` or the existing `pkg/gitops/tree` ownership helpers.
- UI: capabilities context, primary navigation, Home, Cost/Helm route gates,
  Resources, and Topology.
- Distribution: `deploy/helm/radar`, README, and in-cluster documentation.

No new dependency, generic feature registry, or pluggable policy abstraction
will be introduced. Reuse existing capabilities, topology edges, GitOps
ownership detectors, Helm lifecycle hooks, and conditional route registration.

## Implementation Sequence

1. Commit this approved phase plan as the first local branch commit. Do not
   push or open an upstream pull request until the first validated
   implementation slice is also committed.
2. Add feature configuration types, CLI flags, Helm values/schema, deployment
   arguments, and capabilities response with backward-compatible defaults.
3. Gate native Helm initialization, REST/MCP registration, and combined-source
   fallbacks; add focused backend and chart tests.
4. Gate Cost/OpenCost and rightsizing routes; add focused backend tests.
5. Add the shared GitOps ownership projection and apply it to Resources counts,
   Resources lists, Topology REST, and Topology SSE; add table-driven tests for
   ownership and filtering.
6. Consume capabilities in the frontend to hide disabled surfaces, redirect
   direct routes, and render managed-only scope/empty states; add focused UI
   tests.
7. Update public CLI/chart documentation, then run full build, test, render,
   smoke, secret, and diff validation.
8. Update the Draft PR description and mark Ready only after validation and
   review of the complete diff.

## Edge Cases and Failure Modes

- Missing feature fields: preserve existing enabled behavior.
- Helm disabled while Applications/Packages are open: omit Helm-client data and
  retain label, CRD, Argo, and Flux sources.
- Cost disabled while Prometheus is configured: keep Traffic and non-cost
  metrics operating.
- Workload cluster without Argo Application CRs: modern Argo tracking
  annotations still seed managed resources.
- Generated resources without tracking annotations: retain them through
  recursive `manages` edges.
- Native Helm-only workload: exclude it from managed-only Resources/Topology,
  but do not remove its Kubernetes API accessibility.
- Cyclic or broken ownership edges: use a visited set and terminate safely.
- Namespace/RBAC restriction: intersect managed-only results with existing
  caller permissions; never widen visibility.
- SSE refresh: apply identical filtering on initial REST data and subsequent
  topology events so unmanaged nodes cannot reappear.

## Validation Checklist

Focused automated checks:

```sh
go test ./cmd/explorer/... ./internal/app/... ./internal/server/... ./internal/mcp/... ./pkg/topology/... ./pkg/gitops/tree/...
make tsc
```

Chart checks:

```sh
helm lint deploy/helm/radar
helm template radar deploy/helm/radar >/tmp/radar-default.yaml
helm template radar deploy/helm/radar \
  --set features.cost=false \
  --set features.helm=false \
  --set features.gitOpsManagedOnly=true >/tmp/radar-focused.yaml
```

Assertions:

- Default render contains none of the three new CLI flags.
- Focused render contains all three flags.
- Disabled Cost and Helm APIs return `404`.
- Capabilities report resolved feature state.
- Argo-tracked and Flux-managed resources remain visible.
- Generated descendants remain visible.
- Native Helm-only and untracked GKE/Autopilot resources are absent.
- Counts match filtered lists.
- REST and SSE topology contain the same managed set.
- Direct resource GET and Traffic remain unchanged.

Full validation:

```sh
make test
make build
git diff --check
gitleaks detect --source . --no-git --redact --no-banner
```

Run the repository visual-test workflow for Home, Resources, Topology, direct
Cost/Helm routes, and dark mode. If Playwright MCP remains unavailable, report
that validation blocker explicitly and do not substitute an unsupported visual
harness.

## Rollback

- Revert the upstream pull request. Defaults preserve existing installs, so no
  migration or stored-state rollback is required.
- If downstream adoption exposes a defect, set all feature values back to
  defaults or pin the prior Radar release while the upstream fix is prepared.

## PR Plan

- Branch: `feat/radar-surface-controls`
- Base: `skyhook-io/radar:main`
- Commit 1: approved phase plan only, kept local until implementation exists.
- Commit 2: public configuration and chart plumbing.
- Commit 3: native Helm and Cost runtime gates.
- Commit 4: managed-only backend projection.
- Commit 5: frontend gates, scope indicator, and documentation.
- PR description sections: `Summary`, `Changes`, `Testing`, `Risks/Notes`.
- Never merge without explicit authorization for the exact upstream PR.

## Task Breakdown

- [ ] Commit approved phase plan locally.
- [ ] Open Draft PR only after a validated implementation slice exists.
- [ ] Add public configuration and chart plumbing.
- [ ] Disable native Helm runtime and surfaces.
- [ ] Disable Cost runtime and surfaces.
- [ ] Add managed-only Resources and Topology projection.
- [ ] Add frontend visibility gates and managed-only scope states.
- [ ] Update documentation.
- [ ] Run focused and full validation.
- [ ] Run visual verification or report Playwright MCP blocker.
- [ ] Update Draft PR and mark Ready for review.
