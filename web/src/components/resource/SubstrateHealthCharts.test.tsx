import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it, vi } from 'vitest'
import type { SubstrateHealth } from '../../api/substrateHealth'

let health: { data?: SubstrateHealth; error?: unknown; upgrade?: unknown; blockedReason?: string; support?: string } = {}
let promStatus: { connected: boolean; discovering?: boolean; error?: string } = { connected: true }

vi.mock('../../api/client', async (orig) => ({
  ...(await orig<typeof import('../../api/client')>()),
  useAutoPromConnect: () => {},
  usePrometheusStatus: () => ({ data: promStatus }),
  usePrometheusConnect: () => ({ mutate: () => {}, isPending: false }),
}))
vi.mock('../../api/substrateHealth', async (orig) => ({
  ...(await orig<typeof import('../../api/substrateHealth')>()),
  useSubstrateHealth: () => health,
}))

const { SubstrateHealthCharts } = await import('./SubstrateHealthCharts')
const { ApiError } = await import('../../api/client')

const pool = { kind: 'workerpool' as const, namespace: 'agents', name: 'pool' }
const node = { kind: 'node' as const, name: 'worker-1' }
const base = { range: '1h', start: 1000, end: 4600, stepSeconds: 60, charts: [] }
const render = (target: typeof pool | typeof node) => renderToStaticMarkup(<SubstrateHealthCharts target={target} />)

function chart(overrides: Partial<SubstrateHealth['charts'][number]>): SubstrateHealth['charts'][number] {
  return {
    id: 'workers', title: 'Workers by state', unit: 'workers', population: 'Every worker Pod.', source: 'ate_workerpool_workers',
    state: 'ok', isolation: { mode: 'unverified', note: 'Radar couldn\'t confirm these series belong to this exact cluster' },
    series: [
      { labels: { series: 'idle (gvisor)' }, dataPoints: [{ timestamp: 4600, value: 1.5 }] },
      { labels: { series: 'partial (gvisor)' }, dataPoints: [{ timestamp: 4600, value: 1.5 }] },
    ],
    ...overrides,
  }
}

describe('SubstrateHealthCharts', () => {
  it('offers to connect Prometheus only where it is the whole tab', () => {
    promStatus = { connected: false }
    health = {}
    expect(render(pool)).toBe('')
    expect(renderToStaticMarkup(<SubstrateHealthCharts target={pool} standalone />)).toContain('Discover Prometheus')
    promStatus = { connected: true }
  })

  it('says why there are no charts when Radar support could not be checked', () => {
    health = { blockedReason: "Couldn't check this cluster's Radar support. Refresh to try again." }
    expect(render(pool)).toContain('Refresh to try again')
    expect(render(node)).toBe('')
  })

  it('shows a metrics error even when a capabilities refresh failed after support was known', () => {
    health = { support: 'supported', blockedReason: "Couldn't check this cluster's Radar support. Refresh to try again.", error: new Error('Prometheus timed out') }
    expect(render(pool)).toContain('Prometheus timed out')
    expect(render(pool)).not.toContain('Refresh to try again')
  })

  it('says what it looked for when a pool has no Substrate series', () => {
    health = { data: { ...base, status: 'notFound' } }
    expect(render(pool)).toContain('Prometheus has no Agent Substrate metrics for this pool')
  })

  it('says a pool reports no worker Pods, without the metric-naming hint', () => {
    health = { data: { ...base, status: 'notFound' } }
    const none = renderToStaticMarkup(<SubstrateHealthCharts target={pool} workerPods={0} />)
    expect(none).toContain('The controller reports 0 worker Pods for this pool')
    expect(none).not.toContain('underscore names')
    expect(renderToStaticMarkup(<SubstrateHealthCharts target={pool} workerPods={2} />)).toContain('underscore names')
  })

  it('stays out of the way on a node that runs no Substrate', () => {
    health = { data: { ...base, status: 'notFound' } }
    expect(render(node)).toBe('')
    health = { data: { ...base, status: 'notOnNode' } }
    expect(render(node)).toBe('')
  })

  it('explains a node whose series cannot be attributed', () => {
    health = { data: { ...base, status: 'notAttributable', reason: 'atelet\'s series carry no node label' } }
    expect(render(node)).toContain('carry no node label')
  })

  it('shows a denial instead of an empty chart', () => {
    health = { error: new ApiError('forbidden', 403) }
    expect(render(pool)).toContain('You don&#x27;t have access to these metrics')
  })

  it('keeps unrounded values readable and explains charts it cannot draw', () => {
    health = { data: { ...base, status: 'available', charts: [
      chart({}),
      chart({ id: 'resumeTime', title: 'Resume time', state: 'duplicateSources', reason: 'reported through more than one scrape job (a, b)', series: [] }),
    ] } }
    const html = render(pool)
    // Each line's latest value sits in its legend entry, unrounded.
    expect(html).toMatch(/idle \(gvisor\)<\/span><span class="tabular-nums[^"]*">1\.5</)
    expect(html).toMatch(/partial \(gvisor\)<\/span><span class="tabular-nums[^"]*">1\.5</)
    expect(html).toContain('Not shown: reported through more than one scrape job (a, b).')
    expect(html).toContain('Aggregates: Substrate keeps actor names out')
    expect(html).toContain('confirm these series belong to this exact cluster')
  })

  it('collapses among drawer sections, but is a plain heading on a Metrics tab', () => {
    health = { data: { ...base, status: 'available', charts: [chart({})] } }
    expect(render(pool)).toContain('aria-expanded')
    const tab = renderToStaticMarkup(<SubstrateHealthCharts target={pool} collapsible={false} />)
    expect(tab).not.toContain('aria-expanded')
    expect(tab).toContain('Agent Substrate · last 1h')
  })

  it('states a reason shared by several charts once', () => {
    const refused = { state: 'duplicateSources' as const, reason: 'reported through more than one scrape job (a, b)', series: [] }
    health = { data: { ...base, status: 'available', charts: [
      chart({ id: 'resumeTime', title: 'Resume time', ...refused }),
      chart({ id: 'crashes', title: 'Actor crashes', ...refused }),
    ] } }
    const html = render(pool)
    expect(html).toContain('Resume time · Actor crashes')
    expect(html.split('more than one scrape job').length - 1).toBe(1)
  })
  it('does not call an hour-old sample "now"', () => {
    health = { data: { ...base, status: 'available', charts: [chart({ series: [
      { labels: { series: 'idle (gvisor)' }, dataPoints: [{ timestamp: 1000, value: 2 }, { timestamp: 4600, value: null }] },
      { labels: { series: 'partial (gvisor)' }, dataPoints: [{ timestamp: 1000, value: 1 }] },
    ] })] } }
    expect(render(pool)).not.toMatch(/<\/span><span class="tabular-nums/)
  })

  it('says when samples begin only when they begin well after the range', () => {
    const late = new Date(4000 * 1000).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })
    // Gaps and non-finite values before the first sample don't count; a zero does.
    health = { data: { ...base, status: 'available', charts: [chart({ series: [
      { labels: { series: 'idle (gvisor)' }, dataPoints: [{ timestamp: 1000, value: null }, { timestamp: 4000, value: 0 }] },
      { labels: { series: 'partial (gvisor)' }, dataPoints: [{ timestamp: 4300, value: 1 }] },
    ] })] } }
    expect(render(pool)).toContain(`No samples before ${late}.`)
    health = { data: { ...base, status: 'available', charts: [chart({ series: [
      { labels: { series: 'idle (gvisor)' }, dataPoints: [{ timestamp: 1060, value: 2 }, { timestamp: 4600, value: 2 }] },
    ] })] } }
    expect(render(pool)).not.toContain('No samples before')
  })
})
