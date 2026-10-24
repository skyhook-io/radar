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
// TRIVY OPERATOR — TABLE SEARCH OVER FINDINGS
// ============================================================================

export const TRIVY_GROUP = 'aquasecurity.github.io'

// Bounds one table search over findings. The filter runs on the main thread,
// and a cluster can hold thousands of reports with hundreds of findings each.
const MAX_TABLE_FINDINGS = 200_000

// Server search caps one indexed finding at 256 bytes (internal/search/trivy.go).
const MAX_FINDING_LABEL = 256

export interface TrivyFindingMatch {
  site: 'finding' | 'image'
  text: string
}

export interface TrivyFindingsSearch {
  // Rows that matched only through a finding or the scanned image.
  matches: Map<object, TrivyFindingMatch>
  truncated: boolean
}

function str(v: unknown): string {
  return typeof v === 'string' ? v : ''
}

function asArray(v: unknown): any[] {
  return Array.isArray(v) ? v : []
}

// findingLabel joins a finding's parts and caps it at MAX_FINDING_LABEL
// UTF-8 bytes on a character boundary, as server search does, so a phrase
// that falls past the cap matches in neither place.
function findingLabel(...parts: string[]): string {
  const label = parts.filter(Boolean).join(' ')
  // A UTF-16 unit is at most 3 UTF-8 bytes, so short labels need no count.
  if (label.length * 3 <= MAX_FINDING_LABEL) return label
  let bytes = 0
  let end = 0
  for (const ch of label) {
    const cp = ch.codePointAt(0) ?? 0
    const size = cp < 0x80 ? 1 : cp < 0x800 ? 2 : cp < 0x10000 ? 3 : 4
    if (bytes + size > MAX_FINDING_LABEL) break
    bytes += size
    end += ch.length
  }
  return label.slice(0, end)
}

// trivyReportImageRef renders a report's scanned image the way search
// indexes it: registry/repository:tag@digest.
export function trivyReportImageRef(resource: any): string {
  const report = resource?.report
  const repo = str(report?.artifact?.repository)
  if (!repo) return ''
  const registry = str(report?.registry?.server)
  let ref = registry ? `${registry}/${repo}` : repo
  const tag = str(report?.artifact?.tag)
  if (tag) ref += `:${tag}`
  const digest = str(report?.artifact?.digest)
  if (digest) ref += `@${digest}`
  return ref
}

// searchTrivyFindings finds reports whose findings contain term, over the
// entries server search indexes (internal/search/trivy.go): CVE IDs with
// package and versions, SBOM components, failed check IDs, secret rule IDs
// and targets, plus the scanned image. A secret finding's `match` is the
// matched line itself and is never searched. A term with a space matches
// within one finding, never across two. Rows whose name or namespace already
// contain term are skipped, since they match without a finding.
export function searchTrivyFindings(resources: readonly any[], term: string, limit = MAX_TABLE_FINDINGS): TrivyFindingsSearch {
  const matches = new Map<object, TrivyFindingMatch>()
  const re = new RegExp(term.replace(/[.*+?^${}()|[\]\\]/g, '\\$&'), 'i')
  const spaced = term.includes(' ')
  const hit = (...parts: string[]): boolean =>
    spaced ? re.test(findingLabel(...parts)) : parts.some((p) => p !== '' && re.test(p))
  let left = limit
  for (const r of resources) {
    const meta = r?.metadata
    if (re.test(str(meta?.name)) || re.test(str(meta?.namespace))) continue
    const report = r?.report
    if (!report || typeof report !== 'object') continue
    const image = trivyReportImageRef(r)
    if (image && re.test(image)) {
      matches.set(r, { site: 'image', text: image })
      continue
    }
    let found = ''
    let truncated = false
    scan: {
      for (const v of asArray(report.vulnerabilities)) {
        if (--left < 0) { truncated = true; break scan }
        const id = str(v?.vulnerabilityID)
        if (!id) continue
        const parts = [id, str(v.resource), str(v.installedVersion), '→', str(v.fixedVersion) || 'unfixed']
        if (hit(...parts)) { found = findingLabel(...parts); break scan }
      }
      for (const c of asArray(report.components?.components)) {
        if (--left < 0) { truncated = true; break scan }
        const parts = [str(c?.name), str(c?.version)]
        if (hit(...parts)) { found = findingLabel(...parts); break scan }
      }
      for (const c of asArray(report.checks)) {
        if (--left < 0) { truncated = true; break scan }
        if (c?.success !== false) continue
        if (hit(str(c.checkID))) { found = findingLabel(str(c.checkID)); break scan }
      }
      for (const sec of asArray(report.secrets)) {
        if (--left < 0) { truncated = true; break scan }
        const parts = [str(sec?.ruleID), str(sec?.target)]
        if (hit(...parts)) { found = findingLabel(...parts); break scan }
      }
    }
    if (found) matches.set(r, { site: 'finding', text: found })
    if (truncated) return { matches, truncated: true }
  }
  return { matches, truncated: false }
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

// SBOM reports carry the same report.artifact/registry shape as the scan reports.
export const getSbomReportImage = getVulnerabilityReportImage

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
