import { describe, expect, it } from 'vitest'
import {
  getVersionUpdateStatus,
  IN_CLUSTER_UPGRADE_URL,
  isMinorOrMajorUpdate,
  parseMajorMinor,
  versionUpdateURL,
} from './version'

describe('Radar version helpers', () => {
  it('parses major and minor versions with an optional v prefix', () => {
    expect(parseMajorMinor('v1.12.3')).toEqual({ major: 1, minor: 12 })
    expect(parseMajorMinor('dev')).toBeNull()
  })

  it('distinguishes patch updates from minor and major updates', () => {
    expect(isMinorOrMajorUpdate('1.2.3', '1.2.4')).toBe(false)
    expect(isMinorOrMajorUpdate('1.2.3', '1.3.0')).toBe(true)
    expect(isMinorOrMajorUpdate('1.2.3', '2.0.0')).toBe(true)
  })

  it('grades available updates by distance from the latest release', () => {
    expect(getVersionUpdateStatus('1.12.0', '1.12.1')).toEqual({ tier: 'patch' })
    expect(getVersionUpdateStatus('1.11.4', '1.12.1')).toEqual({ tier: 'minor', minorVersionsBehind: 1 })
    expect(getVersionUpdateStatus('1.10.4', '1.12.1')).toEqual({ tier: 'minor', minorVersionsBehind: 2 })
    expect(getVersionUpdateStatus('1.9.4', '1.12.1')).toEqual({ tier: 'stale', minorVersionsBehind: 3 })
    expect(getVersionUpdateStatus('0.12.4', '1.12.1')).toEqual({ tier: 'stale', majorVersionBehind: true })
  })

  it('does not flag invalid, current, or newer local builds', () => {
    expect(getVersionUpdateStatus('dev', '1.12.1')).toEqual({ tier: 'none' })
    expect(getVersionUpdateStatus('1.12.1', '1.12.1')).toEqual({ tier: 'none' })
    expect(getVersionUpdateStatus('1.13.0', '1.12.1')).toEqual({ tier: 'none' })
    expect(getVersionUpdateStatus('2.0.0', '1.12.1')).toEqual({ tier: 'none' })
  })

  it('routes in-cluster upgrades to instructions instead of release notes', () => {
    const releaseURL = 'https://github.com/skyhook-io/radar/releases/tag/v1.2.4'
    expect(versionUpdateURL('in-cluster', releaseURL)).toBe(IN_CLUSTER_UPGRADE_URL)
    expect(versionUpdateURL('local', releaseURL)).toBe(releaseURL)
  })
})
