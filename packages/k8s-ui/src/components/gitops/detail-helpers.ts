import { argoStatusToGitOpsStatus, fluxConditionsToGitOpsStatus, type FluxCondition, type GitOpsStatus } from '../../types/gitops'
import { formatCompactAge } from '../../utils/format'

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
  const ageSuffix = ageText ? ` ${ageText} ago` : ''
  const finalizers = summary?.finalizers ?? []
  const finSuffix = finalizers.length > 0 ? ` Finalizers: ${finalizers.join(', ')}.` : ''
  const ageInline = ageText ? ` (${ageText})` : ''
  return {
    chipTooltip: `Pending deletion${ageSuffix}.${finSuffix} Mutating actions are disabled until cleanup completes.`,
    actionDisabledTooltip: `Disabled — resource is pending deletion${ageInline}`,
  }
}

// getGitOpsResourceStatus dispatches sync/health extraction by tool. Argo
// reads from .status (rich operationState + sync state); Flux reads from
// .status.conditions + .spec.suspend. Returns null if the resource has
// no recognizable status (e.g. just-created, status not yet populated).
export function getGitOpsResourceStatus(kind: string, resource: any): GitOpsStatus | null {
  if (kind === 'applications') {
    return argoStatusToGitOpsStatus(resource?.status ?? {})
  }
  const conditions = (resource?.status?.conditions ?? []) as FluxCondition[]
  return fluxConditionsToGitOpsStatus(conditions, resource?.spec?.suspend === true)
}

// getGitOpsTool routes a kind+group to 'argo' or 'flux'. Same shape used
// by both detail pages + the fleet list normalizers so a kustomization
// always reads as 'flux' and an Argo Application always as 'argo'.
export function getGitOpsTool(kind: string, group?: string): 'argo' | 'flux' {
  if (group === 'argoproj.io' || kind === 'applications' || kind === 'applicationsets' || kind === 'appprojects') return 'argo'
  return 'flux'
}
