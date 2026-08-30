import type { DeploymentMode } from '../types'

export interface MajorMinorVersion {
  major: number
  minor: number
}

export type VersionUpdateTier = 'none' | 'patch' | 'minor' | 'stale'

export interface VersionUpdateStatus {
  tier: VersionUpdateTier
  minorVersionsBehind?: number
  majorVersionBehind?: boolean
}

export const IN_CLUSTER_UPGRADE_URL = 'https://radarhq.io/docs/configuration/in-cluster'

export function versionUpdateURL(deploymentMode: DeploymentMode, releaseURL?: string): string | undefined {
  return deploymentMode === 'in-cluster' ? IN_CLUSTER_UPGRADE_URL : releaseURL
}

export function parseMajorMinor(version: string): MajorMinorVersion | null {
  const match = /^v?(\d+)\.(\d+)/.exec(version.trim())
  if (!match) return null
  return { major: Number(match[1]), minor: Number(match[2]) }
}

function parseVersion(version: string): [major: number, minor: number, patch: number, prerelease: boolean] | null {
  const match = /^v?(\d+)\.(\d+)\.(\d+)(?:-([0-9A-Za-z.-]+))?(?:\+[0-9A-Za-z.-]+)?$/.exec(version.trim())
  if (!match) return null
  return [Number(match[1]), Number(match[2]), Number(match[3]), !!match[4]]
}

export function getVersionUpdateStatus(current: string, latest?: string): VersionUpdateStatus {
  if (!latest) return { tier: 'none' }

  const currentVersion = parseVersion(current)
  const latestVersion = parseVersion(latest)
  if (!currentVersion || !latestVersion) return { tier: 'none' }

  const [currentMajor, currentMinor, currentPatch, currentPrerelease] = currentVersion
  const [latestMajor, latestMinor, latestPatch, latestPrerelease] = latestVersion

  if (latestMajor < currentMajor) return { tier: 'none' }
  if (latestMajor > currentMajor) return { tier: 'stale', majorVersionBehind: true }

  const minorVersionsBehind = latestMinor - currentMinor
  if (minorVersionsBehind >= 3) return { tier: 'stale', minorVersionsBehind }
  if (minorVersionsBehind > 0) return { tier: 'minor', minorVersionsBehind }
  if (minorVersionsBehind < 0 || latestPatch < currentPatch) return { tier: 'none' }
  if (latestPatch === currentPatch) {
    return currentPrerelease && !latestPrerelease ? { tier: 'patch' } : { tier: 'none' }
  }
  return { tier: 'patch' }
}
