import { describe, expect, it, vi } from 'vitest'
import { renderToString } from 'react-dom/server'

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
