import { describe, expect, it } from 'vitest'
import {
  formatPrinterCell,
  printerTableMatchesKind,
  parseHumanDuration,
  printerCellSortValue,
  printerCellTone,
  printerColumnKey,
  printerTableKey,
  readPrinterCell,
  sanitizePrinterTable,
} from './printer-columns'

describe('sanitizePrinterTable', () => {
  it('keeps a well-formed table and defaults priority to 0', () => {
    const t = sanitizePrinterTable({
      columns: [{ name: 'Phase', type: 'string' }, { name: 'Replicas', type: 'integer', priority: 1 }],
      cells: { a: ['Running', 3] },
    })
    expect(t?.columns).toEqual([
      { name: 'Phase', type: 'string', format: undefined, description: undefined, priority: 0 },
      { name: 'Replicas', type: 'integer', format: undefined, description: undefined, priority: 1 },
    ])
    expect(t?.cells.a).toEqual(['Running', 3])
  })

  it.each([
    ['null', null],
    ['no columns key', { cells: {} }],
    ['empty columns', { columns: [], cells: {} }],
    ['columns that are not an array', { columns: 'Phase' }],
    ['every column malformed', { columns: [{ type: 'string' }, null, { name: '  ' }] }],
  ])('returns null for %s', (_label, raw) => {
    expect(sanitizePrinterTable(raw)).toBeNull()
  })

  // A dropped duplicate must take its cell with it, or every later value
  // renders one column to the left.
  it('realigns cells when a column is dropped', () => {
    const t = sanitizePrinterTable({
      columns: [{ name: 'Phase' }, { name: 'Phase' }, { name: 'Host' }],
      cells: { a: ['Running', 'dup', 'example.com'] },
    })
    expect(t?.columns.map(c => c.name)).toEqual(['Phase', 'Host'])
    expect(t?.cells.a).toEqual(['Running', 'example.com'])
  })

  it('ignores rows that are not arrays', () => {
    const t = sanitizePrinterTable({ columns: [{ name: 'A' }, { name: 'B' }], cells: { a: 'nope', b: ['1', '2'] } })
    expect(t?.cells).toEqual({ b: ['1', '2'] })
  })

  it('tolerates a missing cells map', () => {
    expect(sanitizePrinterTable({ columns: [{ name: 'A' }] })?.cells).toEqual({})
  })
})

describe('printerTableKey', () => {
  it('is stable across refetches and changes with the column set', () => {
    const a = { columns: [{ name: 'Phase' }, { name: 'Host' }], cells: {} }
    const b = { columns: [{ name: 'Phase' }, { name: 'Host' }], cells: { x: ['1'] } }
    expect(printerTableKey(a)).toBe(printerTableKey(b))
    expect(printerTableKey(a)).not.toBe(printerTableKey({ columns: [{ name: 'Phase' }], cells: {} }))
  })

  it('is empty when there is no table', () => {
    expect(printerTableKey(null)).toBe('')
    expect(printerTableKey({ columns: [], cells: {} })).toBe('')
  })
})

describe('printerColumnKey', () => {
  // Built-in keys never carry a prefix and custom columns use label:/annotation:,
  // so this namespace cannot collide with either.
  it('namespaces the key', () => {
    expect(printerColumnKey({ name: 'Status' })).toBe('printer:Status')
    expect(printerColumnKey({ name: 'Status' })).not.toBe('status')
  })
})

describe('readPrinterCell', () => {
  const table = { columns: [{ name: 'A' }, { name: 'B' }], cells: { u1: ['x', 2] } }
  it('reads by uid and index', () => {
    expect(readPrinterCell(table, 'u1', 0)).toBe('x')
    expect(readPrinterCell(table, 'u1', 1)).toBe(2)
  })
  it('is undefined for an unknown uid, a missing uid, or an out-of-range index', () => {
    expect(readPrinterCell(table, 'nope', 0)).toBeUndefined()
    expect(readPrinterCell(table, undefined, 0)).toBeUndefined()
    expect(readPrinterCell(table, 'u1', 9)).toBeUndefined()
  })
})

describe('formatPrinterCell', () => {
  it.each([
    ['string', 'Running', 'Running'],
    ['zero', 0, '0'],
    ['number', 3, '3'],
    ['true', true, 'True'],
    ['false', false, 'False'],
    ['null', null, ''],
    ['undefined', undefined, ''],
    // The apiserver leaves an unresolvable path null, but a `type` mismatch can
    // still hand back a structure. Rendering "[object Object]" would be worse
    // than rendering nothing.
    ['object', { a: 1 }, ''],
    ['array', [1, 2], ''],
  ])('renders %s', (_label, value, want) => {
    expect(formatPrinterCell(value)).toBe(want)
  })
})

describe('printerCellSortValue', () => {
  // Sorting must use the value, not its rendered text, or 10 sorts before 9.
  it('sorts numbers numerically', () => {
    expect([10, 9, 100].map(printerCellSortValue).sort((a, b) => (a as number) - (b as number)))
      .toEqual([9, 10, 100])
  })
  it('sorts booleans by truth and falls back to empty for anything else', () => {
    expect(printerCellSortValue(true)).toBe(1)
    expect(printerCellSortValue(false)).toBe(0)
    expect(printerCellSortValue({ a: 1 })).toBe('')
    expect(printerCellSortValue(null)).toBe('')
  })
})

describe('printerTableMatchesKind', () => {
  const table = { kind: 'widgets', group: 'example.com', columns: [{ name: 'A' }], cells: {} }

  it('matches its own kind and group', () => {
    expect(printerTableMatchesKind(table, 'widgets', 'example.com')).toBe(true)
  })

  // The selected kind changes before the new list resolves, so an unchecked
  // table can briefly be the PREVIOUS kind's — which the visibility effect
  // would then persist under the new kind's storage key.
  it('rejects a table belonging to another kind or another group', () => {
    expect(printerTableMatchesKind(table, 'gadgets', 'example.com')).toBe(false)
    expect(printerTableMatchesKind(table, 'widgets', 'other.io')).toBe(false)
    expect(printerTableMatchesKind(table, 'widgets', undefined)).toBe(false)
  })

  it('rejects a table with no identity rather than pairing it blindly', () => {
    expect(printerTableMatchesKind({ columns: [{ name: 'A' }], cells: {} }, 'widgets', 'example.com')).toBe(false)
    expect(printerTableMatchesKind(null, 'widgets', 'example.com')).toBe(false)
  })

  it('treats an absent group and an empty group as the same', () => {
    const noGroup = { kind: 'widgets', columns: [{ name: 'A' }], cells: {} }
    expect(printerTableMatchesKind(noGroup, 'widgets', '')).toBe(true)
    expect(printerTableMatchesKind(noGroup, 'widgets', undefined)).toBe(true)
  })
})

describe('sanitizePrinterTable identity', () => {
  it('carries kind and group through', () => {
    const t = sanitizePrinterTable({ kind: 'widgets', group: 'example.com', columns: [{ name: 'A' }], cells: {} })
    expect(t).toMatchObject({ kind: 'widgets', group: 'example.com' })
  })

  it('leaves them undefined when the server sent none', () => {
    const t = sanitizePrinterTable({ columns: [{ name: 'A' }], cells: {} })
    expect(t?.kind).toBeUndefined()
    expect(t?.group).toBeUndefined()
  })
})

describe('date sorting', () => {
  // The table converter hands back humanized ages, so sorting them as text puts
  // "10m" before "2m".
  it.each([['5s', 5], ['2m', 120], ['3h', 10800], ['4d', 345600], ['1y', 31536000], ['2d3h', 183600], ['1y64d', 37065600]])(
    'parses %s', (text, want) => expect(parseHumanDuration(text)).toBe(want))

  it.each([['', null], ['   ', null], ['abc', null], ['5', null], ['5x', null], ['-3m', null], ['<invalid>', null]])(
    'rejects %s', (text, want) => expect(parseHumanDuration(text)).toBe(want))

  it('orders date cells oldest-first ascending, like the Age column', () => {
    const def = { name: 'Last Refresh', type: 'date' }
    const sorted = ['10m', '2m', '1h', '30s']
      .map(v => ({ v, k: printerCellSortValue(v, def) as number }))
      .sort((a, b) => a.k - b.k)
      .map(x => x.v)
    expect(sorted).toEqual(['1h', '10m', '2m', '30s'])
  })

  it('leaves a date column that is not a duration sorting as text', () => {
    expect(printerCellSortValue('<invalid>', { name: 'D', type: 'date' })).toBe('<invalid>')
  })

  it('does not reinterpret duration-looking values in non-date columns', () => {
    expect(printerCellSortValue('10m', { name: 'Size', type: 'string' })).toBe('10m')
    expect(printerCellSortValue('10m')).toBe('10m')
  })
})

describe('printerTableKey priority', () => {
  // Default visibility is derived from priority, so promoting a column out of
  // the -o wide tier has to re-run the visibility defaults.
  it('changes when a column priority changes', () => {
    const wide = { columns: [{ name: 'A' }, { name: 'B', priority: 1 }], cells: {} }
    const promoted = { columns: [{ name: 'A' }, { name: 'B', priority: 0 }], cells: {} }
    expect(printerTableKey(wide)).not.toBe(printerTableKey(promoted))
  })
})

describe('printerCellTone', () => {
  // The one place Radar interprets vendor data rather than passing it through.
  // Deliberately narrow: the vendor NAMED the column, which is a far stronger
  // signal than inferring health from a value.
  it.each([
    ['Ready', true, 'healthy'],
    ['Ready', false, 'unhealthy'],
    ['Healthy', 'True', 'healthy'],
    ['Healthy', 'False', 'unhealthy'],
    ['Established', 'true', 'healthy'],
    ['Installed', 'True', 'healthy'],
    ['Provided', 'True', 'healthy'],
    ['ReadyToUse', 'False', 'unhealthy'],
    ['Synced', 'True', 'healthy'],
  ])('%s=%s tones as %s', (name, value, want) => {
    expect(printerCellTone({ name }, value)).toBe(want)
  })

  // Names whose polarity is inverted must not be read as positive.
  it.each([
    ['Degraded', 'False', 'healthy'],
    ['Denied', 'False', 'healthy'],
  ])('%s=%s tones as %s', (name, value, want) => {
    expect(printerCellTone({ name }, value)).toBe(want)
  })

  // A controller that could not evaluate a field it owns has not reported a
  // failure — but only for a column we have a claim to speak about.
  it('reports Unknown without calling it a failure, on named columns', () => {
    expect(printerCellTone({ name: 'Ready' }, 'Unknown')).toBe('unknown')
    expect(printerCellTone({ name: 'Degraded' }, 'Unknown')).toBe('unknown')
  })

  // The name decides whether we speak at all. Checking the value first badged
  // `Mode: Unknown` on a column whose polarity we have no claim to.
  it('stays silent on an unnamed column even when the value is Unknown', () => {
    expect(printerCellTone({ name: 'Mode' }, 'Unknown')).toBeNull()
    expect(printerCellTone({ name: 'Strategy' }, 'Unknown')).toBeNull()
    expect(printerCellTone({ name: 'Whatever' }, 'Unknown')).toBeNull()
  })

  // The same condition must not change severity depending on whether it
  // arrived through the generic Status column or a printer column.
  it('tones a degraded name amber, matching the generic ladder', () => {
    expect(printerCellTone({ name: 'Degraded' }, 'True')).toBe('degraded')
    expect(printerCellTone({ name: 'Warning' }, 'True')).toBe('degraded')
    expect(printerCellTone({ name: 'ScalingLimited' }, 'True')).toBe('degraded')
  })

  it('reserves red for genuinely terminal names', () => {
    expect(printerCellTone({ name: 'Denied' }, 'True')).toBe('unhealthy')
    expect(printerCellTone({ name: 'Failed' }, 'True')).toBe('unhealthy')
  })

  it('renders plain text for a column whose polarity we do not know', () => {
    expect(printerCellTone({ name: 'Mode' }, 'True')).toBeNull()
    expect(printerCellTone({ name: 'Strategy' }, false)).toBeNull()
  })

  // A named column holding something other than a truth value is not a badge —
  // "Ready: 3/5" is a count, not a state.
  it.each([
    ['Ready', '3/5'],
    ['Ready', 'Active'],
    ['Healthy', 5],
    ['Ready', null],
    ['Ready', { a: 1 }],
  ])('renders %s=%s as plain text', (name, value) => {
    expect(printerCellTone({ name }, value)).toBeNull()
  })

  it('matches the column name case-insensitively and ignores padding', () => {
    expect(printerCellTone({ name: '  READY  ' }, 'true')).toBe('healthy')
  })
})

describe('printerTableKey collision safety', () => {
  // Column names are vendor text and really do contain spaces ("VERIFY IMAGES",
  // "FAILURE POLICY"). A separator-joined key let two different column sets
  // produce one string, so the visibility effect would miss a real change.
  it('distinguishes sets that a separator-joined key would merge', () => {
    const a = printerTableKey({ columns: [{ name: 'A' }, { name: 'B' }, { name: 'C' }], cells: {} })
    const b = printerTableKey({ columns: [{ name: 'A:0 B' }, { name: 'C' }], cells: {} })
    expect(a).not.toBe(b)
  })

  it('still changes when only a priority changes', () => {
    const before = printerTableKey({ columns: [{ name: 'Phase', priority: 1 }], cells: {} })
    const after = printerTableKey({ columns: [{ name: 'Phase', priority: 0 }], cells: {} })
    expect(before).not.toBe(after)
  })
})

