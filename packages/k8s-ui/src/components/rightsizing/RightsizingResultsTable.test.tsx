import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it } from 'vitest'
import type { DatadogSizingWorkload, RightsizingRow } from '../../types/rightsizing'
import { RightsizingResultsTable } from './RightsizingResultsTable'
import vectors from '../../../../../pkg/rightsizing/testdata/decisions.json'
import { RightsizingEvidence } from './RightsizingEvidence'
import { rightsizingPolicyCoverage, workloadResultRows } from './results-model'

type DatadogRow = DatadogSizingWorkload['rows'][number]

const cpu: DatadogRow = {
  decision: { status: 'actionable' },
  container: 'app',
  resource: 'cpu',
  fit: 'oversized',
  confidence: 'high',
  currentRequest: '500m',
  currentRequestValue: 0.5,
  recommendedRequest: '200m',
  recommendedRequestValue: 0.2,
  observed: { name: 'P95', value: 0.15, formatted: '150m' },
  sampleCount: 1512,
  expectedSamples: 2016,
  coverage: 0.75,
  hpaManaged: false,
  hpaEvidenceAvailable: true,
  oomEvidenceAvailable: true,
}
const workload: DatadogSizingWorkload = {
  kind: 'Deployment',
  name: 'checkout',
  namespace: 'payments',
  replicas: 2,
  rows: [cpu],
  scaledToZero: false,
  window: '7d',
  ownerCoverage: 'current_pods',
  source: 'datadog',
  sampleAvailable: true,
}

describe('operator rightsizing results across clusters', () => {
  it.each([
    { resource: 'cpu', current: 0.3, target: 0.276, expected: 'in_range' },
    { resource: 'cpu', current: 0.3, target: 0.275, expected: 'reduction' },
    { resource: 'cpu', current: 0.3, target: 0.274, expected: 'reduction' },
    { resource: 'memory', current: 128 * 1024 ** 2, target: 97 * 1024 ** 2, expected: 'in_range' },
    { resource: 'memory', current: 128 * 1024 ** 2, target: 96 * 1024 ** 2, expected: 'reduction' },
    { resource: 'memory', current: 128 * 1024 ** 2, target: 95 * 1024 ** 2, expected: 'reduction' },
    { resource: 'cpu', current: 0.3, target: 0.301, expected: 'increase' },
  ] as const)('classifies $resource $current → $target across two replicas as $expected for both sources', ({
    resource, current, target, expected,
  }) => {
    const row: DatadogRow = {
      ...cpu, resource, currentRequestValue: current, recommendedRequestValue: target,
      fit: target > current ? 'under_requested' : 'oversized',
      currentRequest: resource === 'cpu' ? `${Math.round(current * 1000)}m` : `${current / 1024 ** 2}Mi`,
      recommendedRequest: resource === 'cpu' ? `${Math.round(target * 1000)}m` : `${target / 1024 ** 2}Mi`,
    }
    const cluster = { id: 'east', name: 'East' }
    const { kind, namespace, name, replicas, scaledToZero } = workload
    const prometheus = workloadResultRows({ kind, namespace, name, replicas, scaledToZero, rows: [row] }, cluster)
    const datadog = workloadResultRows({
      ...workload, rows: [row],
    }, cluster)
    for (const rows of [prometheus, datadog]) {
      expect(rows[0].classification).toBe(expected)
      if (expected === 'in_range') {
        const html = renderToStaticMarkup(<RightsizingResultsTable rows={rows} openRowId={null}
          onOpenRowChange={() => {}} renderWorkloadLink={() => null} />)
        expect(html).toContain('Below action threshold')
      }
    }
  })

  it('keeps a container in manual review when the other resource qualifies for a reduction', () => {
    const memory: DatadogRow = {
      ...cpu, resource: 'memory', currentRequest: '128Mi', currentRequestValue: 128 * 1024 ** 2,
      recommendedRequest: '64Mi', recommendedRequestValue: 64 * 1024 ** 2, hpaManaged: true,
      decision: { status: 'review', reasons: ['hpa_managed'] },
    }
    const rows = [cpu, memory]
    const cluster = { id: 'east', name: 'East' }
    const { kind, namespace, name, replicas, scaledToZero } = workload
    const prometheus = workloadResultRows({ kind, namespace, name, replicas, scaledToZero, rows }, cluster)
    const datadog = workloadResultRows({
      ...workload, rows,
    }, cluster)
    expect(prometheus[0].classification).toBe('review')
    expect(datadog[0].classification).toBe('review')
    expect(datadog[0].impact).toEqual(prometheus[0].impact)
  })

  it('preserves scanned workload identity, source, time and fleet impact for Prometheus', () => {
    const { kind, namespace, name, replicas, scaledToZero } = workload
    const rows = workloadResultRows(
      { kind, namespace, name, replicas, scaledToZero, rows: [cpu, { ...cpu, container: 'proxy' }] },
      { id: 'west', name: 'West', observedAt: '2026-09-01T12:34:00Z' },
      { cpuChange: -3.2, memoryChange: 0 },
    )
    expect(rows[0].id).toBe('west/Deployment/payments/checkout/app')
    expect(rows[0].source).toBe('prometheus')
    expect(rows[1].workloadImpact).toBeUndefined()
    const html = renderToStaticMarkup(
      <RightsizingResultsTable rows={rows} openRowId={rows[0].id} onOpenRowChange={() => {}}
        renderWorkloadLink={() => null} />,
    )
    expect(html).toContain('Prometheus')
    expect(html).toContain('2026-09-01T12:34:00Z')
    expect(html).toContain('Whole workload · −3.2 CPU requested')
    expect(html).not.toContain('Datadog')
  })

  it('keeps cluster identity in the list, row selection and workload link', () => {
    const a = workloadResultRows(workload, { id: 'a', name: 'East (a)' })
    const b = workloadResultRows(workload, { id: 'b', name: 'East (b)' })
    expect(a[0].id).not.toBe(b[0].id)
    const html = renderToStaticMarkup(
      <RightsizingResultsTable
        rows={[...a, ...b]}
        openRowId={b[0].id}
        onOpenRowChange={() => {}}
        renderWorkloadLink={(row) => <a href={`/c/${row.clusterId}/workload`}>Open workload</a>}
      />,
    )
    expect(html).toContain('East (a) · ')
    expect(html).toContain('East (b) · ')
    expect(html).toContain('href="/c/b/workload"')
    expect(html).not.toContain('href="/c/a/workload"')
  })

  it('shows withheld resources as review-only and counts only admitted container requests', () => {
    const memory: DatadogRow = {
      ...cpu,
      resource: 'memory',
      fit: 'under_requested',
      currentRequest: '64Mi',
      currentRequestValue: 64 * 1024 ** 2,
      recommendedRequest: '128Mi',
      recommendedRequestValue: 128 * 1024 ** 2,
    }
    const rows = workloadResultRows(
      {
        ...workload,
        rows: [
          { ...cpu, decision: { status: 'review', reasons: ['guaranteed_qos'] } }, memory,
          { ...cpu, container: 'proxy', decision: { status: 'review', reasons: ['qos_unavailable'] } },
        ],
      },
      { id: 'one', name: 'East' },
      { cpuChange: 0, memoryChange: 128 * 1024 ** 2 },
    )
    expect(rows.map((row) => [row.container, row.classification])).toEqual([
      ['app', 'increase'],
      ['proxy', 'review'],
    ])
    expect(rows[0].impact).toEqual({ cpuChange: 0, memoryChange: 128 * 1024 ** 2, replicas: 2 })
    expect(rows[1].impact.cpuChange).toBe(0)
    const html = renderToStaticMarkup(
      <RightsizingResultsTable
        rows={rows}
        openRowId={rows[0].id}
        onOpenRowChange={() => {}}
        renderWorkloadLink={() => null}
      />,
    )
    expect(html).toContain('Review before changing')
    expect(html).toContain('CPU request needs review')
    expect(html).toContain('Guaranteed QoS')
    expect(html).toContain('1512 of 2016 expected 5-minute samples')
    expect(html).toContain('64Mi → 128Mi')
    expect(html).toContain('Whole workload · +128Mi memory requested')
    expect(html.match(/Whole workload/g)).toHaveLength(1)
    expect(html).toContain('500m → 200m')
    expect(html).not.toContain('Reduce request')
  })

  it('explains which limit blocks a request in the expanded operator row', () => {
    const row: DatadogRow = {
      ...cpu, fit: 'under_requested', limitConflict: true,
      recommendedRequest: undefined, recommendedRequestValue: undefined,
      recommendationReason: 'requests_exceed_pod_limit',
      decision: { status: 'review', reasons: ['limit_conflict', 'guaranteed_qos'] },
    }
    const rows = workloadResultRows({ ...workload, rows: [row] }, { id: 'east', name: 'East' })
    const html = renderToStaticMarkup(
      <RightsizingResultsTable rows={rows} openRowId={rows[0].id}
        onOpenRowChange={() => {}} renderWorkloadLink={() => null} />,
    )
    expect(html).toContain('pod-level limit (spec.resources)')
    expect(html).toContain('Guaranteed QoS')
    expect(html).not.toContain('The suggestion conflicts with the resource limit.')
  })

  it('keeps the vendor read error and retry instructions scoped to the host', () => {
    const measurement: DatadogRow = {
      ...cpu, fit: 'insufficient_history', coverage: 0, sampleCount: 0,
      recommendedRequest: undefined, recommendedRequestValue: undefined, decision: { status: 'none' },
    }
    function expandedRow(row: DatadogRow, source?: 'datadog') {
      const { kind, namespace, name, replicas, scaledToZero } = workload
      const rows = source ? workloadResultRows({ ...workload, rows: [row] }, { id: 'east', name: 'East' })
        : workloadResultRows({ kind, namespace, name, replicas, scaledToZero, rows: [row] })
      return renderToStaticMarkup(<RightsizingResultsTable rows={rows} openRowId={rows[0].id}
        onOpenRowChange={() => {}} renderWorkloadLink={() => null} />)
    }
    const failed = { ...measurement, queryError: 'usage query failed' }
    const cloud = expandedRow(failed, 'datadog')
    expect(cloud).toContain('Open the workload to retry')
    expect(cloud).not.toContain('top bar')
    const local = expandedRow(failed)
    expect(local).toContain('select fewer namespaces in the top bar')
    expect(local).not.toContain('0.0 days')
    expect(local).not.toContain('Wait for more runtime history')
    const short = expandedRow(measurement)
    expect(short).toContain('0.0 days')
    expect(short).toContain('Wait for more runtime history')
  })

  it('shows the workload measurement gap once even when containers have rows', () => {
    const missing: DatadogRow = {
      ...cpu, decision: { status: 'none' },
      fit: 'insufficient_history',
      sampleCount: 0,
      coverage: 0,
      recommendedRequest: undefined,
      recommendedRequestValue: undefined,
    }
    const rows = workloadResultRows(
      {
        ...workload,
        sampleAvailable: false,
        reason: 'No complete five-minute measurements for the current Pods and containers.',
        rows: [missing, { ...missing, container: 'proxy' }],
      },
      { id: 'one', name: 'East' },
    )
    const html = renderToStaticMarkup(
      <RightsizingResultsTable
        rows={rows}
        openRowId={null}
        onOpenRowChange={() => {}}
        renderWorkloadLink={() => null}
      />,
    )
    expect(html.match(/No complete five-minute measurements/g)).toHaveLength(1)
    expect(html).toContain('Insufficient usage evidence')
    expect(html).not.toContain('No suggested change')
  })

  it('keeps missing data separate from an evaluated request within the fit range', () => {
    const rows = workloadResultRows(
      {
        ...workload,
        rows: [
          {
            ...cpu, decision: { status: 'none' },
            recommendedRequest: undefined,
            recommendedRequestValue: undefined,
            fit: 'balanced',
            recommendationReason: 'request_within_fit_range',
          },
          {
            ...cpu, decision: { status: 'none' },
            container: 'worker',
            fit: 'insufficient_history',
            recommendedRequest: undefined,
            recommendedRequestValue: undefined,
          },
        ],
      },
      { id: 'one', name: 'East' },
    )
    expect(rows.map((row) => row.classification)).toEqual(['in_range', 'need_data'])
  })
})

describe('shared policy decisions', () => {
  it.each(vectors)('$name has the same impact in both provider views', (v) => {
    const status = v.wantStatus as NonNullable<RightsizingRow['decision']>['status']
    const row: DatadogRow = {
      ...cpu, fit: v.samples === 4 ? 'insufficient_history' : v.demand > 0.1 ? 'under_requested' : 'oversized',
      currentRequest: '100m', currentRequestValue: 0.1,
      recommendedRequest: v.wantTarget, recommendedRequestValue: v.wantTarget ? parseInt(v.wantTarget) / 1000 : undefined,
      decision: { status, reasons: v.reason ? [v.reason] : undefined },
    }
    for (const source of ['radar', 'datadog'] as const) {
      const data = { ...workload, scaledToZero: !!v.idle, replicas: v.idle ? 0 : 2, source, rows: [row] }
      const [result] = workloadResultRows(data)
      expect(result.impact.cpuChange).toBeCloseTo(v.wantChange)
      const expectedClass = status === 'review' ? 'review' : status === 'none' ? 'need_data' : 'increase'
      expect(result.classification).toBe(expectedClass)
      for (const compact of [true, false]) {
        const markup = renderToStaticMarkup(<RightsizingEvidence data={data} compact={compact} />)
        if (status === 'review' && v.wantTarget) expect(markup.toLowerCase()).toContain('review')
        if (v.wantTarget) expect(markup).toContain(v.wantTarget)
      }
    }
  })

  it('distinguishes older producers, missing decisions and contradictory actionability', () => {
    const current = { ...cpu, decision: { status: 'actionable' as const } }
    expect(rightsizingPolicyCoverage([])).toBe('empty')
    const older = { ...cpu, decision: undefined }
    expect(rightsizingPolicyCoverage([older])).toBe('legacy')
    expect(rightsizingPolicyCoverage([older], 'datadog')).toBe('incomplete')
    expect(rightsizingPolicyCoverage([current])).toBe('current')
    expect(rightsizingPolicyCoverage([current, older])).toBe('incomplete')
    expect(rightsizingPolicyCoverage([{ ...current, recommendedRequestValue: undefined }])).toBe('incomplete')
  })
})
