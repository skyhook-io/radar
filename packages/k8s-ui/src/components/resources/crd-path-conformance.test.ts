import { describe, it, expect } from 'vitest'
import { readFileSync, readdirSync, existsSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { dirname, resolve, join } from 'node:path'
import ts from 'typescript'

// Every `resource-utils-<vendor>.ts` reads third-party CRDs off an `any`, by
// string path. A path that does not exist yields `undefined` rather than an
// error, so a mistyped field silently disables whatever it feeds — a replica
// cluster that never renders as one, a badge that never fires.
//
// This asserts each path against the vendor's published openAPIV3Schema,
// distilled into testdata/crd-schemas/ by scripts/crd-schema-sync.mjs. It
// checks SHAPE only: that the field exists. Whether the accessor reads the
// right field for the right reason is what the golden matrices are for.
//
// Adding an integration: add it to scripts/crd-sources.json, run the sync, and
// commit the generated file. Integrations with no committed schema are skipped
// rather than failed, so the check adopts incrementally.

const here = dirname(fileURLToPath(import.meta.url))
const schemaDir = resolve(here, '../../../../../testdata/crd-schemas')
const allowlistPath = join(schemaDir, 'allowlist.json')

interface CRDSchema {
  integration: string
  version: string
  accessors: string
  kinds: string[]
  opaqueSubtrees: string[]
  paths: string[]
}

interface AllowEntry {
  integration: string
  path: string
  reason: string
}

/** Roots that denote the resource itself rather than some nested value. */
const ROOT_SEGMENTS = new Set(['spec', 'status', 'metadata'])

/**
 * Trailing segments that are JavaScript, not schema. Array methods are already
 * excluded as call callees; `length` is a property read and would otherwise
 * look like a field. A CRD field genuinely named `length` would be missed —
 * accepted, because a false positive here makes the whole check ignorable.
 */
const JS_INTRINSIC_TAIL = new Set(['length'])

interface FoundPath {
  path: string
  line: number
}

/**
 * Collects `spec.*` / `status.*` / `metadata.*` chains read off each exported
 * function's first parameter.
 *
 * Locals are followed one hop (`const r = resource.spec?.replica` then
 * `r.source` yields `spec.replica.source`), which is where the deeper paths
 * live — accessors rarely spell a four-segment chain inline.
 */
function extractPaths(source: string, fileName: string): FoundPath[] {
  const sf = ts.createSourceFile(fileName, source, ts.ScriptTarget.Latest, true)
  const found: FoundPath[] = []

  // Chain root -> dotted prefix. Seeded with each function's own parameters
  // (prefix '') and grown as locals alias into the resource.
  const walkFunction = (fn: ts.FunctionLikeDeclaration) => {
    const roots = new Map<string, string>()
    for (const p of fn.parameters) {
      if (ts.isIdentifier(p.name)) roots.set(p.name.text, '')
    }
    if (roots.size === 0) return

    // Resolves a member-access chain to a dotted path, or null if it is not
    // rooted at something we track.
    const resolve = (node: ts.Expression): string | null => {
      if (ts.isIdentifier(node)) return roots.get(node.text) ?? null
      if (ts.isNonNullExpression(node) || ts.isParenthesizedExpression(node)) {
        return resolve(node.expression)
      }
      if (ts.isPropertyAccessExpression(node)) {
        const base = resolve(node.expression)
        if (base === null) return null
        return base ? `${base}.${node.name.text}` : node.name.text
      }
      if (ts.isElementAccessExpression(node)) {
        const base = resolve(node.expression)
        if (base === null) return null
        const arg = node.argumentExpression
        // A literal key is part of the path; a computed index is not, and the
        // element shares its parent's path (`conditions[i].type`).
        if (arg && ts.isStringLiteralLike(arg)) return base ? `${base}.${arg.text}` : arg.text
        return base
      }
      return null
    }

    // `conditions.find(...)` is a JS array method, not a CRD field. Skip the
    // callee of a call expression; its base has already been visited on the
    // way down, so the real path is still collected.
    const isCallee = (node: ts.Node) =>
      node.parent && ts.isCallExpression(node.parent) && node.parent.expression === node

    const visit = (node: ts.Node) => {
      if (ts.isVariableDeclaration(node) && node.initializer && ts.isIdentifier(node.name)) {
        const chain = resolve(node.initializer as ts.Expression)
        if (chain) roots.set(node.name.text, chain)
      }
      if (
        (ts.isPropertyAccessExpression(node) || ts.isElementAccessExpression(node)) &&
        !isCallee(node)
      ) {
        const chain = resolve(node as ts.Expression)
        const segments = chain?.split('.') ?? []
        if (
          chain &&
          segments.length > 1 &&
          ROOT_SEGMENTS.has(segments[0]) &&
          !JS_INTRINSIC_TAIL.has(segments[segments.length - 1])
        ) {
          found.push({
            path: chain,
            line: sf.getLineAndCharacterOfPosition(node.getStart(sf)).line + 1,
          })
        }
      }
      ts.forEachChild(node, visit)
    }
    ts.forEachChild(fn, visit)
  }

  const topLevel = (node: ts.Node) => {
    if (ts.isFunctionDeclaration(node) && node.body) walkFunction(node)
    ts.forEachChild(node, topLevel)
  }
  ts.forEachChild(sf, topLevel)
  return found
}

function loadSchemas(): CRDSchema[] {
  if (!existsSync(schemaDir)) return []
  return readdirSync(schemaDir)
    .filter((f) => f.endsWith('.json') && f !== 'allowlist.json')
    .map((f) => JSON.parse(readFileSync(join(schemaDir, f), 'utf8')) as CRDSchema)
}

const allowlist: AllowEntry[] = existsSync(allowlistPath)
  ? JSON.parse(readFileSync(allowlistPath, 'utf8')).allow
  : []

const schemas = loadSchemas()

describe('CRD accessor paths match the published schema', () => {
  it('has at least one distilled schema to check against', () => {
    expect(schemas.length).toBeGreaterThan(0)
  })

  it.each(schemas.map((s) => [s.integration, s] as const))('%s', (_name, schema) => {
    const accessorFile = join(here, schema.accessors)
    expect(existsSync(accessorFile), `${schema.accessors} not found`).toBe(true)

    const declared = new Set(schema.paths)
    const opaque = schema.opaqueSubtrees
    const allowed = new Set(
      allowlist.filter((a) => a.integration === schema.integration).map((a) => a.path),
    )

    const unknown = extractPaths(readFileSync(accessorFile, 'utf8'), accessorFile).filter((f) => {
      if (declared.has(f.path) || allowed.has(f.path)) return false
      // Anything under a subtree the CRD declines to describe is unverifiable.
      return !opaque.some((o) => f.path === o || f.path.startsWith(`${o}.`))
    })

    // Deduplicate: one wrong constant tends to appear on several lines.
    const byPath = new Map<string, number[]>()
    for (const u of unknown) byPath.set(u.path, [...(byPath.get(u.path) ?? []), u.line])

    const report = [...byPath.entries()]
      .sort()
      .map(([p, lines]) => `  ${schema.accessors}:${lines.join(',')}  ${p}`)
      .join('\n')

    expect(
      byPath.size,
      `${schema.integration} (${schema.version}) reads ${byPath.size} path(s) its CRDs do not declare:\n${report}\n\n` +
        `Fix the accessor, or — if the field is real but newer than the pin — bump the version in\n` +
        `scripts/crd-sources.json and re-run: node scripts/crd-schema-sync.mjs ${schema.integration}\n` +
        `If it is genuinely unverifiable, add it to testdata/crd-schemas/allowlist.json with a reason.`,
    ).toBe(0)
  })
})
