import { describe, expect, it } from 'vitest'
import { columnSettingsKey, hasCuratedColumns } from './ResourcesView'
import { getCellFilterValue } from './resource-utils'
import { getGenericResourceStatus } from './generic-status'

describe('columnSettingsKey', () => {
  // Two uncurated CRDs sharing a plural across API groups — Crossplane ships
  // two `Usage` kinds — shared one blob and leaked column visibility, widths
  // and custom columns between them.
  it('qualifies uncurated CRDs by their API group', () => {
    expect(columnSettingsKey('usages', 'apiextensions.crossplane.io'))
      .not.toBe(columnSettingsKey('usages', 'protection.crossplane.io'))
    expect(columnSettingsKey('usages', 'apiextensions.crossplane.io'))
      .toBe('usages.apiextensions.crossplane.io')
  })

  // Curated and core kinds keep the key their saved preferences are already
  // under, so nobody's columns reset on upgrade.
  it('leaves core kinds alone', () => {
    expect(columnSettingsKey('pods')).toBe('pods')
    expect(columnSettingsKey('deployments', 'apps')).toBe('deployments')
  })

  it('leaves curated CRDs alone', () => {
    expect(columnSettingsKey('rollouts', 'argoproj.io')).toBe('rollouts')
  })

  // Crossplane managed resources are curated through a group heuristic, not a
  // KNOWN_COLUMNS entry — curation has to be read from getColumnsForKind, not
  // from key membership.
  it('leaves Crossplane managed resources alone', () => {
    const key = columnSettingsKey('buckets', 's3.aws.upbound.io')
    expect(key).toBe('buckets')
  })
})

describe('hasCuratedColumns', () => {
  // One definition shared by the XOR, the storage key, and the host's decision
  // whether to request a table. If they disagreed, the server would resolve a
  // table the client discards — or two of them would put a kind on different
  // column sets.
  it('recognizes curated kinds, including group-routed and heuristic ones', () => {
    expect(hasCuratedColumns('pods')).toBe(true)
    expect(hasCuratedColumns('rollouts', 'argoproj.io')).toBe(true)
    expect(hasCuratedColumns('clusters', 'postgresql.cnpg.io')).toBe(true)
    // Crossplane managed resources are curated by group heuristic, not plural.
    expect(hasCuratedColumns('buckets', 's3.aws.upbound.io')).toBe(true)
  })

  it('reports uncurated CRDs as uncurated', () => {
    expect(hasCuratedColumns('usages', 'protection.crossplane.io')).toBe(false)
    expect(hasCuratedColumns('widgets', 'example.com')).toBe(false)
  })

  it('agrees with columnSettingsKey about which kinds get group-qualified', () => {
    for (const [kind, group] of [['usages', 'protection.crossplane.io'], ['widgets', 'example.com']] as const) {
      expect(hasCuratedColumns(kind, group)).toBe(false)
      expect(columnSettingsKey(kind, group)).toContain(group)
    }
    for (const [kind, group] of [['rollouts', 'argoproj.io'], ['buckets', 's3.aws.upbound.io']] as const) {
      expect(hasCuratedColumns(kind, group)).toBe(true)
      expect(columnSettingsKey(kind, group)).not.toContain(group)
    }
  })
})

describe('status sort scope', () => {
  const pod = {
    status: {
      phase: 'Running',
      conditions: [{ type: 'Ready', status: 'False' }],
      containerStatuses: [{ name: 'app', ready: false, restartCount: 3, state: { running: {} } }],
    },
    spec: { containers: [{ name: 'app' }] },
  }
  const uncuratedCR = { status: { conditions: [{ type: 'Ready', status: 'False', reason: 'QuorumLost' }] } }

  // Curated kinds keep phase-based ordering: routing them through the per-kind
  // readers would reorder the most-used lists for a divergence that predates
  // this work.
  it('leaves a curated kind sorting on its phase', () => {
    expect(hasCuratedColumns('pods')).toBe(true)
    expect(pod.status.phase).toBe('Running')
    // The displayed text differs from the phase — that gap is intentional here.
    expect(getCellFilterValue(pod, 'status', 'pods')).not.toBe('Running')
  })

  // An uncurated CR has no phase at all, so phase sorted every row as equal.
  it('sorts an uncurated kind on the text its badge shows', () => {
    expect(hasCuratedColumns('widgets', 'example.com')).toBe(false)
    expect((uncuratedCR.status as any).phase).toBeUndefined()
    const shown = getGenericResourceStatus(uncuratedCR)?.text
    expect(shown).toBe('QuorumLost')
    // Sort and the filter dropdown must offer the same string, or the dropdown
    // lists values that appear on no row.
    expect(getCellFilterValue(uncuratedCR, 'status', 'widgets')).toBe(shown)
  })
})
