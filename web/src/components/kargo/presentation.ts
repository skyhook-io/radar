import {
  getPromotionPhaseTone,
  getStageHealth,
  getVerificationPhaseTone,
  getWarehouseStatus,
  issueTitle,
  type HealthLevel,
  type Issue,
  type StatusBadge,
  type WorkspaceProblem,
} from '@skyhook-io/k8s-ui'
import type { KargoLagView, KargoPromotionView, KargoStageView, KargoWarehouseView } from '../../api/kargo'
import { KARGO_GROUP } from './routes'

/** A Kargo finding from Issues, as the workspace's problem callout shows it. */
export function kargoIssueProblem(issue: Issue): WorkspaceProblem {
  return {
    id: issue.id,
    category: issue.category,
    // The title the Issues page gives the same row.
    title: issueTitle(issue),
    detail: [issue.message?.trim(), issue.cause?.trim()].filter(Boolean).join(' ') || undefined,
    nextStep: issue.action?.trim() || undefined,
    subject: { kind: issue.kind, group: issue.group ?? KARGO_GROUP, namespace: issue.namespace ?? '', name: issue.name },
    source: 'issue',
    tone: issue.severity === 'critical' ? 'unhealthy' : 'degraded',
  }
}

/** The Stage's state as Kargo's own UI names it, with a tone. */
export function stagePhaseTone(phase: string): HealthLevel {
  switch (phase) {
    case 'Ready': return 'healthy'
    case 'Failed': return 'unhealthy'
    case 'Promoting':
    case 'Verifying':
    case 'Reconciling': return 'neutral'
    default: return 'unknown'
  }
}

export function stageHealth(stage: KargoStageView): { text: string; tone: HealthLevel; detail?: string } {
  if (!stage.health?.status) return { text: 'Not reported', tone: 'unknown', detail: 'Kargo has not run a health check for this stage.' }
  const health = getStageHealth({ status: { health: { status: stage.health.status, issues: stage.health.issues } } })
  return { text: stage.health.status === 'NotApplicable' ? 'No health checks' : stage.health.status, tone: health.tone, detail: health.issues.join(' ') || undefined }
}

export function stageVerification(stage: KargoStageView): { text: string; tone: HealthLevel; detail?: string } {
  // Kargo records a Stage without verification as verified on arrival; that isn't a check that passed.
  if (!stage.verificationConfigured) return { text: 'Not set up', tone: 'neutral' }
  const v = stage.verification
  if (v) return { text: v.phase || 'Unknown', tone: getVerificationPhaseTone(v.phase), detail: v.message }
  return { text: 'Not run yet', tone: 'unknown', detail: 'Kargo has not verified the version this stage holds.' }
}

export function promotionTone(phase: string | undefined): HealthLevel {
  return getPromotionPhaseTone(phase)
}

const FAILURE_CAUSE_MAX = 140
const FAILURE_TAIL_MIN = 80

/**
 * A failed promotion as its card shows it: the cause leading, the step it
 * stopped at, and Kargo's full message for the tooltip. A long error chain
 * ("a: b: c: d") keeps its outer error and its innermost ones, which say why,
 * so a clamped line doesn't cut the reason off.
 */
export function promotionFailure(p: Pick<KargoPromotionView, 'failedStep' | 'failure' | 'failureCause'>): { cause: string; step?: string; full: string } {
  const full = p.failure?.trim()
  if (!full) return { cause: 'Kargo recorded no reason.', step: p.failedStep, full: 'Kargo recorded no reason.' }
  return { cause: shortenErrorChain(p.failureCause?.trim() || full), step: p.failedStep, full }
}

function shortenErrorChain(text: string): string {
  if (text.length <= FAILURE_CAUSE_MAX) return text
  const segments = text.split(': ')
  if (segments.length < 2) return text
  let start = segments.length - 1
  while (start > 1 && segments.slice(start).join(': ').length < FAILURE_TAIL_MIN) start--
  const tail = segments.slice(start).join(': ')
  const withHead = `${segments[0]}: … ${tail}`
  return withHead.length <= FAILURE_CAUSE_MAX ? withHead : `… ${tail}`
}

/** The lag answer's tone: behind is the reader's question, not a fault; unknown reads as unknown. */
export function lagTone(lag: KargoLagView): HealthLevel {
  if (lag.state === 'current') return 'healthy'
  if (lag.state === 'unknown') return 'unknown'
  return lag.reason === 'lastPromotionFailed' || lag.reason === 'verificationFailedUpstream' ? 'unhealthy' : 'neutral'
}

export function warehouseStatus(wh: KargoWarehouseView): StatusBadge {
  return getWarehouseStatus({ status: { conditions: wh.conditions } })
}

/** The discovery error Kargo reports on a Warehouse, if any. */
export function warehouseError(wh: KargoWarehouseView): string | undefined {
  const healthy = wh.conditions?.find((c) => c.type === 'Healthy')
  return healthy?.status === 'False' ? healthy.message || healthy.reason : undefined
}

/** Stages grouped into pipeline columns, left to right. */
export function pipelineColumns(stages: KargoStageView[]): KargoStageView[][] {
  const columns: KargoStageView[][] = []
  for (const s of stages) {
    ;(columns[s.depth] ??= []).push(s)
  }
  return columns.filter(Boolean)
}
