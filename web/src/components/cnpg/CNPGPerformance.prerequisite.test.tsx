// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { renderToStaticMarkup } from 'react-dom/server'
import { MemoryRouter } from 'react-router-dom'
import { expect, it, vi } from 'vitest'
import { CNPGPerformance } from './CNPGPerformance'
const state = vi.hoisted(() => ({ denied: false, execDenied: false, navigate: vi.fn() }))
vi.mock('./useCNPGNavigate', () => ({ useCNPGNavigate: () => state.navigate }))
vi.mock('../../api/cnpg', () => ({ useCNPGRuntime: () => ({ data: { permission: { proxy: state.denied ? 'denied' : 'allowed', grant: { verb: 'get', resource: 'pods', subresource: 'proxy', namespace: 'db' } }, instances: [] } }), useCNPGClusterCapabilities: () => ({}) }))
vi.mock('../../api/cnpg-sessions', () => ({ useCNPGSessions: () => ({ data: { pod: '', state: state.denied || state.execDenied ? 'denied' : 'unavailable', permission: { exec: state.denied || state.execDenied ? 'denied' : 'unknown', grant: { verb: 'create', resource: 'pods', subresource: 'exec', namespace: 'db' } }, instances: [] } }) }))
vi.mock('../../api/cnpg-history', () => ({ useCNPGClusterHistory: () => ({}) }))
vi.mock('./CNPGTrends', () => ({ CNPGTrends: () => null, useSampleBuffer: () => [] }))
const view = () => <MemoryRouter initialEntries={[{ pathname: '/cnpg/clusters/db/pg', search: '?ctx=test&tab=performance&drawer=x', state: { returnLabel: 'Clusters' } }]}><CNPGPerformance namespace="db" name="pg" /></MemoryRouter>
it('shares one primary prerequisite and preserves the Overview navigation context', () => {
  const html = renderToStaticMarkup(view())
  expect(html.match(/Available once the primary is running/g)).toHaveLength(1)
  expect(html.match(/See Overview/g)).toHaveLength(1)
  expect(html).toContain('Session aggregates not measured')
  expect(html).toContain('Blocking detail not measured')
  expect(html).not.toContain('Query text')
  Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })
  const host = document.createElement('div'); const root = createRoot(host)
  act(() => root.render(view()))
  act(() => [...host.querySelectorAll<HTMLButtonElement>('button')].find((b) => b.textContent?.includes('See Overview'))!.click())
  expect(state.navigate).toHaveBeenCalledWith('/cnpg/clusters/db/pg?ctx=test&tab=overview&drawer=x&problems=all', { replace: true, state: { returnLabel: 'Clusters' } })
  act(() => root.unmount())
})
it('explains proxy access once and blocking exec access once', () => {
  state.denied = true
  const html = renderToStaticMarkup(view())
  expect(html.match(/get pods\/proxy/g)).toHaveLength(1)
  expect(html.match(/create pods\/exec/g)).toHaveLength(1)
  expect(html).not.toContain('aggregate session counts are unavailable too')
  expect(html).not.toContain('Available once the primary is running')
  state.denied = false
})

it('keeps the startup prerequisite and only Blocking exec denial when proxy is allowed', () => {
  state.execDenied = true
  const html = renderToStaticMarkup(view())
  expect(html.match(/Available once the primary is running/g)).toHaveLength(1)
  expect(html.match(/create pods\/exec/g)).toHaveLength(1)
  expect(html).toContain('Session aggregates not measured')
  expect(html).not.toContain('Session detail needs exec')
  expect(html).not.toContain('get pods/proxy')
  state.execDenied = false
})
