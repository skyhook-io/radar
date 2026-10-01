import { createContext, useContext } from 'react'
import { clsx } from 'clsx'
import { ArrowUpCircle, ExternalLink } from 'lucide-react'
import type { RadarUpgradeRequirement } from '../../types/fetch-error'

export interface RadarUpgradeActions {
  /** Hands the upgrade to the host, e.g. Radar Cloud's role-aware upgrade flow. */
  onRequestUpgrade?: (requirement: RadarUpgradeRequirement) => void
  /** Used when there is no handler: a page with upgrade instructions. */
  upgradeHref?: string
}

export const RadarUpgradeContext = createContext<RadarUpgradeActions>({})

/** "v1.10.0" → "v1.10", "1.7.2" → "v1.7.2". Anything else is shown as given. */
export function formatRadarVersion(version: string): string {
  const match = /^v?(\d+)\.(\d+)\.(\d+)$/.exec(version.trim())
  if (!match) return version
  const [, major, minor, patch] = match
  return patch === '0' ? `v${major}.${minor}` : `v${major}.${minor}.${patch}`
}

// "You're on dev." says nothing useful, so only a real version is quoted.
function withCurrentVersion(sentence: string, requirement: RadarUpgradeRequirement): string {
  const current = requirement.currentVersion?.trim()
  return current && /^v?\d+\.\d+/.test(current)
    ? `${sentence} You're on ${formatRadarVersion(current)}.`
    : sentence
}

/** Detail under a "needs a newer Radar" headline: "Available from Radar v1.9. You're on v1.7.2." */
export function radarUpgradeDetail(requirement: RadarUpgradeRequirement): string {
  return withCurrentVersion(`Available from Radar ${formatRadarVersion(requirement.minimumVersion)}.`, requirement)
}

/** The upgrade call to action, or nothing when the host offers no way to upgrade. */
export function RadarUpgradeAction({ requirement }: { requirement: RadarUpgradeRequirement }) {
  const { onRequestUpgrade, upgradeHref } = useContext(RadarUpgradeContext)
  const className = 'inline-flex items-center gap-1 font-medium text-accent-text hover:underline'
  if (onRequestUpgrade) {
    return (
      <button type="button" className={className} onClick={() => onRequestUpgrade(requirement)}>
        Upgrade Radar
      </button>
    )
  }
  if (upgradeHref) {
    return (
      <a href={upgradeHref} target="_blank" rel="noopener noreferrer" className={className}>
        How to upgrade
        <ExternalLink className="h-3 w-3" aria-hidden />
      </a>
    )
  }
  return null
}

/**
 * Inline note for a feature the connected Radar is too old to serve. Calm on
 * purpose: nothing failed, the cluster just runs an older Radar than this UI.
 */
export function RadarUpgradeNote({
  requirement,
  className,
}: {
  requirement: RadarUpgradeRequirement
  className?: string
}) {
  return (
    <div className={clsx('flex items-start gap-2 text-xs text-theme-text-secondary', className)}>
      <ArrowUpCircle className="mt-px h-3.5 w-3.5 shrink-0 text-accent-text" aria-hidden />
      <p className="min-w-0">
        <span className="font-medium text-theme-text-primary">{requirement.feature}</span>
        {' '}
        {withCurrentVersion(`need Radar ${formatRadarVersion(requirement.minimumVersion)} or newer.`, requirement)}
        {' '}
        <RadarUpgradeAction requirement={requirement} />
      </p>
    </div>
  )
}
