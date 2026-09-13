import { describe, expect, test } from 'vitest'

import { RADAR_HEALTH_NOTE, radarHealthNote } from './health-provenance'
import { APP_TREE_HEALTH_NOTICE, REMOTE_DESTINATION_NOTICE, healthSourceNoticeKind } from './GitOpsHealthSourceNotice'

describe('radarHealthNote', () => {
  test('marks only Radar-sourced problems', () => {
    expect(radarHealthNote({ health: 'Degraded', healthSource: 'radar' })).toBe(RADAR_HEALTH_NOTE)
    expect(radarHealthNote({ health: 'Missing', healthSource: 'radar', healthMessage: 'not in cluster' })).toBe(`${RADAR_HEALTH_NOTE} not in cluster`)
  })
  test('is silent for the controller, for healthy values, and for unknown sources', () => {
    expect(radarHealthNote({ health: 'Degraded', healthSource: 'controller' })).toBe('')
    expect(radarHealthNote({ health: 'Healthy', healthSource: 'radar' })).toBe('')
    expect(radarHealthNote({ health: 'Degraded' })).toBe('')
    expect(radarHealthNote({ health: 'Degraded', healthSource: 'something-newer' })).toBe('')
  })
  test('copy stays in plain words', () => {
    for (const copy of [RADAR_HEALTH_NOTE, APP_TREE_HEALTH_NOTICE, REMOTE_DESTINATION_NOTICE]) {
      expect(copy).not.toMatch(/appTree|resourceHealthSource|persist|Tier|overlay/i)
    }
  })
})

describe('healthSourceNoticeKind', () => {
  test('remote destination wins over appTree, Flux never notices', () => {
    expect(healthSourceNoticeKind({ tool: 'argocd', resourceHealthMode: 'appTree', remoteDestination: true })).toBe('remote')
    expect(healthSourceNoticeKind({ tool: 'argocd', resourceHealthMode: 'appTree', health: 'Degraded' })).toBe('appTree')
    expect(healthSourceNoticeKind({ tool: 'argocd', resourceHealthMode: 'appTree' })).toBe('appTree')
    expect(healthSourceNoticeKind({ tool: 'argocd', resourceHealthMode: 'appTree', health: 'Healthy' })).toBeNull()
    expect(healthSourceNoticeKind({ tool: 'argocd', resourceHealthMode: 'inline' })).toBeNull()
    expect(healthSourceNoticeKind({ tool: 'argocd' })).toBeNull()
    expect(healthSourceNoticeKind({ tool: 'fluxcd', resourceHealthMode: 'appTree' })).toBeNull()
    expect(healthSourceNoticeKind(undefined)).toBeNull()
  })
})
