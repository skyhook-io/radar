import { readFileSync } from 'fs'
import { join } from 'path'
import { describe, expect, it } from 'vitest'
import { hasCuratedColumns } from './ResourcesView'

type RegistryRow = {
  logicalId: string
  group: string
  plural: string
  kind: string
  scope: string
  apiVersion: string
  scenario: string
  crdUrl: string
}

const REGISTRY_PATH = join(__dirname, '../../../../../scripts/gpu-ecosystem-demo/resources.tsv')
const [header, ...lines] = readFileSync(REGISTRY_PATH, 'utf8').trim().split('\n')

if (header.replace(/^# /, '') !== 'logical_id\tgroup\tplural\tkind\tscope\tapi_version\tscenario\tcrd_url') {
  throw new Error(`Unexpected GPU ecosystem registry header: ${header}`)
}

const rows: RegistryRow[] = lines.map(line => {
  const [logicalId, group, plural, kind, scope, apiVersion, scenario, crdUrl] = line.split('\t')
  return { logicalId, group, plural, kind, scope, apiVersion, scenario, crdUrl }
})

const rowsFor = (scenario: 'current' | 'experimental') =>
  rows.filter(row => row.scenario === 'all' || row.scenario === scenario)

describe.each(['current', 'experimental'] as const)('GPU ecosystem %s registry', scenario => {
  const scenarioRows = rowsFor(scenario)

  it('resolves to exactly 37 distinct logical resource identities', () => {
    expect(scenarioRows).toHaveLength(37)
    expect(new Set(scenarioRows.map(row => row.logicalId)).size).toBe(37)
  })

  it('maps every registered upstream resource to curated Radar columns', () => {
    const uncurated = scenarioRows
      .filter(row => !hasCuratedColumns(row.plural, row.group))
      .map(row => `${row.plural}.${row.group}`)

    expect(uncurated).toEqual([])
  })

  it('uses pinned tagged upstream CRDs', () => {
    const unpinned = scenarioRows
      .filter(row => !/^(v?\d|cluster-autoscaler-\d)/.test(new URL(row.crdUrl).pathname.split('/')[3] ?? ''))
      .map(row => row.crdUrl)

    expect(unpinned).toEqual([])
  })
})

it('changes only the two inference-routing identities between scenarios', () => {
  const current = new Map(rowsFor('current').map(row => [row.logicalId, `${row.plural}.${row.group}`]))
  const experimental = new Map(rowsFor('experimental').map(row => [row.logicalId, `${row.plural}.${row.group}`]))
  const changed = [...current].filter(([id, resource]) => experimental.get(id) !== resource).map(([id]) => id)

  expect(changed).toEqual(['inferencepool', 'inferenceobjective'])
})
