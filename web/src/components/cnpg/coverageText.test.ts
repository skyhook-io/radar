import { describe, expect, it } from 'vitest'
import { coverageEmpty, coverageLabel } from './shared'

describe('coverage wording', () => {
  it('names every cause the server named, whatever the overall state', () => {
    const mixed = { state: 'uncached' as const, uncachedNamespaces: ['b'], deniedNamespaces: ['c'] }
    expect(coverageEmpty(mixed, 'Pods')).toBe('No Pods visible: no access in c; Radar does not cache Pods in b.')
    expect(coverageLabel(mixed)).toBe('no access in c; not cached by Radar in b')
  })
  it('falls back to the state when no namespace is named', () => {
    expect(coverageEmpty({ state: 'uncached' }, 'Pods')).toBe('Radar does not cache Pods in this scope.')
    expect(coverageEmpty({ state: 'partial' }, 'Pods')).toBe('No Pods visible. Some namespaces were not read.')
    expect(coverageLabel({ state: 'denied' })).toBe('no access')
  })
})
