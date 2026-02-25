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

export function getKyvernoPolicyAction(resource: any): string {
  // Check spec-level validationFailureAction (deprecated but still widely used)
  const action = resource.spec?.validationFailureAction
  if (action) return action
  // Fallback: check first rule's validate.failureAction
  const rules = resource.spec?.rules || []
  for (const rule of rules) {
    if (rule.validate?.failureAction) return rule.validate.failureAction
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
      verifyImages: statusCount.verifyImages ?? 0,
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
