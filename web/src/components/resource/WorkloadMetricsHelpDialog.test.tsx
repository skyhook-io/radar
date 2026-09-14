import { describe, expect, it } from 'vitest'
import { renderToStaticMarkup } from 'react-dom/server'
import { WorkloadMetricsHelpContent } from './WorkloadMetricsHelpDialog'
import type { WorkloadMetrics } from '../../api/workloadMetrics'

const data = (attribution: Record<string, string>): WorkloadMetrics => ({
  attribution, state: 'available', sources: [], history: {}, comparison: {}, panels: {},
  pods: 2, podsTotal: 2, start: 100, end: 160, stepSeconds: 60, rateWindowSeconds: 300,
})

describe('Metrics help', () => {
  it('groups only identical resource evidence, not HTTP observers or different methods', () => {
    const html = renderToStaticMarkup(<WorkloadMetricsHelpContent pending={false} data={data({
      cpu: 'UID match', memory: 'UID match', throttling: 'Label match', beyla: 'UID match', istio: 'No observations',
    })} />)
    expect(html).toContain('CPU / Memory')
    expect(html).not.toContain('CPU / Memory / Throttling')
    expect(html).toContain('Beyla · HTTP requests')
    expect(html).toContain('Istio · HTTP requests')
    expect(html.match(/UID match/g)).toHaveLength(2)
    expect(html).toContain('Label match')
    expect(html).toContain('No observations')
  })
  it('separates asserted scope and history from current-Pod matching', () => {
    const html = renderToStaticMarkup(<WorkloadMetricsHelpContent pending={false} data={data({ scope: 'Operator assertion', history: 'Retained ownership' })} />)
    expect(html).toContain('Operator-asserted scope')
    expect(html).toContain('Historical ownership')
    expect(html).not.toContain('<table')
    expect(html).not.toContain('No current-Pod matching evidence')
    expect(html).toContain('Each chart’s history label')
  })
  it('keeps operator overrides collapsed and explains their limits', () => {
    const html = renderToStaticMarkup(<WorkloadMetricsHelpContent pending={false} data={data({})} />)
    expect(html).toContain('Troubleshooting identity matching')
    expect(html).toContain('--prometheus-single-cluster')
    expect(html).toContain('they do not add missing metrics')
    expect(html).toContain('Leave both unset for automatic matching')
    expect(html).toContain('it does not scope rightsizing')
    expect(html).toContain('Standalone CLI:')
    expect(html).toContain('when starting Radar')
    expect(html).toContain('traffic.prometheusSingleCluster')
    expect(html).toContain('traffic.prometheusClusterLabels')
    expect(html).toContain('In-cluster OSS or Radar Cloud:')
    expect(html).toContain('not viewer preferences')
    expect(html).toContain('Desktop does not expose these overrides yet')
    expect(html).toContain('only alongside a verified scope override')
    expect(html).not.toMatch(/<details[^>]*\bopen[=> ]/)
  })
  it('distinguishes loading, failed refresh and absent evidence', () => {
    const pending = renderToStaticMarkup(<WorkloadMetricsHelpContent pending />)
    expect(pending).toContain('Checking the selected metrics')
    expect(pending).not.toContain('No current-Pod matching evidence')
    const failed = renderToStaticMarkup(<WorkloadMetricsHelpContent pending={false} error={new Error('Query failed')} data={data({ cpu: 'Previous evidence' })} />)
    expect(failed).toContain('Query failed')
    expect(failed).toContain('Evidence below is from the previous response')
    const empty = renderToStaticMarkup(<WorkloadMetricsHelpContent pending={false} />)
    expect(empty).toContain('No current-Pod matching evidence is available yet')
  })
  it('does not deny historical evidence when there are no current-Pod matches', () => {
    const html = renderToStaticMarkup(<WorkloadMetricsHelpContent pending={false} data={data({ history: 'Retained ownership is available' })} />)
    expect(html).toContain('No current-Pod matching evidence')
    expect(html).toContain('Retained ownership is available')
  })
  it.each(['detecting', 'error'] as const)('shows %s comparison independently of usable history', (state) => {
    const response = data({ history: 'Historical ownership is available' })
    response.reason = 'History follows retained workload ownership'
    response.comparison = { cpu: { state, reason: 'Current identity status', unit: 'cores', series: [] } }
    const html = renderToStaticMarkup(<WorkloadMetricsHelpContent pending={false} data={response} />)
    expect(html).toContain('Current identity status')
    expect(html).toContain('Historical ownership is available')
    expect(html).not.toContain(response.reason)
    expect(html).not.toContain('No current-Pod matching evidence')
  })
  it('describes a first-load failure without implying previous evidence', () => {
    const html = renderToStaticMarkup(<WorkloadMetricsHelpContent pending={false} error={new Error('Query failed')} />)
    expect(html).toContain('could not be loaded')
    expect(html).not.toContain('refreshed')
  })
})
