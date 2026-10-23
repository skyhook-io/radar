import { argoApplicationSetConditionsToGitOpsStatus, argoStatusToGitOpsStatus, fluxConditionsToGitOpsStatus, type FluxCondition, type GitOpsStatus } from '../../types/gitops'
import type { GitOpsResourceTree } from '../../types/gitops-tree'
import { formatCompactAge } from '../../utils/format'
import { apiVersionToGroup } from '../../utils/navigation'

// =============================================================================
// Shared helpers used by both per-cluster Radar's GitOps detail view (OSS
// web/src/components/gitops/GitOpsView.tsx) and Radar Hub's fleet GitOps
// detail page (radar-hub-web/src/pages/fleet/GitOpsDetailPage.tsx). Lives
// here so the two surfaces can't drift on what "in-cluster" means, how
// rollback ids parse, or how terminating tooltips read.
// =============================================================================

// formatGitOpsSourceUrl drops the protocol prefix from a Git source URL
// so the row reads as "github.com/org/repo" instead of the redundant
// "https://github.com/org/repo". Leaves non-https URLs alone so SSH-style
// origins (`git@github.com:org/repo`) and HTTP-only on-prem mirrors still
// render as the user wrote them.
export function formatGitOpsSourceUrl(repo: string): string {
  return repo.replace(/^https?:\/\//, '')
}

// formatGitOpsDestination collapses the canonical in-cluster API server
// URL to the literal "in-cluster" so the destination cell reads cleanly
// for the common case. Empty/whitespace-only server also renders as
// "in-cluster" — matches Argo's default-when-unspecified semantics
// (controller-managed apps without an explicit destination target the
// same cluster the controller runs in). Any other server URL gets its
// protocol stripped for the same reason as the source. Namespace is
// appended on the same line so operators see "<host>, Namespace: <ns>"
// without two lookups.
export function formatGitOpsDestination(server: string | undefined, namespace: string | undefined): string {
  // Normalize: trim whitespace, drop any trailing slash so Argo 2.13's
  // "https://kubernetes.default.svc/" variant (the in-cluster URL with a
  // trailing slash that some controller versions emit) matches the literal
  // we collapse to "in-cluster".
  let host = (server || '').trim().replace(/\/+$/, '')
  if (host === '' || host === 'https://kubernetes.default.svc' || host === 'in-cluster') {
    host = 'in-cluster'
  } else {
    host = host.replace(/^https?:\/\//, '')
  }
  return namespace ? `${host}, Namespace: ${namespace}` : host
}

// gitOpsInsightChangeKey produces the stable string GitOpsChangesView
// matches against to scroll-and-highlight a row when the user clicks an
// issue alert in IssuesBand. Kept here so both pages key their focus
// state identically.
export function gitOpsInsightChangeKey(ref: { kind: string; namespace?: string; name: string }): string {
  return `${ref.kind}/${ref.namespace || ''}/${ref.name}`
}

// parseArgoRollbackID parses an Argo HistoryItem.id into the int64 the
// rollback API needs. Returns null when:
//   - id is missing
//   - id is non-numeric (Flux condition rows reuse the same slot for
//     condition.type, which we never want to interpret as a rollback id)
//   - id is non-positive ("0" and negative strings parse fine via
//     Number() but aren't valid Argo history ids — Argo's id sequence
//     starts at 1; rolling back to id 0 would unhelpfully target the
//     first revision).
export function parseArgoRollbackID(id: string | undefined): number | null {
  if (!id) return null
  const n = Number(id)
  if (!Number.isFinite(n) || n <= 0) return null
  return n
}

// describeGitOpsTerminating renders the two tooltip strings the detail
// header + action buttons need when a resource is mid-deletion.
// Returns:
//   chipTooltip:           "Pending deletion 21d ago. Finalizers: foo, bar.
//                           Mutating actions are disabled until cleanup completes."
//   actionDisabledTooltip: "Disabled — resource is pending deletion (21d)"
//
// The age formatter shares its tier breakpoints with
// pkg/gitops/insights/insights.go::formatAgeShort + pkg/audit/checks.go::
// formatDurationShort so UI, lifecycle Issue messages, and audit findings
// agree on units. Empty input → empty suffix.
export function describeGitOpsTerminating(summary?: {
  terminationStartedAt?: string
  finalizers?: string[]
}): { chipTooltip: string; actionDisabledTooltip: string } {
  const ageText = formatCompactAge(summary?.terminationStartedAt)
  const ageSuffix = ageText ? ` for ${ageText}` : ''
  const finalizers = summary?.finalizers ?? []
  const finSuffix = finalizers.length > 0 ? ` Finalizers: ${finalizers.join(', ')}.` : ''
  const ageInline = ageText ? ` (${ageText})` : ''
  return {
    chipTooltip: `Pending deletion${ageSuffix}.${finSuffix} Mutating actions are disabled until cleanup completes.`,
    actionDisabledTooltip: `Disabled — resource is pending deletion${ageInline}`,
  }
}

const FLUX_KIND_GROUPS: Readonly<Record<string, string | undefined>> = {
  gitrepositories: 'source.toolkit.fluxcd.io',
  ocirepositories: 'source.toolkit.fluxcd.io',
  helmrepositories: 'source.toolkit.fluxcd.io',
  helmcharts: 'source.toolkit.fluxcd.io',
  buckets: 'source.toolkit.fluxcd.io',
  kustomizations: 'kustomize.toolkit.fluxcd.io',
  helmreleases: 'helm.toolkit.fluxcd.io',
  alerts: 'notification.toolkit.fluxcd.io',
}
const ARGO_CD_KINDS = new Set(['applications', 'applicationsets', 'appprojects'])


// getGitOpsResourceStatus dispatches sync/health extraction by kind. Argo
// Applications read from .status (rich operationState + sync state); Argo
// ApplicationSets read their own generator conditions; Flux kinds read
// .status.conditions + .spec.suspend. Returns null if the resource has
// no recognizable status (e.g. just-created, status not yet populated).
export function getGitOpsResourceStatus(kind: string, resource: any): GitOpsStatus | null {
  // Existing callers also pass status-only objects with a controller-specific
  // kind. A loaded API identity, when present, must agree with that kind.
  const group = resource?.apiVersion ? apiVersionToGroup(resource.apiVersion)
    : ARGO_CD_KINDS.has(kind) ? 'argoproj.io' : FLUX_KIND_GROUPS[kind]
  const tool = getGitOpsTool(kind, group)
  if (!tool) return null
  if (kind === 'applications') {
    return argoStatusToGitOpsStatus(resource?.status ?? {})
  }
  // ApplicationSets carry Argo's generator conditions, not Flux's. Without
  // this they fall through to the Flux reader below, which looks for Ready /
  // Reconciling / Stalled, finds none, and reports a failing generator as
  // Unknown.
  if (kind === 'applicationsets') {
    return argoApplicationSetConditionsToGitOpsStatus(
      (resource?.status?.conditions ?? []) as FluxCondition[]
    )
  }
  if (tool === 'argo') return null
  const conditions = (resource?.status?.conditions ?? []) as FluxCondition[]
  return fluxConditionsToGitOpsStatus(conditions, resource?.spec?.suspend === true, {
    generation: resource?.metadata?.generation,
    observedGeneration: resource?.status?.observedGeneration,
  })
}


// getGitOpsTool decides the controller family from the API group, the same
// rule the backend applies (pkg/gitops ToolFor). The kind alone is not
// identity: SIG Apps (app.k8s.io) defines an Application too, and argoproj.io
// is shared with Argo Rollouts and Workflows. null means the object is not a
// supported GitOps resource and has no GitOps view or operations.
export function getGitOpsTool(kind: string, group: string | undefined): 'argo' | 'flux' | null {
  if (group === 'argoproj.io') return ARGO_CD_KINDS.has(kind) ? 'argo' : null
  if (group && FLUX_KIND_GROUPS[kind] === group) return 'flux'
  return null
}

// gitOpsDetailGroup is the API group a detail page acts on: the fetched
// object's own apiVersion once it has loaded, the route's group before that.
export function gitOpsDetailGroup(routeGroup: string, resource: { apiVersion?: string } | null | undefined): string {
  return resource?.apiVersion ? apiVersionToGroup(resource.apiVersion) : routeGroup
}

// isDestinationRef reports whether a resource named on a remote GitOps
// object's page lives on its destination cluster, where this cluster's API
// would answer for a different, same-named object. Only nodes the tree marks
// local (the root, Flux sources and dependencies) are local. A ref the tree
// doesn't list, such as an operation error naming just kind and name, is
// treated as remote, since it came from the destination's reconcile.
export function isDestinationRef(
  tree: GitOpsResourceTree | null | undefined,
  remoteDestination: boolean | undefined,
  ref: { group?: string; kind: string; namespace?: string; name: string },
): boolean {
  if (!remoteDestination) return false
  const node = tree?.nodes.find((n) =>
    n.ref.kind === ref.kind &&
    n.ref.name === ref.name &&
    (n.ref.namespace || '') === (ref.namespace || '') &&
    (n.ref.group || '') === (ref.group || ''))
  return node ? !!node.remote : true
}
