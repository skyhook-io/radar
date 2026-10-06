// @vitest-environment jsdom
import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it, vi } from 'vitest'

const state = vi.hoisted(() => ({ data: { pod: 'pg-1', state: 'denied', permission: { exec: 'denied', grant: { verb: 'create', resource: 'pods', subresource: 'exec', namespace: 'db' } }, instances: [] } as any, navigate: vi.fn() }))
vi.mock('./useCNPGNavigate', () => ({ useCNPGNavigate: () => state.navigate }))
vi.mock('react-router-dom', async (original) => ({ ...await original<typeof import('react-router-dom')>(), useLocation: () => ({ pathname: '/cnpg/clusters/db/pg', search: '?ctx=test&tab=performance&drawer=x', state: { returnLabel: 'Clusters' } }) }))
vi.mock('@tanstack/react-query', async (original) => ({ ...await original<typeof import('@tanstack/react-query')>(), useQueries: () => [{ data: { containers: [] } }] }))
vi.mock('../../api/client', () => ({ podMetricsQuery: () => ({}), usePodMetrics: () => ({ data: { containers: [{ name: 'postgres', usage: { cpu: '30000000n', memory: '59Mi' } }] } }) }))
vi.mock('../../api/cnpg-sessions', () => ({
  useCNPGSessions: () => ({
    data: state.data,
    isLoading: false,
    error: null,
    isRefetchError: false,
    dataUpdatedAt: Date.now(),
  }),
}))

const { CNPGBlockingSessions } = await import('./CNPGBlockingSessions')

describe('CNPGBlockingSessions with exec denied', () => {
  it('explains only the exec grant for blocking detail', () => {
    const html = renderToStaticMarkup(<CNPGBlockingSessions namespace="db" cluster="pg" />)
    expect(html).toContain('Who blocks whom is read inside PostgreSQL')
    expect(html).toContain('create pods/exec')
    expect(html).not.toContain('aggregate')
    expect(html).not.toContain('get pods/proxy')
  })
})

it('keeps query-text prose out of unavailable states while the host owns the prerequisite', () => {
  expect(renderToStaticMarkup(<CNPGBlockingSessions namespace="db" cluster="pg" />)).not.toContain('Query text')
  state.data = { pod: '', state: 'unavailable', permission: { exec: 'unknown' }, instances: [] }
  const html = renderToStaticMarkup(<CNPGBlockingSessions namespace="db" cluster="pg" />)
  expect(html).toContain('Blocking detail not measured')
  expect(html).not.toContain('Available once the primary is running')
  expect(html).not.toContain('See Overview')
  expect(html).not.toContain('could not be read')
  expect(html).not.toContain('Query text')
})
it('keeps CPU and memory measurements and limits on separate rows with unbreakable units', () => {
  state.data = { pod: 'pg-1', state: 'ok', permission: { exec: 'allowed' }, sessions: [], instances: [{ pod: 'pg-1', role: 'primary', memoryLimit: '384Mi' }] }
  const html = renderToStaticMarkup(<CNPGBlockingSessions namespace="db" cluster="pg" />)
  expect(html).toMatch(/CPU <span class="whitespace-nowrap">0.03 cores<\/span>/)
  expect(html).toMatch(/Memory <span class="whitespace-nowrap">59 MiB<\/span>/)
  expect(html).toContain('limit <span class="whitespace-nowrap">384 MiB</span>')
  expect(html).not.toContain('Query text')
  expect(html.indexOf('No session is waiting')).toBeLessThan(html.indexOf('Resource usage'))
})
