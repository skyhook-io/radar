import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it } from 'vitest'
import type { PrometheusRightsizing } from '../../api/client'
import { RightsizingBody } from './RightsizingStrip'

const withheld: PrometheusRightsizing = {
  kind: 'Deployment',
  namespace: 'payments',
  name: 'api',
  window: '7d',
  source: 'radar',
  ownerCoverage: 'ksm_history',
  replicas: 2,
  scaledToZero: false,
  sampleAvailable: false,
  rows: [],
  reason: 'Radar withheld recommendations.',
  clusterScope: { state: 'several_clusters', note: 'This metrics store holds several clusters.' },
}

function render(data: PrometheusRightsizing, compact = true) {
  return renderToStaticMarkup(
    <RightsizingBody state={{ statusLoading: false, connected: true, isLoading: false, error: null, data }} compact={compact} />,
  )
}

describe('RightsizingBody cluster scope', () => {
  it('shows the cluster scope note instead of recommendations when withheld', () => {
    for (const compact of [true, false]) {
      const html = render(withheld, compact)
      expect(html).toContain('This metrics store holds several clusters.')
      expect(html).toContain('Recommendations withheld')
      expect(html).not.toContain('Radar withheld recommendations.')
    }
  })

  it('keeps the plain reason when the scope is not withheld or absent', () => {
    for (const clusterScope of [undefined, { state: 'unlabeled' as const, note: 'unverified' }]) {
      const html = render({ ...withheld, reason: 'No workload usage samples are available in the last 7d.', clusterScope })
      expect(html).toContain('No workload usage samples')
      expect(html).not.toContain('role="note"')
    }
  })
})
