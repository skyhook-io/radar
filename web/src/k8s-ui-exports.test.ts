/// <reference types="node" />
import { describe, expect, test } from 'vitest'
import { existsSync, readFileSync, readdirSync, statSync } from 'node:fs'
import { dirname, join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

// Radar's own build aliases @skyhook-io/k8s-ui to its source, so a deep
// import that the package's `exports` map can't serve still builds here and
// only fails in a consumer that installs the published packages (Radar Hub).
// This resolves every deep import the way Node and bundlers do: the most
// specific matching key wins, and an array target resolves to its first entry
// without falling back when that file doesn't exist.

const here = dirname(fileURLToPath(import.meta.url))
const k8sUiRoot = resolve(here, '../../packages/k8s-ui')
const exportsMap: Record<string, string | string[]> = JSON.parse(
  readFileSync(join(k8sUiRoot, 'package.json'), 'utf8'),
).exports

function resolveExport(subpath: string): string | undefined {
  const key = `./${subpath}`
  let target = exportsMap[key]
  let star: string | undefined
  if (target === undefined) {
    let best = ''
    for (const pattern of Object.keys(exportsMap)) {
      const i = pattern.indexOf('*')
      if (i < 0) continue
      const prefix = pattern.slice(0, i)
      const suffix = pattern.slice(i + 1)
      if (key.startsWith(prefix) && key.endsWith(suffix) && key.length >= prefix.length + suffix.length && prefix.length > best.length) {
        best = prefix
        target = exportsMap[pattern]
        star = key.slice(prefix.length, key.length - suffix.length)
      }
    }
  }
  if (target === undefined) return undefined
  const first = Array.isArray(target) ? target[0] : target
  return join(k8sUiRoot, star === undefined ? first : first.split('*').join(star))
}

// Everything radar-app publishes that can import from k8s-ui, stylesheets
// included (vitest doesn't load CSS, so these are read from disk).
function sourceFiles(dir: string): string[] {
  return readdirSync(dir).flatMap((name) => {
    const path = join(dir, name)
    if (statSync(path).isDirectory()) return sourceFiles(path)
    return /\.(ts|tsx|css)$/.test(name) && !/\.test\.(ts|tsx)$/.test(name) ? [path] : []
  })
}

describe('@skyhook-io/k8s-ui deep imports', () => {
  const imports = new Set<string>()
  for (const file of sourceFiles(here)) {
    // Any quoted specifier: `from` imports, side-effect and dynamic imports,
    // and stylesheet @imports all resolve through the same map.
    for (const m of readFileSync(file, 'utf8').matchAll(/['"]@skyhook-io\/k8s-ui\/([^'"]+)['"]/g)) {
      imports.add(m[1])
    }
  }

  test('finds the deep imports it checks, stylesheets included', () => {
    expect(imports.size).toBeGreaterThan(10)
    expect(imports).toContain('theme/variables.css')
  })

  test.each([...imports].sort())('%s resolves through the published exports map', (subpath) => {
    const file = resolveExport(subpath)
    expect(file, `no exports entry serves "@skyhook-io/k8s-ui/${subpath}"`).toBeDefined()
    expect(existsSync(file!), `"@skyhook-io/k8s-ui/${subpath}" resolves to missing ${file}`).toBe(true)
  })
})
