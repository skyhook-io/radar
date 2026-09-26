import { readdirSync, readFileSync, statSync } from 'node:fs'
import { join, relative } from 'node:path'
import { describe, expect, it } from 'vitest'

// Screen readers announce a dialog by its name; an unnamed one is read as just
// "dialog". Every DialogPortal must pass ariaLabelledBy (its visible title) or
// ariaLabel.
const repoRoot = join(__dirname, '..', '..', '..')
const roots = ['web/src', 'packages/k8s-ui/src'].map(r => join(repoRoot, r))

function sourceFiles(dir: string): string[] {
  return readdirSync(dir).flatMap(name => {
    const path = join(dir, name)
    if (statSync(path).isDirectory()) return name === 'node_modules' ? [] : sourceFiles(path)
    return /\.tsx$/.test(name) && !/\.test\.tsx$/.test(name) ? [path] : []
  })
}

// The opening tag ends at the first '>' outside a {…} expression.
function openingTags(source: string, component: string): string[] {
  const tags: string[] = []
  let from = 0
  for (;;) {
    const start = source.indexOf(`<${component}`, from)
    if (start < 0) return tags
    let depth = 0
    let i = start + component.length + 1
    for (; i < source.length; i++) {
      const c = source[i]
      if (c === '{') depth++
      else if (c === '}') depth--
      else if (c === '>' && depth === 0) break
    }
    tags.push(source.slice(start, i + 1))
    from = i
  }
}

describe('dialogs', () => {
  it('every DialogPortal has an accessible name', () => {
    const unnamed = roots.flatMap(sourceFiles).flatMap(file => {
      if (file.endsWith('DialogPortal.tsx')) return []
      return openingTags(readFileSync(file, 'utf8'), 'DialogPortal')
        .filter(tag => /^<DialogPortal[\s>]/.test(tag) && !/\baria(Label|LabelledBy)=/.test(tag))
        .map(() => relative(repoRoot, file))
    })
    expect(unnamed).toEqual([])
  })
})
