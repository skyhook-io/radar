import { describe, expect, it } from 'vitest'
import { IN_CLUSTER_UPGRADE_URL, isMinorOrMajorUpdate, parseMajorMinor, versionUpdateURL } from './version'

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

  it('routes in-cluster upgrades to instructions instead of release notes', () => {
    const releaseURL = 'https://github.com/skyhook-io/radar/releases/tag/v1.2.4'
    expect(versionUpdateURL('in-cluster', releaseURL)).toBe(IN_CLUSTER_UPGRADE_URL)
    expect(versionUpdateURL('local', releaseURL)).toBe(releaseURL)
  })
})
