import { describe, expect, test } from 'vitest'

import { RADAR_HEALTH_NOTE, radarHealthNote } from './health-provenance'
import { APP_TREE_API_ERROR_NOTICE, APP_TREE_HEALTH_NOTICE, APP_TREE_NO_FINDINGS_NOTICE, APP_TREE_PERSIST_REMEDY, REMOTE_DESTINATION_NOTICE, REMOTE_FLUX_TARGET_NOTICE, hasRadarFinding, healthSourceNoticeKind } from './GitOpsHealthSourceNotice'

describe('radarHealthNote', () => {
  test('marks only Radar-sourced problems', () => {
    expect(radarHealthNote({ health: 'Degraded', healthSource: 'radar' })).toBe(RADAR_HEALTH_NOTE)
    expect(radarHealthNote({ health: 'Missing', healthSource: 'radar', healthMessage: 'not in cluster' })).toBe(`${RADAR_HEALTH_NOTE} not in cluster`)
  })
  test('is silent for the controller, for healthy values, and for unknown sources', () => {
    expect(radarHealthNote({ health: 'Degraded', healthSource: 'controller' })).toBe('')
    expect(radarHealthNote({ health: 'Degraded', healthSource: 'controllerApi' })).toBe('')
    expect(radarHealthNote({ health: 'Healthy', healthSource: 'radar' })).toBe('')
    expect(radarHealthNote({ health: 'Degraded' })).toBe('')
    expect(radarHealthNote({ health: 'Degraded', healthSource: 'something-newer' })).toBe('')
  })
  test('copy stays in plain words', () => {
    for (const copy of [RADAR_HEALTH_NOTE, APP_TREE_HEALTH_NOTICE, REMOTE_DESTINATION_NOTICE, REMOTE_FLUX_TARGET_NOTICE]) {
      expect(copy).not.toMatch(/appTree|resourceHealthSource|persist|Tier|overlay/i)
    }
    // The remedy deliberately names the one Argo knob the user can turn;
    // it must still avoid Radar's internal vocabulary. Radar's own Settings
    // are the host's to name — an embedded host may have no such place.
    for (const copy of [APP_TREE_NO_FINDINGS_NOTICE, APP_TREE_PERSIST_REMEDY, APP_TREE_API_ERROR_NOTICE]) {
      expect(copy).not.toMatch(/appTree|Tier|overlay|Settings/i)
    }
    expect(APP_TREE_PERSIST_REMEDY).toContain('controller.resource.health.persist')
  })

  test('hasRadarFinding reads the rows the markers read', () => {
    const row = (health: string, healthSource?: string) =>
      ({ ref: { kind: 'Deployment', namespace: 'p', name: 'w' }, category: 'Unknown', health, healthSource, hasDesired: false, hasLive: true }) as const
    expect(hasRadarFinding(undefined)).toBe(false)
    expect(hasRadarFinding([row('Degraded', 'controller')])).toBe(false)
    expect(hasRadarFinding([row('Healthy', 'radar')])).toBe(false)
    expect(hasRadarFinding([row('Degraded', 'radar')])).toBe(true)
    expect(hasRadarFinding([row('Missing', 'radar')])).toBe(true)
  })
})

describe('healthSourceNoticeKind', () => {
  test('remote destination wins over appTree; Flux notices only a remote target', () => {
    expect(healthSourceNoticeKind({ tool: 'argocd', resourceHealthMode: 'appTree', remoteDestination: true })).toBe('remote')
    expect(healthSourceNoticeKind({ tool: 'argocd', resourceHealthMode: 'appTree', health: 'Degraded' })).toBe('appTree')
    expect(healthSourceNoticeKind({ tool: 'argocd', resourceHealthMode: 'appTree' })).toBe('appTree')
    expect(healthSourceNoticeKind({ tool: 'argocd', resourceHealthMode: 'appTree', health: 'Healthy' })).toBeNull()
    expect(healthSourceNoticeKind({ tool: 'argocd', resourceHealthMode: 'appTree', health: 'Degraded', resourceHealthFromApi: true })).toBeNull()
    expect(healthSourceNoticeKind({ tool: 'argocd', resourceHealthMode: 'inline' })).toBeNull()
    expect(healthSourceNoticeKind({ tool: 'argocd' })).toBeNull()
    expect(healthSourceNoticeKind({ tool: 'fluxcd', resourceHealthMode: 'appTree' })).toBeNull()
    expect(healthSourceNoticeKind({ tool: 'fluxcd', remoteDestination: true })).toBe('remote')
    expect(healthSourceNoticeKind(undefined)).toBeNull()
  })
})
