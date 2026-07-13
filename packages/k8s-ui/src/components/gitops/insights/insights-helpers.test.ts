import { describe, it, expect } from 'vitest'
import { SEVERITY_DOT } from '../../../utils/badge-colors'
import type { GitOpsChange } from '../../../types'
import {
  changeMatchesFacets,
  changeMatchesSearch,
  compactSource,
  entryTone,
  gitopsToSeverity,
  healthStatusRank,
  isArgoResourceSyncEligible,
  messageToPhase,
  phaseToTone,
  resourceStatusCounts,
  syncStatusRank,
} from './insights-helpers'

function change(partial: Partial<GitOpsChange> & { ref: GitOpsChange['ref'] }): GitOpsChange {
  return { category: 'Unknown', hasDesired: true, hasLive: true, partial: false, ...partial }
}

describe('gitopsToSeverity', () => {
  it.each([
    ['critical', 'error'],
    ['Failed', 'error'],
    ['UpgradeFailed', 'error'],
    ['alert', 'alert'],
    ['warning', 'warning'],
    ['Terminating', 'warning'],
    ['Pending', 'warning'],
    ['info', 'info'],
    ['Progressing', 'info'],
    ['Reconciling', 'info'],
    ['Succeeded', 'success'],
    ['Healthy', 'success'],
    ['', 'neutral'],
    [undefined, 'neutral'],
    ['mystery-phase', 'neutral'],
  ] as const)('%s → %s', (input, expected) => {
    expect(gitopsToSeverity(input)).toBe(expected)
  })
})

describe('phaseToTone', () => {
  it('returns SEVERITY_DOT class for known phases', () => {
    expect(phaseToTone('Succeeded')).toBe(SEVERITY_DOT.success)
    expect(phaseToTone('Failed')).toBe(SEVERITY_DOT.error)
    expect(phaseToTone('Progressing')).toBe(SEVERITY_DOT.info)
    expect(phaseToTone('Pending')).toBe(SEVERITY_DOT.warning)
  })
  it('returns null when phase has no meaningful signal', () => {
    expect(phaseToTone(undefined)).toBeNull()
    expect(phaseToTone('')).toBeNull()
    expect(phaseToTone('mystery')).toBeNull()
  })
})

describe('messageToPhase', () => {
  it('detects success language', () => {
    expect(messageToPhase('Application was synced successfully')).toBe('succeeded')
    expect(messageToPhase('reconcile succeeded')).toBe('succeeded')
  })
  it('detects failure language', () => {
    expect(messageToPhase('reconciliation failed: context deadline')).toBe('failed')
    expect(messageToPhase('Helm upgrade error')).toBe('failed')
  })
  it('detects in-flight language', () => {
    expect(messageToPhase('progressing toward target state')).toBe('progressing')
    expect(messageToPhase('still reconciling')).toBe('progressing')
  })
  it('returns undefined when nothing matches', () => {
    expect(messageToPhase(undefined)).toBeUndefined()
    expect(messageToPhase('')).toBeUndefined()
    expect(messageToPhase('plain note')).toBeUndefined()
  })
})

describe('entryTone', () => {
  it('uses explicit phase when present', () => {
    const tone = entryTone({ phase: 'Succeeded' })
    expect(tone.dot).toBe(SEVERITY_DOT.success)
    expect(tone.inferredFrom).toBeUndefined()
  })
  it('falls back to message inference when phase is missing', () => {
    const tone = entryTone({ message: 'reconciliation failed' })
    expect(tone.dot).toBe(SEVERITY_DOT.error)
    expect(tone.inferredFrom).toBe('inferred from message')
  })
  it('returns neutral when neither phase nor message carries signal', () => {
    const tone = entryTone({})
    expect(tone.dot).toBe(SEVERITY_DOT.neutral)
    expect(tone.inferredFrom).toBe('no phase information')
  })
})

describe('compactSource', () => {
  it('strips https://github.com/ prefix and collapses deep paths', () => {
    const got = compactSource('https://github.com/KoalaOps/deployment · argocd/addons/karpenter/default-nodepool/overlays/nonprod-cluster-us-east1')
    expect(got).toBe('KoalaOps/deployment · argocd/…/nonprod-cluster-us-east1')
  })
  it('keeps short paths intact', () => {
    expect(compactSource('https://github.com/org/repo · charts/foo')).toBe('org/repo · charts/foo')
  })
  it('handles trailing slash and no path', () => {
    expect(compactSource('https://github.com/org/repo/')).toBe('org/repo')
    expect(compactSource('https://github.com/org/repo')).toBe('org/repo')
  })
  it('strips http and www prefixes', () => {
    expect(compactSource('http://www.github.com/org/repo')).toBe('org/repo')
  })
  it('returns empty string for missing input', () => {
    expect(compactSource(undefined)).toBe('')
    expect(compactSource('')).toBe('')
  })
})

describe('changeMatchesSearch', () => {
  const c = change({ ref: { kind: 'Deployment', name: 'guestbook-ui', namespace: 'demo', group: 'apps' } })
  it('matches on kind, name, namespace, and group (case-insensitive)', () => {
    expect(changeMatchesSearch(c, 'deploy')).toBe(true)
    expect(changeMatchesSearch(c, 'GUESTBOOK')).toBe(true)
    expect(changeMatchesSearch(c, 'demo')).toBe(true)
    expect(changeMatchesSearch(c, 'apps')).toBe(true)
  })
  it('empty/whitespace query matches everything', () => {
    expect(changeMatchesSearch(c, '')).toBe(true)
    expect(changeMatchesSearch(c, '   ')).toBe(true)
  })
  it('non-matching query is filtered out', () => {
    expect(changeMatchesSearch(c, 'nomatch')).toBe(false)
  })
})

describe('changeMatchesFacets', () => {
  const outOfSync = change({ ref: { kind: 'Service', name: 'a' }, sync: 'OutOfSync', health: 'Healthy' })
  const degraded = change({ ref: { kind: 'Deployment', name: 'b' }, sync: 'Synced', health: 'Degraded' })
  const missing = change({ ref: { kind: 'Secret', name: 'c' }, sync: 'OutOfSync', health: 'Missing' })
  const healthy = change({ ref: { kind: 'ConfigMap', name: 'd' }, sync: 'Synced', health: 'Healthy' })

  it('empty facet set matches everything', () => {
    const none = new Set<never>()
    expect(changeMatchesFacets(outOfSync, none)).toBe(true)
    expect(changeMatchesFacets(healthy, none)).toBe(true)
  })
  it('outOfSync keys off sync status', () => {
    const f = new Set(['outOfSync'] as const)
    expect(changeMatchesFacets(outOfSync, f)).toBe(true)
    expect(changeMatchesFacets(degraded, f)).toBe(false)
  })
  it('degraded/missing key off health status', () => {
    expect(changeMatchesFacets(degraded, new Set(['degraded'] as const))).toBe(true)
    expect(changeMatchesFacets(missing, new Set(['missing'] as const))).toBe(true)
    expect(changeMatchesFacets(healthy, new Set(['degraded'] as const))).toBe(false)
  })
  it('multiple facets union (OR)', () => {
    const f = new Set(['degraded', 'missing'] as const)
    expect(changeMatchesFacets(degraded, f)).toBe(true)
    expect(changeMatchesFacets(missing, f)).toBe(true)
    expect(changeMatchesFacets(outOfSync, f)).toBe(false)
  })
})

describe('resourceStatusCounts', () => {
  it('counts each facet independently (a resource can be both OutOfSync and Missing)', () => {
    const counts = resourceStatusCounts([
      change({ ref: { kind: 'Service', name: 'a' }, sync: 'OutOfSync', health: 'Healthy' }),
      change({ ref: { kind: 'Secret', name: 'c' }, sync: 'OutOfSync', health: 'Missing' }),
      change({ ref: { kind: 'Deployment', name: 'b' }, sync: 'Synced', health: 'Degraded' }),
      change({ ref: { kind: 'ConfigMap', name: 'd' }, sync: 'Synced', health: 'Healthy' }),
    ])
    expect(counts).toEqual({ outOfSync: 2, degraded: 1, missing: 1 })
  })
})

describe('isArgoResourceSyncEligible', () => {
  const outOfSync = change({ ref: { group: 'apps', kind: 'Deployment', namespace: 'demo', name: 'web' }, sync: 'OutOfSync' })

  it('allows declared OutOfSync and Missing resources', () => {
    expect(isArgoResourceSyncEligible(outOfSync, true)).toBe(true)
    expect(isArgoResourceSyncEligible(change({ ref: { kind: 'Service', name: 'missing' }, health: 'Missing' }), true)).toBe(true)
  })

  it('excludes generated, hook, and already-synced resources', () => {
    expect(isArgoResourceSyncEligible(outOfSync, false)).toBe(false)
    expect(isArgoResourceSyncEligible(outOfSync, true, 'PreSync')).toBe(false)
    expect(isArgoResourceSyncEligible(change({ ref: { kind: 'Service', name: 'ready' }, sync: 'Synced', health: 'Healthy' }), true)).toBe(false)
  })
})

describe('syncStatusRank / healthStatusRank', () => {
  it('ascending sync rank surfaces OutOfSync before Synced', () => {
    expect(syncStatusRank('OutOfSync')).toBeLessThan(syncStatusRank('Synced'))
    expect(syncStatusRank('OutOfSync')).toBeLessThan(syncStatusRank('Unknown'))
  })
  it('ascending health rank surfaces Missing/Degraded before Healthy', () => {
    expect(healthStatusRank('Missing')).toBeLessThan(healthStatusRank('Degraded'))
    expect(healthStatusRank('Degraded')).toBeLessThan(healthStatusRank('Healthy'))
  })
})
