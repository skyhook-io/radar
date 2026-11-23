import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it } from 'vitest'
import type { DatadogSizingWorkload, RightsizingRow } from '../../types/rightsizing'
import { DatadogRightsizingEvidence } from './DatadogRightsizingEvidence'
import { datadogRowReview, isDatadogRequestActionable } from '../../utils/rightsizing-reasons'

const row: RightsizingRow = {
  container: 'api',
  resource: 'cpu',
  fit: 'oversized',
  confidence: 'medium',
  observed: { name: 'P95', formatted: '20m', value: 0.02 },
  peak: { name: 'P99', formatted: '80m', value: 0.08 },
  currentRequest: '1',
  recommendedRequest: '500m',
  coverage: 0.5,
  sampleCount: 1008,
  expectedSamples: 2016,
  hpaManaged: false,
  hpaEvidenceAvailable: true,
  oomEvidenceAvailable: true,
  throttleAvailable: true,
}
const sizing: DatadogSizingWorkload = {
  kind: 'Deployment',
  namespace: 'payments',
  name: 'api',
  source: 'datadog',
  ownerCoverage: 'current_pods',
  scaledToZero: false,
  sampleAvailable: true,
  window: '7d',
  replicas: 2,
  actionableRequests: [],
  rows: [row],
  reviewReasons: { api: { cpu: ['qos_change'] } },
  managedBy: {
    tool: 'flux',
    kind: 'HelmRelease',
    namespace: 'apps',
    name: 'payments',
    signal: 'labels helm.toolkit.fluxcd.io/name and namespace',
  },
}

describe('workload evidence used by embedded and standalone Radar', () => {
  it('retains measurements, the declared manager and the server’s guarded recommendation', () => {
    const html = renderToStaticMarkup(<DatadogRightsizingEvidence data={sizing} />)
    for (const text of [
      '2 current replicas',
      '50% coverage',
      '1008 of 2016',
      'P99 peak',
      '80m',
      'Detected manager · flux (HelmRelease apps/payments) · labels helm.toolkit.fluxcd.io/name and namespace',
      'Candidate for manual review',
      'Guaranteed QoS',
    ]) {
      expect(html).toContain(text)
    }
    expect(isDatadogRequestActionable(sizing, row)).toBe(false)
    expect(datadogRowReview(sizing, row)).toContain('Guaranteed QoS')
  })
  it('labels an eligible request and keeps the guard decision attached to its row', () => {
    const data: DatadogSizingWorkload = {
      ...sizing,
      actionableRequests: [{ container: row.container, resource: row.resource }],
      reviewReasons: {},
    }
    const html = renderToStaticMarkup(<DatadogRightsizingEvidence data={data} />)
    expect(html).toContain('Current request')
    expect(html).toContain('Suggested request')
    expect(html).not.toContain('Candidate for manual review')
  })
})
