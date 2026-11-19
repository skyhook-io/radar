import { describe, expect, it } from 'vitest'
import { readFileSync } from 'fs'
import { join } from 'path'
import { getColumnHeaderWidth } from './ResourcesView'
import { NAME_MIN_PX } from './column-content-width'

// The table's room in a maximized 1920px window with Radar's default sidebars,
// as rendered (Pods, Events and the other 1920 captures all measure 1456).
const ROOM_AT_1920 = 1456
// What a column shrunk to its heading keeps beyond it (HEADER_SHRINK_SLACK_PX).
const HEADER_SLACK = 6

// Parsed from the source, as curated-column-ownership.test.ts does.
function curatedDefaultColumns(): Record<string, { key: string; label: string; width?: string }[]> {
  const src = readFileSync(join(__dirname, 'ResourcesView.tsx'), 'utf8')
  const start = src.indexOf('const KNOWN_COLUMNS')
  const body = src.slice(start, src.indexOf('\n}\n', start))
  const kinds: Record<string, { key: string; label: string; width?: string }[]> = {}
  let plural = ''
  for (const line of body.split('\n')) {
    const kind = line.match(/^  '?([a-z0-9.]+)'?: \[/)
    if (kind) { plural = kind[1]; kinds[plural] = []; continue }
    const col = line.match(/\{ key: '([^']+)', label: '((?:[^'\\]|\\.)*)'(?:, width: '([^']*)')?/)
    if (!col || !plural || /defaultVisible: false/.test(line)) continue
    kinds[plural].push({ key: col[1], label: col[2], width: col[3] })
  }
  return kinds
}

describe('a 1920px window', () => {
  it('fits every curated kind\'s default columns at their headings, with Name at its minimum', () => {
    const kinds = curatedDefaultColumns()
    expect(Object.keys(kinds).length).toBeGreaterThan(40)
    expect(kinds.pods.map(col => col.key)).toContain('name')
    const over: string[] = []
    for (const [plural, columns] of Object.entries(kinds)) {
      const total = columns.reduce((sum, col) => sum + (col.key === 'name' ? NAME_MIN_PX : getColumnHeaderWidth(col) + HEADER_SLACK), 0)
      if (total > ROOM_AT_1920) over.push(`${plural}: ${total}`)
    }
    expect(over).toEqual([])
  })
})
