import { AlertBanner } from '../drawer-components'

// Shared severity color maps for all Trivy report renderers

export const SEVERITY_BADGE_COLORS: Record<string, string> = {
  CRITICAL: 'bg-red-500/20 text-red-400',
  HIGH: 'bg-orange-500/20 text-orange-400',
  MEDIUM: 'bg-yellow-500/20 text-yellow-400',
  LOW: 'bg-blue-500/20 text-blue-400',
  UNKNOWN: 'bg-gray-500/20 text-gray-400',
}

export const SEVERITY_BAR_COLORS: Record<string, string> = {
  CRITICAL: 'bg-red-500',
  HIGH: 'bg-orange-500',
  MEDIUM: 'bg-yellow-500',
  LOW: 'bg-blue-500',
  UNKNOWN: 'bg-gray-500',
}

export const SEVERITY_TEXT_COLORS: Record<string, string> = {
  CRITICAL: 'text-red-400',
  HIGH: 'text-orange-400',
  MEDIUM: 'text-yellow-400',
  LOW: 'text-blue-400',
  UNKNOWN: 'text-gray-400',
}

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
