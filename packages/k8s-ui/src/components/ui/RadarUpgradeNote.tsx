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

function parseVersion(version: string | undefined): number[] | null {
  const match = /^v?(\d+)\.(\d+)\.(\d+)/.exec(version?.trim() ?? '')
  return match ? match.slice(1).map(Number) : null
}

function isNewer(a: number[], b: number[]): boolean {
  for (let i = 0; i < 3; i++) if (a[i] !== b[i]) return a[i] > b[i]
  return false
}

/**
 * Which Radar to ask for. The release that introduced the feature when it is
 * known; otherwise the latest release, which serves everything this UI calls;
 * otherwise just "a newer Radar".
 */
function target(requirement: RadarUpgradeRequirement): { since?: string; latest?: string } {
  if (requirement.minimumVersion) return { since: formatRadarVersion(requirement.minimumVersion) }
  const { latestVersion } = requirement
  const latest = parseVersion(latestVersion)
  const current = parseVersion(requirement.currentVersion)
  if (latestVersion && latest && (!current || isNewer(latest, current))) {
    return { latest: formatRadarVersion(latestVersion) }
  }
  return {}
}

// The Radar that is behind is the cluster's, not the UI the user is looking
// at, so the copy names the cluster. "It runs dev." says nothing useful, so
// only a real version is quoted.
function clusterVersion(requirement: RadarUpgradeRequirement): string | undefined {
  const current = requirement.currentVersion?.trim()
  return current && /^v?\d+\.\d+/.test(current) ? formatRadarVersion(current) : undefined
}

/** Headline for a full-page state: "Capacity needs a newer Radar on this cluster". */
export function radarUpgradeHeadline(feature: string): string {
  return `${feature} needs a newer Radar on this cluster`
}

/** Detail under that headline: "Available from Radar v1.9. This cluster runs v1.7.2." */
export function radarUpgradeDetail(requirement: RadarUpgradeRequirement): string {
  const { since, latest } = target(requirement)
  const current = clusterVersion(requirement)
  return [
    since ? `Available from Radar ${since}.` : latest ? `Available in the latest Radar, ${latest}.` : '',
    current ? `This cluster runs ${current}.` : '',
  ].filter(Boolean).join(' ')
}

/** Follows the feature name in an inline note: "need Radar v1.10 or newer on this cluster. It runs v1.7.2." */
function radarUpgradeNeed(requirement: RadarUpgradeRequirement): string {
  const { since, latest } = target(requirement)
  const current = clusterVersion(requirement)
  const need = since
    ? `need Radar ${since} or newer on this cluster.`
    : latest
      ? `need the latest Radar (${latest}) on this cluster.`
      : 'need a newer Radar on this cluster.'
  return current ? `${need} It runs ${current}.` : need
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
        {radarUpgradeNeed(requirement)}
        {' '}
        <RadarUpgradeAction requirement={requirement} />
      </p>
    </div>
  )
}
