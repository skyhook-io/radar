import { describe, expect, it } from 'vitest'
import { getColumnMinWidth, type Column } from './ResourcesView'

// The header row is px-4 with a `truncate` label, under table-layout:fixed with
// explicit <col> widths. A column narrower than its own heading clips to
// "ADDRESS T…" and — because only the Name column flexes — stays clipped at
// 1280, 1920 and 2560 alike. These pin the floor that stops that.
//
// Reference measurements taken in the browser against the real header font
// (DM Sans 500, 12px, uppercase, tracking-wide), on kind-radar-kyverno-demo:
//   RECLAIM       52.7px  ADDRESSES     69.6px
//   EXPANSION     67.5px  ADDRESS TYPE  87.7px
const MEASURED = {
  Reclaim: 52.7,
  Expansion: 67.5,
  Addresses: 69.6,
  'Address Type': 87.7,
}

const col = (over: Partial<Column> & Pick<Column, 'key' | 'label'>): Column => over

describe('getColumnMinWidth header floor', () => {
  it.each(Object.entries(MEASURED))(
    'reserves at least the measured width of %s plus padding',
    (label, measuredPx) => {
      const got = getColumnMinWidth(col({ key: 'x', label, width: 'w-16' }))
      // 32px is the px-4 on both sides; the estimate must never land under the
      // width the browser actually renders, or the label clips again.
      expect(got).toBeGreaterThanOrEqual(Math.ceil(measuredPx + 32))
    },
  )

  it('leaves a column that already fits its label untouched', () => {
    // "Age" needs ~25px + 32 padding + 20 for its sort icon = well under w-24.
    expect(getColumnMinWidth(col({ key: 'age', label: 'Age', width: 'w-24' }))).toBe(96)
  })

  it('raises a column that is narrower than its label', () => {
    // w-20 is 80px; "Address Type" cannot fit in 80 - 32 = 48px.
    expect(getColumnMinWidth(col({ key: 'addressType', label: 'Address Type', width: 'w-20' })))
      .toBeGreaterThan(80)
  })

  it('reserves extra room for the sort icon on sortable columns', () => {
    const sortable = getColumnMinWidth(col({ key: 'status', label: 'Wide Label Here', width: 'w-16' }))
    const plain = getColumnMinWidth(col({ key: 'notSortable', label: 'Wide Label Here', width: 'w-16' }))
    expect(sortable).toBeGreaterThan(plain)
  })

  it('still honours an explicit minWidth override', () => {
    expect(getColumnMinWidth(col({ key: 'x', label: 'Address Type', width: 'w-20', minWidth: 300 }))).toBe(300)
  })

  it('keeps the flexible Name column at its wider default', () => {
    expect(getColumnMinWidth(col({ key: 'name', label: 'Name' }))).toBe(200)
  })

  // Printer columns (uncurated CRDs) and host-injected custom columns are
  // sortable via their own getSortValue, not by being in the built-in key list —
  // the header renders a sort icon for them, so the floor has to reserve it.
  it('reserves the sort icon for a printer/custom column that carries getSortValue', () => {
    const c = col({ key: 'printer:Phase', label: 'Reconcile Phase', width: 'w-24' })
    const extras = new Map([[c.key, { ...c, render: () => null, getSortValue: () => '' }]]) as any
    expect(getColumnMinWidth(c, extras)).toBeGreaterThan(getColumnMinWidth(c))
  })

  it('caps the floor so one long vendor label cannot size a column off screen', () => {
    const monstrous = 'A'.repeat(200)
    expect(getColumnMinWidth(col({ key: 'printer:x', label: monstrous, width: 'w-24' }))).toBe(320)
  })

  it('never returns less than the declared width, even past the cap', () => {
    expect(getColumnMinWidth(col({ key: 'printer:x', label: 'A'.repeat(200), width: 'w-96' }))).toBe(384)
  })
})
