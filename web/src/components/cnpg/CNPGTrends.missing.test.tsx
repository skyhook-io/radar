import { renderToStaticMarkup } from 'react-dom/server'
import { MemoryRouter } from 'react-router-dom'
import { describe, expect, it, vi } from 'vitest'

const lag = (series: string[], state = 'ok') => ({
  id: 'replicationLag',
  title: 'Replay lag per standby',
  unit: 'seconds',
  source: 'cnpg_pg_replication_lag',
  seriesBy: 'pod',
  state,
  reason: state === 'empty' ? 'no instance was a standby in this range' : undefined,
  series: series.map((pod) => ({
    labels: { pod },
    dataPoints: [
      { timestamp: 60, value: 0 },
      { timestamp: 120, value: 0 },
    ],
  })),
  steps: 2,
  covered: 2,
})

let chart = lag(['pg-2'])
vi.mock('../../api/cnpg-history', async (orig) => ({
  ...(await orig<typeof import('../../api/cnpg-history')>()),
  useCNPGClusterHistory: () => ({
    data: {
      source: 'prometheus',
      state: 'ok',
      range: '1h',
      start: '1970-01-01T00:01:00Z',
      end: '1970-01-01T00:02:00Z',
      stepSeconds: 60,
      sampledAt: '',
      cluster: { namespace: 'db', name: 'pg', uid: 'u' },
      charts: [chart],
    },
    isLoading: false,
    error: null,
  }),
}))

const { CNPGTrends } = await import('./CNPGTrends')

const render = (standbys?: string[]) =>
  renderToStaticMarkup(
    <MemoryRouter>
      <CNPGTrends namespace="db" name="pg" samples={[]} standbys={standbys} instancePods={['pg-1', 'pg-2', 'pg-3']} />
    </MemoryRouter>,
  )

describe('replay-lag history with a standby that does not report', () => {
  it('names the standby instead of drawing only the calm one', () => {
    chart = lag(['pg-2'])
    expect(render(['pg-1', 'pg-2'])).toContain('No line for pg-1: Prometheus has no samples from it in this range.')
  })
  it('says nothing when every standby has a line, or when the standbys are unknown', () => {
    chart = lag(['pg-2'])
    expect(render(['pg-2'])).not.toContain('No line for')
    expect(render()).not.toContain('No line for')
  })
  it('replaces "no instance was a standby" when standbys exist but none reported', () => {
    chart = lag([], 'empty')
    const html = render(['pg-1', 'pg-2'])
    expect(html).toContain('No line for pg-1, pg-2: Prometheus has no samples from them in this range.')
    expect(html).not.toContain('no instance was a standby')
  })
})
