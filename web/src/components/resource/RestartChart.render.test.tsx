import { describe, expect, it, vi } from 'vitest'
import { renderToString } from 'react-dom/server'
import { PARTIAL_DATA_NOTE } from '@skyhook-io/k8s-ui/components/charts'

let metricsResult: { data?: unknown; isLoading?: boolean; error?: unknown } = {}

vi.mock('../../api/client', async (importActual) => ({
  ...(await importActual<typeof import('../../api/client')>()),
  usePrometheusResourceMetrics: () => metricsResult,
  usePrometheusStatus: () => ({ data: { connected: true } }),
}))

const { RestartEventLane } = await import('./RestartChart')

const empty = { resultType: 'matrix', series: [] }

/**
 * The lane is also shown on its own (the workload view), so a withheld answer
 * must say why rather than vanish like a pod that never restarted.
 */
describe('RestartEventLane — cluster scope', () => {
  it('says why the restart history is withheld instead of hiding', () => {
    metricsResult = { data: { result: empty, clusterScope: { state: 'several_clusters', note: 'This store holds several clusters.' } } }
    const html = renderToString(<RestartEventLane kind="Deployment" namespace="shop" name="api" />)
    expect(html).toContain('data-cluster-scope="several_clusters"')
    expect(html).toContain('Restart history withheld for this cluster')
    expect(html).toContain('This store holds several clusters.')
  })

  it('stays hidden when a scoped answer has no restarts', () => {
    metricsResult = { data: { result: empty, clusterScope: { state: 'scoped_verified', note: 'Matched.' } } }
    expect(renderToString(<RestartEventLane kind="Deployment" namespace="shop" name="api" />)).toBe('')
  })

  it('stays hidden when an older Radar sends no clusterScope', () => {
    metricsResult = { data: { result: empty } }
    expect(renderToString(<RestartEventLane kind="Deployment" namespace="shop" name="api" />)).toBe('')
  })
})

const restartSeries = (values: number[]) => ({
  resultType: 'matrix',
  series: [{ labels: { pod: 'api-1' }, dataPoints: values.map((value, i) => ({ timestamp: Date.now() / 1000 - 600 + i * 60, value })) }],
})

/**
 * A partial answer may be missing restarts: an empty one must not read as a
 * pod that never restarted, and a count from one is only a lower bound.
 */
describe('RestartEventLane — partial answers', () => {
  it('notes an empty partial answer instead of hiding', () => {
    metricsResult = { data: { result: empty, partial: true } }
    const html = renderToString(<RestartEventLane kind="Deployment" namespace="shop" name="api" />)
    expect(html).toContain('No restarts seen in a partial answer')
    expect(html).toContain(PARTIAL_DATA_NOTE)
  })

  it('qualifies the total of a partial answer as a lower bound', () => {
    metricsResult = { data: { result: restartSeries([0, 1, 1, 3]), partial: true } }
    const html = renderToString(<RestartEventLane kind="Deployment" namespace="shop" name="api" />)
    expect(html).toContain('at least <!-- -->3<!-- --> total')
    expect(html).toContain(PARTIAL_DATA_NOTE)
  })

  it('shows an exact total for a complete answer', () => {
    metricsResult = { data: { result: restartSeries([0, 1, 1, 3]) } }
    const html = renderToString(<RestartEventLane kind="Deployment" namespace="shop" name="api" />)
    expect(html).toContain('3<!-- --> total')
    expect(html).not.toContain('at least')
    expect(html).not.toContain(PARTIAL_DATA_NOTE)
  })
})
