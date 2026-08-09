import { describe, it, expect } from 'vitest'
import { summarizeInClusterTests, evidenceBannerTitle, type InClusterTestRow } from './inClusterSummary'

// The three server row shapes (internal/reachability/incluster.go):
//   folded       - results, NO status        (default branch: outcome moved)
//   evidence     - results AND status         (throwaway-denied / override-mismatch / guessed)
//   couldn't-run - status/fallback, NO results (Job failed / capped / budget)
const folded = (): InClusterTestRow => ({ results: [{ layer: 'http', ok: true }] })
const throwawayDenied = (): InClusterTestRow => ({
  results: [{ layer: 'http', ok: false }],
  status: "in-cluster probe from a throwaway pod didn't get through - not a confirmed failure",
  fallbackCommand: 'kubectl run ...',
})
const overrideMismatch = (): InClusterTestRow => ({
  results: [{ layer: 'http', ok: true }],
  status: 'tested custom path /x: shown as evidence only; the declared path / was not tested',
})
const guessedPath = (): InClusterTestRow => ({
  results: [{ layer: 'http', ok: true }],
  status: 'in-cluster probe reached the backend on a GUESSED path',
})
const jobFailed = (): InClusterTestRow => ({ status: 'in-cluster probe timed out: probe container couldn\'t start', fallbackCommand: 'kubectl ...' })
const capped = (): InClusterTestRow => ({ status: 'not tested in-cluster: capped at 8 probe pods per call' })

describe('summarizeInClusterTests', () => {
  it('nothing ran (all status-only, no results) → couldn\'t run, not partial, not evidence-only', () => {
    const s = summarizeInClusterTests([jobFailed()])
    expect(s.error).toContain('timed out')
    expect(s.fallback).toBe('kubectl ...')
    expect(s.partial).toBe(false)
    expect(s.evidenceOnly).toBe(false)
  })

  it('some folded + a couldn\'t-run row → error set AND partial true (partially completed)', () => {
    const s = summarizeInClusterTests([folded(), capped()])
    expect(s.error).toContain('capped')
    expect(s.partial).toBe(true)
    expect(s.evidenceOnly).toBe(false)
  })

  it('single throwaway-denied row (results+status+fallback, none folded) → evidence-only, NOT partial, NOT couldn\'t-run', () => {
    const s = summarizeInClusterTests([throwawayDenied()])
    expect(s.error).toBeUndefined()
    expect(s.partial).toBe(false)
    expect(s.evidenceOnly).toBe(true)
    expect(s.evidence).toContain('throwaway pod')
    expect(s.fallback).toBe('kubectl run ...')
  })

  it('all override-mismatch rows (results+status, none folded) → evidence-only note, not a silent no-op', () => {
    const s = summarizeInClusterTests([overrideMismatch(), overrideMismatch()])
    expect(s.evidenceOnly).toBe(true)
    expect(s.evidence).toContain('evidence only')
    expect(s.error).toBeUndefined()
    expect(s.partial).toBe(false)
  })

  it('guessed-path-only row → evidence-only', () => {
    const s = summarizeInClusterTests([guessedPath()])
    expect(s.evidenceOnly).toBe(true)
    expect(s.evidence).toContain('GUESSED')
  })

  it('clean full run (everything folded) → no banner at all', () => {
    const s = summarizeInClusterTests([folded(), folded()])
    expect(s.error).toBeUndefined()
    expect(s.partial).toBe(false)
    expect(s.evidenceOnly).toBe(false)
  })

  it('a couldn\'t-run row outranks evidence rows (real failure surfaces first)', () => {
    const s = summarizeInClusterTests([overrideMismatch(), jobFailed()])
    expect(s.error).toContain('timed out')
    expect(s.partial).toBe(false) // nothing folded
    expect(s.evidenceOnly).toBe(false)
  })

  it('empty / undefined input → no banner', () => {
    expect(summarizeInClusterTests(undefined)).toEqual({ partial: false, evidenceOnly: false })
    expect(summarizeInClusterTests([])).toEqual({ partial: false, evidenceOnly: false })
  })
})

describe('a mixed run never says "unchanged"', () => {
  const folded = { route: 'a', results: [{}] }
  const evidence = { route: 'b', results: [{}], status: 'kept informational - throwaway identity' }

  it('the reducer marks the mixed case partial', () => {
    const s = summarizeInClusterTests([folded, evidence] as any)
    expect(s.evidenceOnly).toBe(true)
    expect(s.partial).toBe(true)
  })

  it('the banner title follows the state', () => {
    expect(evidenceBannerTitle({ partial: true, evidenceOnly: true })).toMatch(/some routes updated/)
    expect(evidenceBannerTitle({ partial: false, evidenceOnly: true })).toMatch(/unchanged/)
  })
})
