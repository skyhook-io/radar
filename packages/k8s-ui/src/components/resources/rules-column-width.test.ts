import { describe, expect, it } from 'vitest'
import { readFileSync } from 'fs'
import { join } from 'path'

// Parsed from the source rather than exported, matching
// curated-column-ownership.test.ts: this asserts a property of the data as it
// is written, and an export would let the two drift.
const SRC = readFileSync(join(__dirname, 'ResourcesView.tsx'), 'utf8')

function columnSetBody(key: string): string {
  const start = SRC.match(new RegExp(`^  ${key}: \\[`, 'm'))
  if (!start || start.index === undefined) throw new Error(`${key} not found in KNOWN_COLUMNS`)
  const open = SRC.indexOf('[', start.index)
  let depth = 1
  let i = open + 1
  while (depth > 0) {
    if (SRC[i] === '[') depth++
    else if (SRC[i] === ']') depth--
    i++
  }
  return SRC.slice(open + 1, i)
}

// The header cell is px-4, so w-16 (64px) leaves 31px for the label while
// uppercase "RULES" needs ~37px — it clips to "RUL…" unconditionally, with no
// sort or filter button involved: 'rules' is in SKIP_FILTER_COLUMNS and is not
// in the sortable-key list, so neither affordance ever renders on it. w-20
// (80px) leaves 47px.
describe('Rules column width', () => {
  const kinds = ['kyvernopolicies', 'clusterpolicies', 'httproutes', 'grpcroutes', 'tcproutes', 'tlsroutes']

  it.each(kinds)('%s Rules column is at least w-20', (kind) => {
    const rules = columnSetBody(kind).match(/\{ key: 'rules'.*\}/)
    expect(rules, `${kind} has no rules column`).not.toBeNull()
    const width = rules![0].match(/width: 'w-(\d+)'/)
    expect(width, `${kind} rules column has no fixed w-* width`).not.toBeNull()
    // Compared numerically, not pinned to w-20, so the wider table-width pass
    // tracked in #1583 can widen these without tripping a test that only
    // claims a floor.
    expect(Number(width![1])).toBeGreaterThanOrEqual(20)
  })
})
