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

// w-16 (64px) clips the "Rules" header to "RUL…" once sort/filter affordances
// render; w-20 (80px) fits it.
describe('Rules column width', () => {
  const kinds = ['kyvernopolicies', 'clusterpolicies', 'httproutes', 'grpcroutes', 'tcproutes', 'tlsroutes']

  it.each(kinds)('%s Rules column is at least w-20', (kind) => {
    const rules = columnSetBody(kind).match(/\{ key: 'rules'.*\}/)
    expect(rules, `${kind} has no rules column`).not.toBeNull()
    expect(rules![0]).not.toContain("width: 'w-16'")
    expect(rules![0]).toContain("width: 'w-20'")
  })
})
