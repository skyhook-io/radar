import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it } from 'vitest'
import type { RightsizingRow, RightsizingScanResponse } from '../../api/client'
import { FitWhy, ScanNotices } from './RightsizingScanView'

const scan: RightsizingScanResponse = {
  state: 'complete',
  scannedAt: '2026-09-19T06:52:54Z',
  window: '7d',
  source: 'radar',
  coverage: {
    workloadsDiscovered: 120,
    workloadsEvaluated: 120,
    workloadsWithData: 47,
    batches: 3,
    completedBatches: 3,
  },
  workloads: [],
}

function render(result: RightsizingScanResponse) {
  return renderToStaticMarkup(<ScanNotices result={result} />)
}

describe('rightsizing scan notices', () => {
  it('keeps timeout coverage visible with query failures behind one collapsed disclosure', () => {
    const html = render({
      ...scan,
      state: 'partial',
      coverage: {
        ...scan.coverage,
        workloadsEvaluated: 100,
        completedBatches: 0,
        daemonSetsWithoutNodes: 27,
      },
      warnings: ['cpu_query_failed', 'memory_query_failed', 'scan_deadline_exceeded'].map(
        (code) => ({ code, message: 'backend-specific details' }),
      ),
    })
    expect(html.match(/<section/g)).toHaveLength(1)
    expect(html.match(/<button/g)).toHaveLength(1)
    expect(html).toContain('Partial results')
    expect(html).not.toContain('workloads evaluated')
    expect(html).toContain('select namespaces in the top bar')
    expect(html).toContain('Available recommendations are shown')
    expect(html).toContain('aria-expanded="false"')
    expect(html).toContain('inert=""')
    expect(html).toContain('Some CPU data could not be queried')
    expect(html).toContain('Some memory data could not be queried')
    expect(html).toContain('27 DaemonSets')
    expect(html).not.toContain('completed batches')
    expect(html).not.toContain('backend-specific details')
  })

  it('does not label a complete scan with excluded DaemonSets as a query failure', () => {
    const html = render({ ...scan, coverage: { ...scan.coverage, daemonSetsWithoutNodes: 1 } })
    expect(html).toContain('Scan notes')
    expect(html).toContain('1 DaemonSet runs on no node right now and is not listed')
    expect(html).not.toContain('<button')
    expect(html).not.toContain('Partial results')
    expect(html).not.toContain('time limit')
  })

  it('preserves access and cache limitations without claiming a timeout', () => {
    const html = render({
      ...scan,
      state: 'partial',
      coverage: {
        ...scan.coverage,
        restrictedKinds: ['Deployment'],
        partiallyCachedKinds: ['StatefulSet'],
        unavailableKinds: ['DaemonSet'],
      },
    })
    expect(html).toContain('Kubernetes access')
    expect(html).toContain('available ownership data')
    expect(html).toContain('caching only some namespaces')
    expect(html).not.toContain('time limit')
  })

  it('does not infer a timeout or short history from a query failure', () => {
    const html = render({
      ...scan,
      state: 'partial',
      warnings: [{ code: 'cpu_query_failed', message: 'unknown error' }],
    })
    expect(html).toContain('Some CPU data could not be queried')
    expect(html).not.toContain('time limit')
    expect(html).not.toContain('not enough recent history')
  })

  it('shows no notice for a clean scan and no empty Details button for deadline-only partial results', () => {
    expect(render(scan)).toBe('')
    const html = render({
      ...scan,
      state: 'partial',
      warnings: [{ code: 'scan_deadline_exceeded', message: 'deadline' }],
    })
    expect(html).toContain('time limit')
    expect(html).not.toContain('<button')
  })
})

describe('rightsizing evidence explanation', () => {
  const row: RightsizingRow = {
    container: 'app',
    resource: 'cpu',
    fit: 'insufficient_history',
    confidence: 'low',
    coverage: 0,
    sampleCount: 0,
    expectedSamples: 2017,
    hpaManaged: false,
    hpaEvidenceAvailable: true,
    oomEvidenceAvailable: false,
  }

  it('distinguishes failed queries from successfully measured short history', () => {
    const failed = renderToStaticMarkup(
      <FitWhy label="CPU" row={{ ...row, queryError: 'usage query failed' }} />,
    )
    expect(failed).toContain('Metrics could not be queried')
    expect(failed).not.toContain('0.0 days')
    expect(failed).not.toContain('Wait for more runtime history')
    const short = renderToStaticMarkup(<FitWhy label="CPU" row={row} />)
    expect(short).toContain('0.0 days')
    expect(short).toContain('Wait for more runtime history')
  })
})
