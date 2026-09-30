import { describe, it, expect } from 'vitest'
import { applyCNPGDisk, buildCNPGFleet, cnpgDiskFact, CNPG_WORKSPACE_KEYS, type CNPGDiskReading, type CNPGWorkspaceResponse } from './workspace'

function cluster(name: string): any {
  return {
    apiVersion: 'postgresql.cnpg.io/v1',
    kind: 'Cluster',
    metadata: { name, namespace: 'db' },
    spec: { instances: 1 },
    status: { phase: 'Cluster in healthy state', readyInstances: 1, currentPrimary: `${name}-1` },
  }
}

function fleetOf(...names: string[]) {
  const coverage: CNPGWorkspaceResponse['coverage'] = {}
  for (const k of CNPG_WORKSPACE_KEYS) coverage[k] = { state: 'full' }
  return buildCNPGFleet({
    installed: true,
    context: 'test',
    namespaces: null,
    coverage,
    objects: { clusters: names.map(cluster) },
    issues: [],
    audit: [],
    backupsOmitted: 0,
  })
}

function reading(name: string, over: Partial<CNPGDiskReading> = {}): CNPGDiskReading {
  return { namespace: 'db', name, state: 'ok', claims: 1, measured: 1, ...over }
}

const max = (ratio: number) => ({ claim: 'pg-1', instance: 'pg-1', role: 'PG_DATA', usedBytes: ratio * 10 * 1024 ** 3, capacityBytes: 10 * 1024 ** 3, ratio })

describe('cnpgDiskFact', () => {
  it('never reads an unmeasured cluster as zero or healthy', () => {
    for (const state of ['noSeries', 'noPrometheus', 'denied', 'error', 'notRead']) {
      const f = cnpgDiskFact(reading('pg', { state, measured: 0 }))
      expect(f.tone).toBe('unknown')
      expect(f.text).not.toMatch(/\d/)
    }
    expect(cnpgDiskFact(undefined).tone).toBe('unknown')
  })

  it('names the fullest volume and its source, and labels partial coverage', () => {
    const f = cnpgDiskFact(reading('pg', { state: 'partial', claims: 2, measured: 1, max: max(0.5) }))
    expect(f.text).toBe('50% used')
    expect(f.tone).toBe('healthy')
    expect(f.source).toContain('data volume of pg-1')
    expect(f.source).toContain('kubelet volume stats')
    expect(f.source).toContain('1 of 2 volumes measured')
  })

  it('names the missing grant when denied', () => {
    expect(cnpgDiskFact(reading('pg', { state: 'denied', grant: 'list persistentvolumeclaims in db', measured: 0 })).source).toBe('Needs list persistentvolumeclaims in db')
  })
})

describe('applyCNPGDisk', () => {
  it('puts low-disk clusters into Needs attention by severity', () => {
    const fleet = applyCNPGDisk(fleetOf('pg-a', 'pg-b', 'pg-c'), [
      reading('pg-a', { max: max(0.85) }),
      reading('pg-b', { max: max(0.95) }),
      reading('pg-c', { max: max(0.4) }),
    ])
    expect(fleet.attentionCount).toBe(2)
    const a = fleet.rows.find((r) => r.name === 'pg-a')!
    const b = fleet.rows.find((r) => r.name === 'pg-b')!
    const c = fleet.rows.find((r) => r.name === 'pg-c')!
    expect(a.problems[0].severity).toBe('warning')
    expect(b.problems[0].severity).toBe('critical')
    expect(b.problems[0].source).toBe('measurement')
    expect(b.problems[0].title).toBe('The data volume of pg-1 is 95% full')
    expect(c.attention).toBe(false)
    expect(fleet.categoryCounts.availability).toBe(2)
  })

  it('raises nothing without a measurement', () => {
    const fleet = applyCNPGDisk(fleetOf('pg-a'), [reading('pg-a', { state: 'noPrometheus', measured: 0, reason: 'Radar is not connected to Prometheus: x' })])
    expect(fleet.attentionCount).toBe(0)
    expect(fleet.rows[0].disk).toMatchObject({ text: 'No usage metrics', source: 'Prometheus not connected', detail: 'Radar is not connected to Prometheus: x' })
  })

  it('leaves the fleet as built when no reading was requested', () => {
    const base = fleetOf('pg-a')
    expect(applyCNPGDisk(base, undefined)).toBe(base)
  })
})
