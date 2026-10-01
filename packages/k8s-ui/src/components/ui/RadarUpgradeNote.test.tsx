import { describe, expect, it } from 'vitest'
import { formatRadarVersion, radarUpgradeDetail } from './RadarUpgradeNote'
import { getRadarUpgradeRequirement } from '../../types/fetch-error'

describe('radarUpgradeDetail', () => {
  it('quotes the minimum and a real current version', () => {
    expect(radarUpgradeDetail({ feature: 'Policy results', minimumVersion: 'v1.10.0', currentVersion: '1.7.2' }))
      .toBe("Available from Radar v1.10. You're on v1.7.2.")
  })

  it('leaves out a current version that is not a version', () => {
    expect(radarUpgradeDetail({ feature: 'Policy results', minimumVersion: 'v1.10.0', currentVersion: 'dev' }))
      .toBe('Available from Radar v1.10.')
    expect(radarUpgradeDetail({ feature: 'Policy results', minimumVersion: 'v1.10.0' }))
      .toBe('Available from Radar v1.10.')
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
    expect(getRadarUpgradeRequirement({ radarUpgrade: { feature: 'x' } })).toBeNull()
    expect(getRadarUpgradeRequirement(null)).toBeNull()
  })
})
