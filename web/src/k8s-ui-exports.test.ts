import { describe, expect, test } from 'vitest'
import k8sUiPackage from '../../packages/k8s-ui/package.json'

// Radar's own build aliases @skyhook-io/k8s-ui to its source, so a deep
// import that the package's `exports` map can't serve still builds here and
// only fails in a consumer that installs the published packages (Radar Hub).
// This resolves every deep import the way Node and bundlers do: the most
// specific matching key wins, and an array target resolves to its first entry
// without falling back when that file doesn't exist.

const exportsMap = k8sUiPackage.exports as Record<string, string | string[]>
const k8sUiFiles = new Set(
  Object.keys(import.meta.glob('../../packages/k8s-ui/src/**/*')).map((p) => p.replace('../../packages/k8s-ui/', './')),
)
const sources = import.meta.glob(['./**/*.{ts,tsx}', '!./**/*.test.{ts,tsx}'], {
  query: '?raw',
  import: 'default',
  eager: true,
}) as Record<string, string>

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
  return star === undefined ? first : first.replace('*', star)
}

describe('@skyhook-io/k8s-ui deep imports', () => {
  const imports = new Set<string>()
  for (const source of Object.values(sources)) {
    for (const m of source.matchAll(/from\s+['"]@skyhook-io\/k8s-ui\/([^'"]+)['"]/g)) {
      imports.add(m[1])
    }
  }

  test('finds the deep imports it checks', () => {
    expect(imports.size).toBeGreaterThan(10)
  })

  test.each([...imports].sort())('%s resolves through the published exports map', (subpath) => {
    const file = resolveExport(subpath)
    expect(file, `no exports entry serves "@skyhook-io/k8s-ui/${subpath}"`).toBeDefined()
    expect(k8sUiFiles.has(file!), `"@skyhook-io/k8s-ui/${subpath}" resolves to missing ${file}`).toBe(true)
  })
})
