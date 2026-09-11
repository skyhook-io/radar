import { describe, expect, it } from 'vitest'
import { readdirSync, readFileSync, statSync } from 'fs'
import { join } from 'path'

// A stray NUL in a source file compiles fine and passes every type and unit
// check, so nothing else here would catch one. What it does break is the
// tooling around the file: grep treats the file as binary and silently reports
// no matches, which is a genuinely confusing way to lose an afternoon. This
// package is also published as source, so the byte would ship to consumers.
function sourceFiles(dir: string): string[] {
  const out: string[] = []
  for (const entry of readdirSync(dir)) {
    if (entry === 'node_modules' || entry === 'dist') continue
    const full = join(dir, entry)
    if (statSync(full).isDirectory()) out.push(...sourceFiles(full))
    else if (/\.(ts|tsx|css)$/.test(entry)) out.push(full)
  }
  return out
}

describe('source hygiene', () => {
  it('has no NUL bytes in any source file', () => {
    const offenders = sourceFiles(join(__dirname))
      .filter(f => readFileSync(f).includes(0x00))
      .map(f => f.slice(f.indexOf('/src/')))
    expect(offenders).toEqual([])
  })
})
