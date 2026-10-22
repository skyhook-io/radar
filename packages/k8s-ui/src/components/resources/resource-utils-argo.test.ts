import { describe, expect, it } from 'vitest'
import { argoResumeSettings, getArgoApplicationStatus, isArgoAutoSyncEnabled, isArgoAutoSyncSwitchedOff, isArgoOperationInProgress } from './resource-utils-argo'

describe('getArgoApplicationStatus', () => {
  // A Suspended Argo app is intentionally paused — neutral (sky), matching the
  // backend rollup (mapArgoHealth) + the GitOps badge, so it doesn't read amber
  // on the resource table while reading Idle in Applications.
  it('maps health Suspended to neutral (sky), not degraded', () => {
    const badge = getArgoApplicationStatus({ status: { health: { status: 'Suspended' }, sync: { status: 'Synced' } } })
    expect(badge.level).toBe('neutral')
    expect(badge.text).toBe('Suspended')
  })

  it('still maps a healthy synced app to healthy', () => {
    const badge = getArgoApplicationStatus({ status: { health: { status: 'Healthy' }, sync: { status: 'Synced' } } })
    expect(badge.level).toBe('healthy')
  })

  it('still maps a degraded app to unhealthy', () => {
    const badge = getArgoApplicationStatus({ status: { health: { status: 'Degraded' }, sync: { status: 'Synced' } } })
    expect(badge.level).toBe('unhealthy')
  })

  // Radar's annotations plus automation switched off in place still read as
  // suspended; the same annotations on an app that auto-syncs again do not.
  it('treats a Radar-suspended app with automated.enabled=false as suspended', () => {
    const annotations = { 'radarhq.io/suspended-prune': 'true' }
    const off = { metadata: { annotations }, spec: { syncPolicy: { automated: { enabled: false } } }, status: {} }
    const on = { metadata: { annotations }, spec: { syncPolicy: { automated: { prune: true } } }, status: {} }
    expect(getArgoApplicationStatus(off).text).toBe('Suspended')
    expect(getArgoApplicationStatus(on).text).not.toBe('Suspended')
  })

  it('surfaces an operation being terminated', () => {
    const badge = getArgoApplicationStatus({ status: { operationState: { phase: 'Terminating' } } })
    expect(badge.level).toBe('degraded')
    expect(badge.text).toBe('Terminating')
  })
})

describe('isArgoOperationInProgress', () => {
  it.each([
    [{ status: { operationState: { phase: 'Running' } } }, true],
    [{ status: { operationState: { phase: 'Terminating' } } }, true],
    [{ operation: { sync: {} }, status: { operationState: { phase: 'Succeeded' } } }, true],
    [{ status: { operationState: { phase: 'Succeeded' } } }, false],
  ])('maps %o to %s', (app, expected) => {
    expect(isArgoOperationInProgress(app)).toBe(expected)
  })
})

// Mirrors gitops.ArgoAutoSyncEnabled and Argo's SyncPolicy.IsAutomatedSyncEnabled:
// Argo CD 3.x keeps the automated block (and its prune/selfHeal) when
// automation is switched off with enabled: false.
describe('isArgoAutoSyncEnabled', () => {
  const app = (syncPolicy: unknown) => ({ spec: { syncPolicy } })
  it.each([
    ['no sync policy', undefined, false],
    ['no automated block', {}, false],
    ['automated: null', { automated: null }, false],
    ['empty automated block', { automated: {} }, true],
    ['automated with prune/selfHeal', { automated: { prune: true, selfHeal: true } }, true],
    ['enabled: true', { automated: { enabled: true } }, true],
    ['enabled: false', { automated: { enabled: false, prune: true, selfHeal: true } }, false],
  ])('%s', (_label, syncPolicy, expected) => {
    expect(isArgoAutoSyncEnabled(app(syncPolicy))).toBe(expected)
  })

  it('tolerates a missing resource', () => {
    expect(isArgoAutoSyncEnabled(undefined)).toBe(false)
  })
})

describe('isArgoAutoSyncSwitchedOff', () => {
  const app = (syncPolicy: unknown) => ({ spec: { syncPolicy } })
  // Only an automated block kept with enabled: false; a Manual app has none.
  it.each([
    ['no automated block', {}, false],
    ['automated: null', { automated: null }, false],
    ['automated on', { automated: { prune: true } }, false],
    ['enabled: true', { automated: { enabled: true } }, false],
    ['enabled: false', { automated: { enabled: false, prune: true } }, true],
  ])('%s', (_label, syncPolicy, expected) => {
    expect(isArgoAutoSyncSwitchedOff(app(syncPolicy))).toBe(expected)
  })

  it('tolerates a missing resource', () => {
    expect(isArgoAutoSyncSwitchedOff(undefined)).toBe(false)
  })
})

describe('argoResumeSettings', () => {
  const app = (syncPolicy: unknown) => ({ spec: { syncPolicy } })
  it('keeps a block switched off in place, whatever the Radar', () => {
    expect(argoResumeSettings(app({ automated: { enabled: false, prune: true } }), undefined)).toEqual({ prune: true, selfHeal: false })
  })
  it.each([
    ['a Radar with the new default', true, { prune: false, selfHeal: true }],
    ['an older Radar', false, { prune: true, selfHeal: true }],
    ['an unknown Radar', undefined, null],
  ])('without a block, follows %s', (_label, serverStartsPruneOff, expected) => {
    expect(argoResumeSettings(app({}), serverStartsPruneOff)).toEqual(expected)
  })
})
