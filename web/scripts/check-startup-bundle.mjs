#!/usr/bin/env node
// Guard: Monaco and ELK stay out of what the browser loads at startup.
// Both are loaded on first use (the YAML editor, the first traffic layout);
// one static import of either from startup code would quietly put megabytes
// back on every page load. Walks the built index.html's entry script,
// modulepreload links and stylesheets, follows each chunk's static imports
// (dynamic import() calls are the point and are not followed), and looks for
// content each library carries rather than chunk names alone.
//
// Usage: node scripts/check-startup-bundle.mjs [dist-dir]   (default: dist)
import { existsSync, readFileSync, statSync } from 'node:fs'
import { dirname, join, relative, resolve } from 'node:path'

const LIBRARIES = [
  { name: 'ELK', files: /^elk/i, markers: ['org.eclipse.elk'] },
  {
    name: 'Monaco',
    files: /^monaco/i,
    js: ['MonacoEnvironment', 'editor.action.formatDocument'],
    css: ['.monaco-scrollable-element', '.codicon'],
  },
]

const dist = resolve(process.argv[2] ?? 'dist')
const indexPath = join(dist, 'index.html')
if (!existsSync(indexPath)) {
  console.error(`check-startup-bundle: ${relative(process.cwd(), indexPath)} not found; build the frontend first.`)
  process.exit(1)
}
const html = readFileSync(indexPath, 'utf8')

const attr = (tag, name) => tag.match(new RegExp(`\\s${name}\\s*=\\s*["']([^"']+)["']`, 'i'))?.[1]
const local = (href) => href && !/^([a-z]+:)?\/\//i.test(href) && !href.startsWith('data:')
const fromIndex = (href) => join(dist, href.replace(/^\.?\//, ''))

const roots = []
for (const tag of html.match(/<script\b[^>]*>/gi) ?? []) {
  const src = attr(tag, 'src')
  if (local(src) && /type\s*=\s*["']module["']/i.test(tag)) roots.push(fromIndex(src))
}
for (const tag of html.match(/<link\b[^>]*>/gi) ?? []) {
  const rel = attr(tag, 'rel')?.toLowerCase()
  const href = attr(tag, 'href')
  if (local(href) && (rel === 'modulepreload' || rel === 'stylesheet')) roots.push(fromIndex(href))
}
if (!roots.some((f) => f.endsWith('.js'))) {
  console.error('check-startup-bundle: index.html has no module entry script; the guard cannot check anything.')
  process.exit(1)
}

// Static `import … from "./x.js"`, `import "./x.js"` and `export … from "./x.js"`.
// The excluded characters keep a match inside one statement and away from
// import("./x.js").
const STATIC_IMPORT = /\b(?:import|export)\b[^'"`();]*?["'](\.\.?\/[^"']+)["']/g

const startup = new Set()
const queue = [...roots]
while (queue.length) {
  const file = queue.pop()
  if (startup.has(file)) continue
  if (!existsSync(file)) {
    console.error(`check-startup-bundle: ${relative(dist, file)} is referenced at startup but missing from the build.`)
    process.exit(1)
  }
  startup.add(file)
  if (!file.endsWith('.js')) continue
  for (const [, spec] of readFileSync(file, 'utf8').matchAll(STATIC_IMPORT)) {
    queue.push(resolve(dirname(file), spec))
  }
}

const offenders = []
for (const file of startup) {
  const base = file.slice(file.lastIndexOf('/') + 1)
  const content = readFileSync(file, 'utf8')
  const isCss = file.endsWith('.css')
  for (const lib of LIBRARIES) {
    if (lib.files.test(base)) offenders.push(`${relative(dist, file)}: named as a ${lib.name} chunk`)
    const markers = lib.markers ?? (isCss ? lib.css : lib.js)
    for (const marker of markers) {
      if (content.includes(marker)) offenders.push(`${relative(dist, file)}: contains ${lib.name} ("${marker}")`)
    }
  }
}

const kb = (file) => `${(statSync(file).size / 1024).toFixed(0)} kB`
const listing = [...startup].sort().map((f) => `  ${relative(dist, f)}  ${kb(f)}`).join('\n')

if (offenders.length) {
  console.error('Monaco or ELK is part of the startup bundle. Load it with a dynamic import() from the code that needs it.\n')
  console.error(offenders.map((o) => `  ${o}`).join('\n'))
  console.error(`\nStartup files:\n${listing}`)
  process.exit(1)
}
console.log(`Startup bundle is free of Monaco and ELK (${startup.size} files):\n${listing}`)
