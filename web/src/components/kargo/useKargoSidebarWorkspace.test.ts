import { describe, expect, it } from 'vitest'
import { bestScore } from '../ui/command-items'
import { kargoCommandItem, kargoProblemCount } from './useKargoSidebarWorkspace'

describe('Kargo sidebar badge', () => {
  it('says what its number counts', () => {
    const full = { stages: { state: 'full' as const } }
    expect(kargoProblemCount({ installed: true, projects: [{ name: 'a', stages: 2, problems: 2 }], coverage: full }).title).toBe('2 problems')
    expect(kargoProblemCount({ installed: true, projects: [{ name: 'a', stages: 2, problems: 1 }], coverage: full }).title).toBe('1 problem')
    expect(kargoProblemCount({ installed: true, projects: [{ name: 'a', stages: 2, problems: 1 }], coverage: { stages: { state: 'partial' } } }).title)
      .toBe('At least 1 problem: some projects could not be read')
  })
})

describe('Kargo in the command palette', () => {
  it('is found by kargo, pipeline and the page name', () => {
    const item = kargoCommandItem(() => {})
    for (const query of ['kargo', 'pipeline', 'kargo pipeline']) expect(bestScore(item, query)).toBeGreaterThan(0)
    expect(bestScore(item, 'velero')).toBe(0)
  })
})
