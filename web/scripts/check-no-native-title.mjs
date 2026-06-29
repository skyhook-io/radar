#!/usr/bin/env node
// Guard: no native `title=` attributes on host (lowercase) JSX elements.
// Native title renders the browser's unstyled gray tooltip (ignores theme,
// breaks dark mode). Use the shared <Tooltip> instead. Component props named
// `title` (e.g. <PageHeader title=...>) and SVG <title> elements are fine.
import { readFileSync, readdirSync, statSync } from 'node:fs'
import { join } from 'node:path'
import ts from 'typescript'

const ROOT = 'src'
const offenders = []

function walk(dir) {
  for (const name of readdirSync(dir)) {
    const p = join(dir, name)
    const s = statSync(p)
    if (s.isDirectory()) walk(p)
    else if (p.endsWith('.tsx')) scan(p)
  }
}

function scan(file) {
  const src = ts.createSourceFile(file, readFileSync(file, 'utf8'), ts.ScriptTarget.Latest, true, ts.ScriptKind.TSX)
  const visit = (node) => {
    if (ts.isJsxOpeningElement(node) || ts.isJsxSelfClosingElement(node)) {
      const tag = node.tagName.getText(src)
      const isHost = /^[a-z]/.test(tag) // lowercase = DOM element, not a Component
      if (isHost) {
        for (const attr of node.attributes.properties) {
          if (ts.isJsxAttribute(attr) && attr.name.getText(src) === 'title') {
            const { line } = src.getLineAndCharacterOfPosition(attr.getStart(src))
            offenders.push(`${file}:${line + 1}  <${tag} title=…>`)
          }
        }
      }
    }
    ts.forEachChild(node, visit)
  }
  visit(src)
}

walk(ROOT)
if (offenders.length) {
  console.error(`\n✖ Found ${offenders.length} native title attribute(s) on DOM elements.`)
  console.error('  Use the shared <Tooltip> component instead (themed, dark-mode aware).\n')
  for (const o of offenders) console.error('  ' + o)
  console.error('')
  process.exit(1)
}
console.log('✓ no native title= attributes on host elements')
