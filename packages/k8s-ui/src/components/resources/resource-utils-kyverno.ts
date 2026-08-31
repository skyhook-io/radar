// Kyverno / Policy Report CRD utility functions

import type { StatusBadge } from './resource-utils'
import { healthColors, formatAge } from './resource-utils'

// ============================================================================
// SHARED HELPERS
// ============================================================================

interface PolicyReportSummary {
  pass: number
  fail: number
  warn: number
  error: number
  skip: number
}

function extractSummary(resource: any): PolicyReportSummary {
  const summary = resource.summary || {}
  return {
    pass: summary.pass ?? 0,
    fail: summary.fail ?? 0,
    warn: summary.warn ?? 0,
    error: summary.error ?? 0,
    skip: summary.skip ?? 0,
  }
}

function extractResults(resource: any): any[] {
  return resource.results || []
}

// ============================================================================
// POLICYREPORT UTILITIES (wgpolicyk8s.io/v1alpha2)
// ============================================================================

export function getPolicyReportStatus(resource: any): StatusBadge {
  const summary = extractSummary(resource)

  if (summary.error > 0) {
    return { text: `${summary.error} Error`, color: healthColors.unhealthy, level: 'unhealthy' }
  }
  if (summary.fail > 0) {
    return { text: `${summary.fail} Fail`, color: healthColors.unhealthy, level: 'unhealthy' }
  }
  if (summary.warn > 0) {
    return { text: `${summary.warn} Warn`, color: healthColors.degraded, level: 'degraded' }
  }
  if (summary.pass > 0) {
    return { text: 'Pass', color: healthColors.healthy, level: 'healthy' }
  }
  if (summary.skip > 0) {
    return { text: 'Skip', color: healthColors.neutral, level: 'neutral' }
  }
  return { text: 'Empty', color: healthColors.unknown, level: 'unknown' }
}

export function getPolicyReportSummary(resource: any): PolicyReportSummary {
  return extractSummary(resource)
}

export function getPolicyReportResults(resource: any): any[] {
  return extractResults(resource)
}

export function getPolicyReportResultCount(resource: any): number {
  return extractResults(resource).length
}

export function getPolicyReportScope(resource: any): string {
  const scope = resource.scope
  if (!scope) return '-'
  const parts = [scope.kind, scope.namespace, scope.name].filter(Boolean)
  return parts.join('/') || '-'
}

export function getPolicyReportSource(resource: any): string {
  // Source may be on the report itself or on individual results
  if (resource.source) return resource.source
  const results = extractResults(resource)
  if (results.length > 0 && results[0].source) return results[0].source
  return '-'
}

// ============================================================================
// CLUSTERPOLICYREPORT UTILITIES (wgpolicyk8s.io/v1alpha2)
// ============================================================================

// ClusterPolicyReport has the same structure as PolicyReport (cluster-scoped)
export const getClusterPolicyReportStatus = getPolicyReportStatus
export const getClusterPolicyReportSummary = getPolicyReportSummary
export const getClusterPolicyReportResults = getPolicyReportResults
export const getClusterPolicyReportResultCount = getPolicyReportResultCount

// ============================================================================
// KYVERNO POLICY UTILITIES (kyverno.io/v1)
// ============================================================================

export function getKyvernoPolicyStatus(resource: any): StatusBadge {
  const conditions = resource.status?.conditions || []

  const readyCond = conditions.find((c: any) => c.type === 'Ready')
  if (readyCond?.status === 'True') {
    return { text: 'Ready', color: healthColors.healthy, level: 'healthy' }
  }
  if (readyCond?.status === 'False') {
    return { text: readyCond.reason || 'Not Ready', color: healthColors.unhealthy, level: 'unhealthy' }
  }

  // Fallback: if spec exists, likely active
  if (resource.spec?.rules?.length > 0) {
    return { text: 'Active', color: healthColors.healthy, level: 'healthy' }
  }

  return { text: 'Unknown', color: healthColors.unknown, level: 'unknown' }
}

/**
 * Kyverno's ValidationFailureAction enum accepts the deprecated lowercase
 * `enforce` alongside `Enforce`, and the admission controller blocks on the
 * lowercase spelling exactly as on the capitalized one. Match case-insensitively
 * so a legacy policy carrying the lowercase value is not read as non-blocking.
 */
export function isKyvernoEnforceAction(action: unknown): boolean {
  return String(action ?? '').toLowerCase() === 'enforce'
}

/** The rule blocks whose failure an admission request can be rejected for. */
function rejectingActions(rule: any): string[] {
  if (!rule) return []
  const actions: string[] = []
  if (rule.validate) actions.push(rule.validate.failureAction)
  if (Array.isArray(rule.verifyImages)) {
    for (const v of rule.verifyImages) actions.push(v?.failureAction)
  }
  return actions.filter(Boolean)
}

/**
 * Whether this policy can REJECT an admission request, as Audit or Enforce.
 *
 * Three things make the obvious reading wrong. A rule-level failureAction
 * OVERRIDES `spec.validationFailureAction` rather than deferring to it, so
 * consulting the spec field first answers Audit for a policy whose rule
 * enforces. Image verification carries its own failureAction on
 * `verifyImages[]` rather than under `validate`, so a signing policy blocks
 * without either of the fields the name suggests. And the spec field is
 * defaulted to Audit when absent, so its presence proves nothing.
 *
 * Only validate and verifyImages rules can reject at all, so only they inherit
 * the spec-level action — a mutate or generate rule sitting in the same policy
 * would otherwise carry Enforce into a policy where nothing enforces.
 *
 * Rules can disagree, and one value has to stand for the whole policy. The
 * question worth answering is whether anything here can reject, so any Enforce
 * wins; the Rules section is where a mixed policy is read rule by rule.
 */
export function getKyvernoPolicyAction(resource: any): string {
  const specAction = resource.spec?.validationFailureAction
  const rules = resource.spec?.rules || []
  if (rules.length === 0) return isKyvernoEnforceAction(specAction) ? 'Enforce' : 'Audit'
  const rejecting = rules.filter((rule: any) => rule?.validate || rule?.verifyImages)
  // Rules, but none that can reject: the spec-level action governs nothing here.
  if (rejecting.length === 0) return 'Audit'
  for (const rule of rejecting) {
    const overrides = rejectingActions(rule)
    const effective = overrides.length > 0 ? overrides : [specAction]
    if (effective.some(isKyvernoEnforceAction)) return 'Enforce'
  }
  return 'Audit'
}

export function getKyvernoPolicyRuleCount(resource: any): number {
  return (resource.spec?.rules || []).length
}

export function getKyvernoPolicyRuleTypes(resource: any): string {
  const rules = resource.spec?.rules || []
  const types = new Set<string>()
  for (const rule of rules) {
    if (rule.validate) types.add('validate')
    if (rule.mutate) types.add('mutate')
    if (rule.generate) types.add('generate')
    if (rule.verifyImages) types.add('verifyImages')
  }
  if (types.size === 0) return '-'
  return Array.from(types).join(', ')
}

export function getKyvernoPolicyRules(resource: any): Array<{
  name: string
  type: string
  hasMatch: boolean
  hasExclude: boolean
}> {
  const rules = resource.spec?.rules || []
  return rules.map((rule: any) => {
    let type = 'unknown'
    if (rule.validate) type = 'validate'
    else if (rule.mutate) type = 'mutate'
    else if (rule.generate) type = 'generate'
    else if (rule.verifyImages) type = 'verifyImages'

    return {
      name: rule.name || '-',
      type,
      hasMatch: !!rule.match,
      hasExclude: !!rule.exclude,
    }
  })
}

export function getKyvernoPolicyBackground(resource: any): boolean {
  return resource.spec?.background !== false
}

export function getKyvernoPolicyRuleCountByType(resource: any): {
  validate: number
  mutate: number
  generate: number
  verifyImages: number
} {
  // Use status rulecount if available, otherwise compute from rules
  const statusCount = resource.status?.rulecount
  if (statusCount) {
    return {
      validate: statusCount.validate ?? 0,
      mutate: statusCount.mutate ?? 0,
      generate: statusCount.generate ?? 0,
      // Kyverno writes this key all-lowercase (`verifyimages`), verified on
      // 1.18.2. Reading the camelCase spelling alone returns 0 for every
      // image-verification policy in existence.
      verifyImages: statusCount.verifyImages ?? statusCount.verifyimages ?? 0,
    }
  }

  const rules = resource.spec?.rules || []
  const counts = { validate: 0, mutate: 0, generate: 0, verifyImages: 0 }
  for (const rule of rules) {
    if (rule.validate) counts.validate++
    if (rule.mutate) counts.mutate++
    if (rule.generate) counts.generate++
    if (rule.verifyImages) counts.verifyImages++
  }
  return counts
}

export function getKyvernoPolicyAutogenRules(resource: any): string[] {
  const autogen = resource.status?.autogen?.rules || []
  return autogen.map((r: any) => r.name).filter(Boolean)
}

export function getKyvernoPolicyLastScheduleTime(resource: any): string {
  const lastSchedule = resource.status?.lastScheduleTime
  if (!lastSchedule) return '-'
  return formatAge(lastSchedule)
}

// ============================================================================
// KYVERNO CLUSTERPOLICY UTILITIES (kyverno.io/v1)
// ============================================================================

// ClusterPolicy has the same structure as Policy (cluster-scoped)
export const getClusterPolicyStatus = getKyvernoPolicyStatus
export const getClusterPolicyAction = getKyvernoPolicyAction
export const getClusterPolicyRuleCount = getKyvernoPolicyRuleCount
export const getClusterPolicyRuleTypes = getKyvernoPolicyRuleTypes
export const getClusterPolicyRules = getKyvernoPolicyRules
export const getClusterPolicyBackground = getKyvernoPolicyBackground
export const getClusterPolicyRuleCountByType = getKyvernoPolicyRuleCountByType
