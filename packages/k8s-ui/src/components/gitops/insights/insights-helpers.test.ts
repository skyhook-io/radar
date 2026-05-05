import { describe, it, expect } from 'vitest'
import { SEVERITY_DOT } from '../../../utils/badge-colors'
import { compactSource, entryTone, gitopsToSeverity, messageToPhase, phaseToTone } from './insights-helpers'

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
