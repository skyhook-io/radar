import { describe, expect, it } from 'vitest'
import { renderToString } from 'react-dom/server'
import { formatRadarVersion, radarUpgradeDetail, radarUpgradeHeadline, RadarUpgradeNote } from './RadarUpgradeNote'
import { getRadarUpgradeRequirement } from '../../types/fetch-error'

describe('radarUpgradeHeadline', () => {
  it('says the cluster is behind, not the UI', () => {
    expect(radarUpgradeHeadline('Capacity')).toBe('Capacity needs a newer Radar on this cluster')
  })
})

describe('radarUpgradeDetail', () => {
  it('quotes the minimum and a real current version', () => {
    expect(radarUpgradeDetail({ feature: 'Policy results', minimumVersion: 'v1.10.0', currentVersion: '1.7.2' }))
      .toBe('Available from Radar v1.10. This cluster runs v1.7.2.')
  })

  it('leaves out a current version that is not a version', () => {
    expect(radarUpgradeDetail({ feature: 'Policy results', minimumVersion: 'v1.10.0', currentVersion: 'dev' }))
      .toBe('Available from Radar v1.10.')
    expect(radarUpgradeDetail({ feature: 'Policy results', minimumVersion: 'v1.10.0' }))
      .toBe('Available from Radar v1.10.')
  })
})

describe('radarUpgradeDetail without a known first release', () => {
  const unknownSince = { feature: 'Drift alerts', currentVersion: 'v1.7.2' }

  it('asks for the latest release when it is newer than the running one', () => {
    expect(radarUpgradeDetail({ ...unknownSince, latestVersion: '1.16.0' }))
      .toBe('Available in the latest Radar, v1.16. This cluster runs v1.7.2.')
  })

  it('only names the running version when no target release is known', () => {
    expect(radarUpgradeDetail(unknownSince)).toBe('This cluster runs v1.7.2.')
    expect(radarUpgradeDetail({ ...unknownSince, currentVersion: 'v1.16.0', latestVersion: 'v1.16.0' }))
      .toBe('This cluster runs v1.16.')
  })

  it('reads the same way inline', () => {
    const html = renderToString(<RadarUpgradeNote requirement={{ ...unknownSince, latestVersion: 'v1.16.0' }} />)
    expect(html).toContain('need the latest Radar (v1.16) on this cluster. It runs v1.7.2.')
  })
})

describe('formatRadarVersion', () => {
  it('drops a zero patch and adds the v', () => {
    expect(formatRadarVersion('v1.14.0')).toBe('v1.14')
    expect(formatRadarVersion('1.7.2')).toBe('v1.7.2')
    expect(formatRadarVersion('v1.7.2-dirty')).toBe('v1.7.2-dirty')
  })
})

describe('getRadarUpgradeRequirement', () => {
  it('reads the requirement off any error that carries one', () => {
    const error = Object.assign(new Error('x'), { radarUpgrade: { feature: 'Drain plans', minimumVersion: 'v1.14.0' } })
    expect(getRadarUpgradeRequirement(error)).toEqual({ feature: 'Drain plans', minimumVersion: 'v1.14.0' })
  })

  it('ignores plain errors and malformed requirements', () => {
    expect(getRadarUpgradeRequirement(new Error('HTTP 404 (Not Found)'))).toBeNull()
    expect(getRadarUpgradeRequirement({ radarUpgrade: { minimumVersion: 'v1.9.0' } })).toBeNull()
    expect(getRadarUpgradeRequirement({ radarUpgrade: { feature: 'x', minimumVersion: 9 } })).toBeNull()
    expect(getRadarUpgradeRequirement(null)).toBeNull()
  })
})
