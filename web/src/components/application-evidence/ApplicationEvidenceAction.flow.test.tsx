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

it('keeps an open observation and validated targets after a permission retry fails', async () => {
  state.fetch.mockResolvedValueOnce({ ...candidates, permissionCheckTimedOut: true })
  await act(async () => root.render(<QueryClientProvider client={client}><ApplicationEvidenceAction kind="Deployment" group="apps" namespace="demo" name="vault" uid="subject-uid" /></QueryClientProvider>))
  await vi.waitFor(() => expect(host.textContent).toContain('Collect Vault evidence'))
  act(() => [...host.querySelectorAll('button')].find(item => item.textContent === 'Collect Vault evidence')!.click())
  state.fetch.mockResolvedValueOnce({ adapter: 'vault', target: candidates.candidates[0].target, observedAt: '2026-10-13T00:00:00Z', source: 'selected_pod_endpoint', outcome: 'observed', facts: { vault: { initialized: false, sealed: true, standby: true } }, limitations: [] })
  await act(async () => [...document.querySelectorAll('button')].find(item => item.textContent === 'Collect evidence')!.click())
  expect(document.body.textContent).toContain('Endpoint observations')
  state.fetch.mockRejectedValue(new ApiError('permission check failed', 500))
  await act(async () => [...host.querySelectorAll('button')].find(item => item.textContent === 'Retry permission check')!.click())
  await vi.waitFor(() => expect(host.textContent).toContain('Could not refresh application evidence'))
  expect(document.body.textContent).toContain('Endpoint observations')
  expect(document.body.textContent).toContain('Copy observation')
  expect(host.textContent).toContain('Collect Vault evidence')
  state.fetch.mockResolvedValue(candidates)
  await act(async () => [...host.querySelectorAll('button')].find(item => item.textContent === 'Retry permission check')!.click())
  await vi.waitFor(() => expect(host.textContent).not.toContain('Could not refresh application evidence'))
  expect(document.body.textContent).toContain('Endpoint observations')
})

it.each(['target_changed', 'pod_not_running', 'container_not_running'])('preserves a %s refusal without offering its stale target after a failed refresh', async reason => {
  state.fetch.mockResolvedValueOnce(candidates)
  await act(async () => root.render(<QueryClientProvider client={client}><ApplicationEvidenceAction kind="Deployment" group="apps" namespace="demo" name="vault" uid="subject-uid" /></QueryClientProvider>))
  await vi.waitFor(() => expect(host.textContent).toContain('Collect Vault evidence'))
  act(() => host.querySelector('button')!.click())
  state.fetch.mockResolvedValueOnce({ adapter: 'vault', target: candidates.candidates[0].target, observedAt: '2026-10-13T00:00:00Z', source: 'selected_pod_endpoint', outcome: 'unavailable', reason, limitations: [] }).mockRejectedValue(new ApiError('refresh failed', 500))
  await act(async () => [...document.querySelectorAll('button')].find(item => item.textContent === 'Collect evidence')!.click())
  await vi.waitFor(() => expect(host.textContent).toContain('Could not refresh application evidence'))
  expect(document.body.textContent).toContain('Evidence unavailable')
  expect(document.body.textContent).toContain('Copy observation')
  expect(host.textContent).not.toContain('Collect Vault evidence')
  state.fetch.mockResolvedValue({ ...candidates, candidates: [{ ...candidates.candidates[0], target: { ...candidates.candidates[0].target, uid: 'replacement-uid' } }] })
  await act(async () => [...host.querySelectorAll('button')].find(item => item.textContent === 'Retry')!.click())
  await vi.waitFor(() => expect(host.textContent).toContain('Collect Vault evidence'))
  act(() => [...document.querySelectorAll('button')].find(item => item.textContent === 'Close')!.click())
  act(() => [...host.querySelectorAll('button')].find(item => item.textContent === 'Collect Vault evidence')!.click())
  state.fetch.mockResolvedValue({ adapter: 'vault', target: { ...candidates.candidates[0].target, uid: 'replacement-uid' }, observedAt: '2026-10-13T00:00:00Z', source: 'selected_pod_endpoint', outcome: 'observed', limitations: [] })
  await act(async () => [...document.querySelectorAll('button')].find(item => item.textContent === 'Collect evidence')!.click())
  const [, request] = state.fetch.mock.calls.at(-1)!
  expect(JSON.parse(request.body).uid).toBe('replacement-uid')
})

it('allows the same Pod UID after a container restart and a successful refresh', async () => {
  state.fetch.mockResolvedValueOnce(candidates)
  await act(async () => root.render(<QueryClientProvider client={client}><ApplicationEvidenceAction kind="Deployment" group="apps" namespace="demo" name="vault" uid="subject-uid" /></QueryClientProvider>))
  await vi.waitFor(() => expect(host.textContent).toContain('Collect Vault evidence'))
  act(() => host.querySelector('button')!.click())
  state.fetch.mockResolvedValueOnce({ adapter: 'vault', target: candidates.candidates[0].target, observedAt: '2026-10-13T00:00:00Z', source: 'selected_pod_endpoint', outcome: 'unavailable', reason: 'target_changed', limitations: [] }).mockResolvedValue(candidates)
  await act(async () => [...document.querySelectorAll('button')].find(item => item.textContent === 'Collect evidence')!.click())
  await vi.waitFor(() => expect(host.textContent).toContain('Collect Vault evidence'))
  expect(document.body.textContent).toContain('Evidence unavailable')
  act(() => [...document.querySelectorAll('button')].find(item => item.textContent === 'Close')!.click())
  act(() => host.querySelector('button')!.click())
  state.fetch.mockResolvedValue({ adapter: 'vault', target: candidates.candidates[0].target, observedAt: '2026-10-13T00:00:00Z', source: 'selected_pod_endpoint', outcome: 'observed', limitations: [] })
  await act(async () => [...document.querySelectorAll('button')].find(item => item.textContent === 'Collect evidence')!.click())
  expect(document.body.textContent).toContain('Endpoint observations')
  const [, request] = state.fetch.mock.calls.at(-1)!
  expect(JSON.parse(request.body).uid).toBe('pod-uid')
})
it('keeps an optional first-lookup failure quiet until the user has used the feature', async () => {
  state.fetch.mockRejectedValue(new ApiError('access denied', 403))
  await act(async () => root.render(<QueryClientProvider client={client}><ApplicationEvidenceAction kind="Deployment" group="apps" namespace="demo" name="vault" uid="subject-uid" /></QueryClientProvider>))
  await vi.waitFor(() => expect(state.fetch).toHaveBeenCalledTimes(1))
  expect(host.textContent).toBe('')
})

it('offers an explicit retry after permission evaluation fails without collecting', async () => {
  state.fetch.mockResolvedValueOnce({ ...candidates, candidates: [], permissionCheckFailed: true })
  await act(async () => root.render(<QueryClientProvider client={client}><ApplicationEvidenceAction kind="Deployment" group="apps" namespace="demo" name="vault" uid="subject-uid" /></QueryClientProvider>))
  await vi.waitFor(() => expect(host.textContent).toContain('Application evidence permissions could not be verified'))
  expect(host.textContent).not.toContain('Collect Vault evidence')
  state.fetch.mockResolvedValue(candidates)
  await act(async () => [...host.querySelectorAll('button')].find(item => item.textContent === 'Retry permission check')!.click())
  await vi.waitFor(() => expect(host.textContent).toContain('Collect Vault evidence'))
  expect(state.fetch.mock.calls.every(([path]) => path.startsWith('/application-evidence/candidates'))).toBe(true)
  expect(state.fetch.mock.calls.at(-1)![0]).toContain('retryPermissions=true')
})

it.each(['pod-uid', 'replacement-uid'])('allows deliberate reselection of %s inside the dialog while keeping the refusal', async nextUID => {
  state.fetch.mockResolvedValueOnce(candidates)
  await act(async () => root.render(<QueryClientProvider client={client}><ApplicationEvidenceAction kind="Deployment" group="apps" namespace="demo" name="vault" uid="subject-uid" /></QueryClientProvider>))
  await vi.waitFor(() => expect(host.textContent).toContain('Collect Vault evidence'))
  act(() => host.querySelector('button')!.click())
  const refreshed = { ...candidates, candidates: [{ ...candidates.candidates[0], target: { ...candidates.candidates[0].target, uid: nextUID } }] }
  state.fetch.mockResolvedValueOnce({ adapter: 'vault', target: candidates.candidates[0].target, observedAt: '2026-10-13T00:00:00Z', source: 'selected_pod_endpoint', outcome: 'unavailable', reason: 'target_changed', limitations: [] }).mockResolvedValueOnce(nextUID === 'replacement-uid' ? { ...candidates, candidates: [], permissionCheckFailed: true } : refreshed).mockResolvedValue(refreshed)
  await act(async () => [...document.querySelectorAll('button')].find(item => item.textContent === 'Collect evidence')!.click())
  if (nextUID === 'replacement-uid') {
    await vi.waitFor(() => expect(host.textContent).toContain('Application evidence permissions could not be verified'))
    await vi.waitFor(() => expect(document.body.textContent).toContain('A current Pod endpoint has not been verified'))
    await act(async () => [...document.querySelectorAll('button')].find(item => item.textContent === 'Refresh Pod choices')!.click())
    expect(state.fetch.mock.calls.at(-1)![0]).toContain('retryPermissions=true')
  }
  await vi.waitFor(() => expect(document.querySelector<HTMLButtonElement>('button[aria-label="Pod endpoint"]')?.disabled).toBe(false))
  expect(document.body.textContent).toContain('Evidence unavailable')
  expect(document.body.textContent).toContain('Copy observation')
  expect([...document.querySelectorAll<HTMLButtonElement>('button')].find(item => item.textContent === 'Collect again')!.disabled).toBe(true)
  act(() => document.querySelector<HTMLButtonElement>('button[aria-label="Pod endpoint"]')!.click())
  act(() => document.querySelector<HTMLButtonElement>('[role="option"]')!.click())
  expect(document.body.textContent).toContain('Evidence unavailable')
  expect(state.fetch.mock.calls.filter(([path]) => path === '/application-evidence/collect')).toHaveLength(1)
  state.fetch.mockResolvedValue({ adapter: 'vault', target: refreshed.candidates[0].target, observedAt: '2026-10-13T00:00:00Z', source: 'selected_pod_endpoint', outcome: 'observed', limitations: [] })
  await act(async () => [...document.querySelectorAll('button')].find(item => item.textContent === 'Collect again')!.click())
  expect(JSON.parse(state.fetch.mock.calls.at(-1)![1].body).uid).toBe(nextUID)
  expect(document.body.textContent).toContain('Endpoint observations')
})
