import { AlertBanner } from '../../ui/drawer-components'
import {
  VULN_SEVERITY_BADGE,
  VULN_SEVERITY_BAR,
  VULN_SEVERITY_TEXT,
} from '../../../utils/badge-colors'

// Re-export from centralized badge-colors under the legacy names used by Trivy renderers
export const SEVERITY_BADGE_COLORS = VULN_SEVERITY_BADGE
export const SEVERITY_BAR_COLORS = VULN_SEVERITY_BAR
export const SEVERITY_TEXT_COLORS = VULN_SEVERITY_TEXT

export const SEVERITY_ORDER: Record<string, number> = {
  CRITICAL: 0,
  HIGH: 1,
  MEDIUM: 2,
  LOW: 3,
  UNKNOWN: 4,
}

/** Build a full image string from a Trivy report's artifact and registry fields. */
export function formatTrivyImage(report: any): string {
  const artifact = report.artifact || {}
  const registry = report.registry?.server || ''
  if (!artifact.repository) return '-'
  const repo = registry ? `${registry}/${artifact.repository}` : artifact.repository
  return artifact.tag ? `${repo}:${artifact.tag}` : repo
}

// Reusable alert banner shown at top of Trivy report renderers
export function TrivyAlertBanner({ critical, high, noun }: { critical: number; high: number; noun: string }) {
  if (critical > 0) {
    return (
      <AlertBanner
        variant="error"
        title={`${critical} critical ${noun}${critical !== 1 ? 's' : ''}`}
        message="Critical issues should be addressed immediately."
      />
    )
  }
  if (high > 0) {
    return (
      <AlertBanner
        variant="warning"
        title={`${high} high-severity ${noun}${high !== 1 ? 's' : ''}`}
        message="Consider addressing these issues to improve security posture."
      />
    )
  }
  return null
}
