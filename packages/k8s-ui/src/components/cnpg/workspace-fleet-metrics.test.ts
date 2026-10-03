import { describe, it, expect } from 'vitest'
import { applyCNPGFleetMetrics, buildCNPGFleet, CNPG_WORKSPACE_KEYS, type CNPGFleetMetricsReading, type CNPGWorkspaceResponse } from './workspace'

function pod(cluster: string, n: number, role: 'primary' | 'replica'): any {
  return {
    metadata: { name: `${cluster}-${n}`, namespace: 'db', labels: { 'cnpg.io/cluster': cluster, 'cnpg.io/instanceRole': role } },
    status: { conditions: [{ type: 'Ready', status: 'True' }] },
  }
}

function fleet() {
  const coverage: CNPGWorkspaceResponse['coverage'] = {}
  for (const k of CNPG_WORKSPACE_KEYS) coverage[k] = { state: 'full' }
  const cluster = (name: string, instances: number) => ({
    apiVersion: 'postgresql.cnpg.io/v1',
    kind: 'Cluster',
    metadata: { name, namespace: 'db' },
    spec: { instances },
    status: { phase: 'Cluster in healthy state', readyInstances: instances, currentPrimary: `${name}-1` },
  })
  return buildCNPGFleet({
    installed: true,
    context: 'test',
    namespaces: null,
    coverage,
    objects: {
      clusters: [cluster('ha', 2), cluster('solo', 1), cluster('dark', 2)],
      pods: [pod('ha', 1, 'primary'), pod('ha', 2, 'replica'), pod('solo', 1, 'primary'), pod('dark', 1, 'primary'), pod('dark', 2, 'replica')],
    },
    issues: [],
    audit: [],
    backupsOmitted: 0,
  })
}

const reading = (name: string, lag: CNPGFleetMetricsReading['lag'], growth: CNPGFleetMetricsReading['growth'] = { state: 'noSeries' }): CNPGFleetMetricsReading => ({
  namespace: 'db',
  name,
  lag,
  growth,
})

const row = (f: ReturnType<typeof fleet>, name: string) => f.rows.find((r) => r.name === name)!

describe('applyCNPGFleetMetrics', () => {
  it('replaces "lag unknown" with the measured standby lag and names its source', () => {
    const f = applyCNPGFleetMetrics(fleet(), [reading('ha', { state: 'ok', seconds: 7.2, pod: 'ha-2' })], { source: 'prometheus', lagSource: 'Prometheus cnpg_pg_replication_lag' })
    const r = row(f, 'ha')
    expect(r.replication.text).toBe('1/1 ready · lag 7.2 s')
    expect(r.replication.tone).toBe('degraded')
    expect(r.replication.source).toContain('ha-2')
    expect(r.replication.source).toContain('cnpg_pg_replication_lag')
  })

  it('says why lag is unknown instead of zero, and leaves non-replica facts alone', () => {
    const f = applyCNPGFleetMetrics(fleet(), [reading('dark', { state: 'noSeries', reason: 'no exporter series' })], { source: 'prometheus' })
    expect(row(f, 'dark').replication.text).toBe('1/1 ready · lag unknown')
    expect(row(f, 'dark').replication.source).toBeTruthy()
    expect(row(f, 'dark').replication.tone).toBe('unknown')
    expect(row(f, 'solo').replication.text).toBe('Single instance')

    const denied = applyCNPGFleetMetrics(fleet(), [reading('ha', { state: 'denied', grant: 'get pods in db' })], { source: 'prometheus' })
    expect(row(denied, 'ha').replication.text).toBe('1/1 ready · lag unknown')
    expect(row(denied, 'ha').replication.source).toBe('Needs get pods in db')

    const none = applyCNPGFleetMetrics(fleet(), undefined, { source: 'none', reason: 'Radar is not connected to Prometheus' })
    expect(row(none, 'ha').replication.text).toBe('1/1 ready · lag unknown')
    expect(row(none, 'ha').replication.source).toBe('Prometheus not connected')
    expect(row(none, 'ha').replication.detail).toBe('Radar is not connected to Prometheus')
    expect(row(none, 'ha').diskGrowth).toBeUndefined()
  })

  it('keeps the fleet untouched when no reading was requested', () => {
    const base = fleet()
    expect(applyCNPGFleetMetrics(base, undefined, undefined)).toBe(base)
    expect(row(base, 'ha').replication.text).toBe('1/1 ready · lag unknown')
  })

  it('reports disk growth only when measured', () => {
    const f = applyCNPGFleetMetrics(
      fleet(),
      [reading('ha', { state: 'ok', seconds: 0.2, pod: 'ha-2' }, { state: 'ok', bytesPerHour: 1024 ** 3 / 24, claim: 'ha-1', instance: 'ha-1' })],
      { source: 'prometheus', growthSource: 'deriv' },
    )
    expect(row(f, 'ha').diskGrowth?.text).toBe('+1 GB/day')
    expect(row(f, 'ha').replication.tone).toBe('healthy')
    expect(row(f, 'dark').diskGrowth).toBeUndefined()
  })
})

describe('sustained replication lag', () => {
  const src = { source: 'prometheus' as const, lagSource: 'Prometheus cnpg_pg_replication_lag' }

  it('puts a cluster into Needs attention only when lag stayed high for the whole window', () => {
    const f = applyCNPGFleetMetrics(
      fleet(),
      [
        reading('ha', { state: 'ok', seconds: 95, pod: 'ha-2', sustainedSeconds: 40, sustainedPod: 'ha-2', sustainedWindow: '10m0s' }),
        reading('dark', { state: 'ok', seconds: 120, pod: 'dark-2' }),
      ],
      src,
    )
    const ha = row(f, 'ha')
    expect(ha.attention).toBe(true)
    expect(ha.problems[0]).toMatchObject({ severity: 'warning', category: 'availability', source: 'measurement' })
    expect(ha.problems[0].title).toBe('ha-2 ≥ 40 s behind in every sample for 10 min')
    expect(ha.problems[0]).toMatchObject({ measuredBy: 'Prometheus' })
    expect(ha.problems[0].detail).not.toMatch(/cnpg_/)
    expect(ha.problems[0].detail).toContain("If Prometheus missed some scrapes, those moments aren't included")
    expect(ha.problems[0].shortTitle).toBe('ha-2: all samples ≥ 40 s behind (10 min)')
    expect(ha.problems[0].detail).not.toContain('whole window')
    expect(row(f, 'dark').attention).toBe(false)
    expect(f.attentionCount).toBe(1)
  })

  it('says when the series were matched to the cluster by Pod name only', () => {
    const note = "Matched by namespace and Pod names. Radar couldn't confirm these series belong to this exact cluster (no cluster label it could check)"
    const f = applyCNPGFleetMetrics(
      fleet(),
      [reading('ha', { state: 'ok', seconds: 95, pod: 'ha-2', sustainedSeconds: 40, sustainedPod: 'ha-2', sustainedWindow: '10m0s', isolation: { mode: 'unverified', note } })],
      src,
    )
    const p = row(f, 'ha').problems[0]
    expect(p.measuredBy).toBe('Prometheus, matched by Pod name')
    expect(p.detail).toContain(note)
    const verified = applyCNPGFleetMetrics(
      fleet(),
      [reading('ha', { state: 'ok', seconds: 95, pod: 'ha-2', sustainedSeconds: 40, sustainedPod: 'ha-2', sustainedWindow: '10m0s', isolation: { mode: 'verified', note: 'x' } })],
      src,
    )
    expect(row(verified, 'ha').problems[0].measuredBy).toBe('Prometheus')
  })

  it('escalates to critical past five minutes and ignores a floor under 30 s', () => {
    const f = applyCNPGFleetMetrics(
      fleet(),
      [
        reading('ha', { state: 'ok', seconds: 400, pod: 'ha-2', sustainedSeconds: 360, sustainedPod: 'ha-2', sustainedWindow: '10m0s' }),
        reading('dark', { state: 'ok', seconds: 20, pod: 'dark-2', sustainedSeconds: 12, sustainedPod: 'dark-2', sustainedWindow: '10m0s' }),
      ],
      src,
    )
    expect(row(f, 'ha').problems[0]).toMatchObject({ severity: 'critical', title: 'ha-2 ≥ 6 min behind in every sample for 10 min' })
    expect(row(f, 'dark').problems).toHaveLength(0)
  })
})
