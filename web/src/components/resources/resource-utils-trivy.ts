// Trivy Operator CRD utility functions — extracted from resource-utils.ts

import type { StatusBadge } from './resource-utils'
import { healthColors } from './resource-utils'

// ============================================================================
// TRIVY OPERATOR — VULNERABILITY REPORT UTILITIES
// ============================================================================

export interface VulnerabilitySummary {
  critical: number
  high: number
  medium: number
  low: number
  unknown: number
}

export function getVulnerabilityReportSummary(report: any): VulnerabilitySummary {
  const summary = report.report?.summary || {}
  return {
    critical: summary.criticalCount || 0,
    high: summary.highCount || 0,
    medium: summary.mediumCount || 0,
    low: summary.lowCount || 0,
    unknown: summary.unknownCount || 0,
  }
}

export function getVulnerabilityReportStatus(report: any): StatusBadge {
  const summary = getVulnerabilityReportSummary(report)
  if (summary.critical > 0) {
    return { text: `${summary.critical} Critical`, color: healthColors.unhealthy, level: 'unhealthy' }
  }
  if (summary.high > 0) {
    return { text: `${summary.high} High`, color: healthColors.degraded, level: 'degraded' }
  }
  if (summary.medium > 0) {
    return { text: `${summary.medium} Medium`, color: healthColors.degraded, level: 'degraded' }
  }
  if (summary.low > 0 || summary.unknown > 0) {
    return { text: 'Low', color: healthColors.neutral, level: 'neutral' }
  }
  return { text: 'Clean', color: healthColors.healthy, level: 'healthy' }
}

export function getVulnerabilityReportImage(report: any): string {
  const artifact = report.report?.artifact || {}
  const registry = report.report?.registry?.server || ''
  if (!artifact.repository) return '-'
  const repo = registry ? `${registry}/${artifact.repository}` : artifact.repository
  return artifact.tag ? `${repo}:${artifact.tag}` : repo
}

export function getVulnerabilityReportContainer(report: any): string {
  return report.metadata?.labels?.['trivy-operator.container.name'] || '-'
}

// ============================================================================
// TRIVY OPERATOR — CONFIG AUDIT REPORT UTILITIES
// ============================================================================

export interface ConfigAuditSummary {
  critical: number
  high: number
  medium: number
  low: number
}

export function getConfigAuditReportSummary(report: any): ConfigAuditSummary {
  const summary = report.report?.summary || {}
  return {
    critical: summary.criticalCount || 0,
    high: summary.highCount || 0,
    medium: summary.mediumCount || 0,
    low: summary.lowCount || 0,
  }
}

export function getConfigAuditReportStatus(report: any): StatusBadge {
  const summary = getConfigAuditReportSummary(report)
  if (summary.critical > 0) {
    return { text: `${summary.critical} Critical`, color: healthColors.unhealthy, level: 'unhealthy' }
  }
  if (summary.high > 0) {
    return { text: `${summary.high} High`, color: healthColors.degraded, level: 'degraded' }
  }
  if (summary.medium > 0) {
    return { text: `${summary.medium} Medium`, color: healthColors.degraded, level: 'degraded' }
  }
  if (summary.low > 0) {
    return { text: 'Low', color: healthColors.neutral, level: 'neutral' }
  }
  return { text: 'Pass', color: healthColors.healthy, level: 'healthy' }
}

// ============================================================================
// TRIVY OPERATOR — EXPOSED SECRET REPORT UTILITIES
// ============================================================================

// ExposedSecretReport has the same summary/label structure as VulnerabilityReport
export const getExposedSecretReportSummary = getVulnerabilityReportSummary
export const getExposedSecretReportContainer = getVulnerabilityReportContainer
export const getExposedSecretReportImage = getVulnerabilityReportImage

export const getExposedSecretReportStatus = getVulnerabilityReportStatus

// ============================================================================
// TRIVY OPERATOR — RBAC ASSESSMENT REPORT UTILITIES
// ============================================================================

// RbacAssessmentReport and ClusterRbacAssessmentReport have the same
// report.summary + report.checks[] structure as ConfigAuditReport
export const getRbacAssessmentReportSummary = getConfigAuditReportSummary
export const getRbacAssessmentReportStatus = getConfigAuditReportStatus

// ============================================================================
// TRIVY OPERATOR — CLUSTER COMPLIANCE REPORT UTILITIES
// ============================================================================

export function getClusterComplianceReportStatus(report: any): StatusBadge {
  const summary = report.status?.summary || {}
  const fail = summary.failCount || 0
  const pass = summary.passCount || 0
  if (fail > 0) {
    return { text: `${fail} Fail`, color: healthColors.unhealthy, level: 'unhealthy' }
  }
  if (pass > 0) {
    return { text: 'Pass', color: healthColors.healthy, level: 'healthy' }
  }
  return { text: 'Pending', color: healthColors.neutral, level: 'neutral' }
}

// ============================================================================
// TRIVY OPERATOR — SBOM REPORT UTILITIES
// ============================================================================

export function getSbomReportStatus(report: any): StatusBadge {
  const summary = report.report?.summary
  if (summary == null) {
    return { text: 'Pending', color: healthColors.neutral, level: 'neutral' }
  }
  const count = summary.componentsCount ?? 0
  return { text: `${count} components`, color: healthColors.neutral, level: 'neutral' }
}

export const getSbomReportContainer = getVulnerabilityReportContainer
