import { describe, expect, it } from 'vitest'
import { structuredPatch } from 'diff'
import { buildSplitRows, isHunkGap, type SplitRow } from './ArgoResourceDiff'

function rowsFor(desired: string, live: string, context: number): SplitRow[] {
  const patch = structuredPatch('desired', 'live', desired, live, '', '', { context })
  return buildSplitRows(patch.hunks)
}

describe('buildSplitRows', () => {
  it('pairs a single changed line as one row on both sides', () => {
    const rows = rowsFor('a\nb\nc\n', 'a\nX\nc\n', Number.MAX_SAFE_INTEGER)
    const changed = rows.find((r) => r.left?.type === 'removal')
    expect(changed?.left).toEqual({ num: 2, text: 'b', type: 'removal' })
    expect(changed?.right).toEqual({ num: 2, text: 'X', type: 'addition' })
  })

  it('zips an unequal removal/addition block, blank-padding the shorter side', () => {
    const rows = rowsFor('a\nb\nc\n', 'a\nX\nY\nZ\nc\n', Number.MAX_SAFE_INTEGER)
    const block = rows.filter((r) => r.left?.type === 'removal' || r.right?.type === 'addition')
    expect(block).toHaveLength(3)
    expect(block[0].left).toEqual({ num: 2, text: 'b', type: 'removal' })
    expect(block[0].right).toEqual({ num: 2, text: 'X', type: 'addition' })
    expect(block[1].left).toBeUndefined()
    expect(block[1].right).toEqual({ num: 3, text: 'Y', type: 'addition' })
    expect(block[2].left).toBeUndefined()
    expect(block[2].right).toEqual({ num: 4, text: 'Z', type: 'addition' })
  })

  it('carries context lines through unchanged on both sides with matching line numbers', () => {
    const rows = rowsFor('a\nb\nc\n', 'a\nX\nc\n', Number.MAX_SAFE_INTEGER)
    const context = rows.filter((r) => r.left?.type === 'context')
    expect(context.map((r) => r.left?.text)).toEqual(['a', 'c'])
    expect(context.map((r) => [r.left?.num, r.right?.num])).toEqual([[1, 1], [3, 3]])
  })

  it('inserts a hunk-gap row only between non-adjacent hunks (compact context)', () => {
    const desired = Array.from({ length: 20 }, (_, i) => `line${i}`).join('\n') + '\n'
    const live = desired.replace('line2', 'X').replace('line17', 'Y')
    const rows = rowsFor(desired, live, 2)
    const gaps = rows.filter(isHunkGap)
    expect(gaps).toHaveLength(1)
  })

  it('produces no hunk-gap row for a single hunk (full-file context)', () => {
    const rows = rowsFor('a\nb\nc\n', 'a\nX\nc\n', Number.MAX_SAFE_INTEGER)
    expect(rows.some(isHunkGap)).toBe(false)
  })

  it('returns no rows for identical documents', () => {
    const rows = rowsFor('a\nb\nc\n', 'a\nb\nc\n', Number.MAX_SAFE_INTEGER)
    expect(rows).toHaveLength(0)
  })
})
