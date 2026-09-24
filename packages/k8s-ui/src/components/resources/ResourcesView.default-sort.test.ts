import { describe, expect, it } from 'vitest'
import { sanitizeSavedSort } from './ResourcesView'

// The per-kind column record crosses the localStorage boundary, so a saved sort
// can be anything a hand edit or an older build left there.
describe('sanitizeSavedSort', () => {
  it('accepts a column with a known direction', () => {
    expect(sanitizeSavedSort({ column: 'age', direction: 'desc' })).toEqual({ column: 'age', direction: 'desc' })
    expect(sanitizeSavedSort({ column: 'label:app', direction: 'asc' })).toEqual({ column: 'label:app', direction: 'asc' })
  })

  it('reads a missing or malformed record as no saved sort', () => {
    expect(sanitizeSavedSort(undefined)).toBeNull()
    expect(sanitizeSavedSort(null)).toBeNull()
    expect(sanitizeSavedSort('age')).toBeNull()
    expect(sanitizeSavedSort({})).toBeNull()
    expect(sanitizeSavedSort({ column: '', direction: 'asc' })).toBeNull()
    expect(sanitizeSavedSort({ column: 'age' })).toBeNull()
    expect(sanitizeSavedSort({ column: 'age', direction: 'sideways' })).toBeNull()
    expect(sanitizeSavedSort({ column: 42, direction: 'asc' })).toBeNull()
  })

  it('drops fields it does not know rather than carrying them into state', () => {
    expect(sanitizeSavedSort({ column: 'name', direction: 'asc', extra: true })).toEqual({ column: 'name', direction: 'asc' })
  })
})
