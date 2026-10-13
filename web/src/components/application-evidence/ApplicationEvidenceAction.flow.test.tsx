// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { guardRadarFeature, radarFeatureSupport } from '../../api/radarFeatures'
import { ApiError } from '../../api/client'
import { ApplicationEvidenceAction } from './ApplicationEvidenceAction'

const state = vi.hoisted(() => ({ fetch: vi.fn(), supported: true, ApiError: class extends Error { constructor(message: string, readonly status: number) { super(message) } } }))
vi.mock('../../api/client', () => ({
  ApiError: state.ApiError,
  fetchJSON: (...args: unknown[]) => state.fetch(...args),
  useClusterInfo: () => ({ data: { context: 'test-cluster' } }),
  useCapabilities: () => ({ data: { deployment: { mode: 'local' }, features: state.supported ? { applicationEvidence: true } : {} } }),
  useRadarFeature: () => {
    const support = radarFeatureSupport('applicationEvidence', { features: state.supported ? { applicationEvidence: true } : {} }, 'v1.16.0')
    return { support, gatedKey: [], guard: <T,>(request: () => Promise<T>) => guardRadarFeature('applicationEvidence', support, {}, request) }
  },
}))
const candidates = { enabled: true, context: 'test-cluster', subjectUID: 'subject-uid', candidates: [{ application: 'vault', target: { namespace: 'demo', pod: 'vault-0', uid: 'pod-uid', container: 'vault' }, expectedEvidence: ['sealed'], coverage: 'One selected Pod.' }] }
let root: ReturnType<typeof createRoot>
let host: HTMLDivElement
let client: QueryClient
beforeEach(() => {
  state.fetch.mockReset(); state.supported = true
  ;(globalThis as Record<string, unknown>).IS_REACT_ACT_ENVIRONMENT = true
  window.matchMedia = vi.fn().mockReturnValue({ matches: false, addEventListener: vi.fn(), removeEventListener: vi.fn() })
  host = document.createElement('div'); document.body.append(host); root = createRoot(host)
  client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
})
afterEach(() => { act(() => root.unmount()); client.clear(); host.remove() })
it('does not request endpoints from a Radar without the collection flag', async () => {
  state.supported = false
  await act(async () => root.render(<QueryClientProvider client={client}><ApplicationEvidenceAction kind="Deployment" group="apps" namespace="demo" name="vault" uid="subject-uid" /></QueryClientProvider>))
  expect(state.fetch).not.toHaveBeenCalled()
  expect(host.textContent).toBe('')
})
it('collects only on explicit click and discards a cancelled response on reopen', async () => {
  let finish: (value: unknown) => void = () => {}
  const delayed = new Promise(resolve => { finish = resolve })
  state.fetch.mockImplementation((path: string) => path.startsWith('/application-evidence/candidates') ? Promise.resolve(candidates) : delayed)
  await act(async () => root.render(<QueryClientProvider client={client}><ApplicationEvidenceAction kind="Deployment" group="apps" namespace="demo" name="vault" uid="subject-uid" /></QueryClientProvider>))
  await vi.waitFor(() => expect(host.textContent).toContain('Collect Vault evidence'))
  act(() => host.querySelector('button')!.click())
  expect(state.fetch).toHaveBeenCalledTimes(1)
  await act(async () => [...document.querySelectorAll('button')].find(item => item.textContent === 'Collect evidence')!.click())
  const [path, request] = state.fetch.mock.calls[1]
  expect(path).toBe('/application-evidence/collect')
  expect(JSON.parse(request.body)).toEqual({ application: 'vault', namespace: 'demo', pod: 'vault-0', uid: 'pod-uid', context: 'test-cluster', confirmNetworkAccess: true })
  act(() => [...document.querySelectorAll('button')].find(item => item.textContent === 'Cancel')!.click())
  expect(request.signal.aborted).toBe(true)
  act(() => host.querySelector('button')!.click())
  await act(async () => finish({ adapter: 'vault', target: candidates.candidates[0].target, observedAt: '2026-10-13T00:00:00Z', source: 'selected_pod_endpoint', outcome: 'observed', facts: { vault: { initialized: true, sealed: false, standby: false } }, limitations: [] }))
  expect(document.body.textContent).not.toContain('Endpoint observations')
  expect(state.fetch).toHaveBeenCalledTimes(2)
})

it('recovers from one transient cache failure without collecting an endpoint', async () => {
  state.fetch.mockRejectedValueOnce(new ApiError('not connected', 503)).mockResolvedValue(candidates)
  await act(async () => root.render(<QueryClientProvider client={client}><ApplicationEvidenceAction kind="Deployment" group="apps" namespace="demo" name="vault" uid="subject-uid" /></QueryClientProvider>))
  await vi.waitFor(() => expect(host.textContent).toContain('Collect Vault evidence'), { timeout: 2500 })
  expect(state.fetch).toHaveBeenCalledTimes(2)
  expect(state.fetch.mock.calls.every(([path]) => path.startsWith('/application-evidence/candidates'))).toBe(true)
})
