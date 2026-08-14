#!/usr/bin/env node
// Fetches the upstream CRDs listed in scripts/crd-sources.json and distills each
// one into the set of field paths it declares, written to testdata/crd-schemas/.
//
// The distilled sets are committed so the conformance test runs offline and
// deterministically in CI; only this script touches the network. Bumping a pin
// in crd-sources.json and re-running produces a reviewable diff of exactly which
// upstream fields appeared or disappeared.
//
//   node scripts/crd-schema-sync.mjs            # all integrations
//   node scripts/crd-schema-sync.mjs velero     # one integration

import { readFileSync, writeFileSync, mkdirSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import YAML from 'yaml'

const repoRoot = join(dirname(fileURLToPath(import.meta.url)), '..')
const outDir = join(repoRoot, 'testdata', 'crd-schemas')

// A subtree the CRD declines to describe. Anything below one of these is
// unverifiable rather than wrong: `additionalProperties` is a free-form map
// (velero BSL's spec.config), and preserve-unknown-fields is an explicit
// opt-out of schema validation (Crossplane, Argo Workflows).
function isOpaque(node) {
  return (
    node['x-kubernetes-preserve-unknown-fields'] === true ||
    typeof node.additionalProperties === 'object' ||
    node.additionalProperties === true ||
    (node.type === 'object' && !node.properties && !node.allOf && !node.oneOf)
  )
}

// Walks an openAPIV3Schema collecting dotted paths. Array items contribute the
// same path as their parent, because accessors index into arrays and then read
// fields off the element (`status.conditions.type`), and the distinction between
// "field on the array" and "field on its element" is not one the accessor makes.
function collectPaths(node, prefix, paths, opaque) {
  if (!node || typeof node !== 'object') return

  if (isOpaque(node) && prefix) {
    opaque.add(prefix)
    return
  }

  for (const [name, child] of Object.entries(node.properties ?? {})) {
    const path = prefix ? `${prefix}.${name}` : name
    paths.add(path)
    collectPaths(child, path, paths, opaque)
  }

  if (node.items) collectPaths(node.items, prefix, paths, opaque)
  for (const branch of node.allOf ?? node.oneOf ?? node.anyOf ?? []) {
    collectPaths(branch, prefix, paths, opaque)
  }
}

// metadata is `type: object` with no properties in every CRD (the apiserver
// substitutes ObjectMeta), so the walk above yields nothing for it. Accessors
// legitimately read the standard fields, so declare them rather than forcing
// every integration to allowlist `metadata.name`.
const OBJECT_META = [
  'metadata',
  'metadata.name',
  'metadata.namespace',
  'metadata.uid',
  'metadata.labels',
  'metadata.annotations',
  'metadata.creationTimestamp',
  'metadata.deletionTimestamp',
  'metadata.generation',
  'metadata.resourceVersion',
  'metadata.ownerReferences',
  'metadata.finalizers',
]

function parseCRDs(text) {
  return YAML.parseAllDocuments(text)
    .map((d) => d.toJS({ maxAliasCount: -1 }))
    .filter((d) => d && d.kind === 'CustomResourceDefinition')
}

async function fetchText(url) {
  const res = await fetch(url, { redirect: 'follow' })
  if (!res.ok) throw new Error(`${res.status} ${res.statusText} for ${url}`)
  return res.text()
}

async function sync(key, entry) {
  const paths = new Set(OBJECT_META)
  const opaque = new Set()
  const kinds = []

  for (const url of entry.urls) {
    const crds = parseCRDs(await fetchText(url))
    if (crds.length === 0) throw new Error(`no CustomResourceDefinition in ${url}`)
    for (const crd of crds) {
      const group = crd.spec?.group ?? ''
      for (const version of crd.spec?.versions ?? []) {
        const schema = version.schema?.openAPIV3Schema
        if (!schema) continue
        collectPaths(schema, '', paths, opaque)
        kinds.push(`${group}/${version.name}/${crd.spec.names.kind}`)
      }
    }
  }

  const doc = {
    _generated: 'scripts/crd-schema-sync.mjs — do not edit by hand',
    integration: key,
    version: entry.version,
    accessors: entry.accessors,
    sources: entry.urls,
    kinds: [...new Set(kinds)].sort(),
    // Paths below an opaque subtree are unverifiable, not invalid.
    opaqueSubtrees: [...opaque].sort(),
    paths: [...paths].sort(),
  }

  mkdirSync(outDir, { recursive: true })
  writeFileSync(join(outDir, `${key}.json`), JSON.stringify(doc, null, 2) + '\n')
  return doc
}

const manifest = JSON.parse(readFileSync(join(repoRoot, 'scripts', 'crd-sources.json'), 'utf8'))
const only = process.argv.slice(2)
const selected = Object.entries(manifest.integrations).filter(
  ([key]) => only.length === 0 || only.includes(key),
)

if (selected.length === 0) {
  console.error(`no integration matched: ${only.join(', ')}`)
  process.exit(1)
}

let failed = 0
for (const [key, entry] of selected) {
  try {
    const doc = await sync(key, entry)
    console.log(
      `${key.padEnd(14)} ${String(doc.paths.length).padStart(5)} paths  ` +
        `${String(doc.kinds.length).padStart(3)} kinds  ${doc.version}`,
    )
  } catch (err) {
    console.error(`${key.padEnd(14)} FAILED: ${err.message}`)
    failed++
  }
}
process.exit(failed > 0 ? 1 : 0)
