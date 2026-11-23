// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { DatadogRightsizingPanel } from './DatadogRightsizingPanel'
import { getApiBase } from '../../api/config'
import { radarControlBlockedReason } from '../../api/radarFeatures'

const state = vi.hoisted(() => ({ fetch: vi.fn(), support: 'supported' as 'supported' | 'unknown' | 'unsupported', capabilitiesFailed: false }))
vi.mock('../../api/client', () => ({
  fetchJSON: (...args: unknown[]) => state.fetch(...args),
  usePrometheusStatus: () => ({ data: { connected: false } }),
  useRadarFeature: () => ({
    support: state.support,
    blockedReason: radarControlBlockedReason('datadogRightsizing', state.support, state.capabilitiesFailed),
  }),
}))
const detail = {
  configured: true,
  rightsizing: {
    kind: 'Deployment',
    namespace: 'team',
    name: 'api',
    source: 'datadog',
    ownerCoverage: 'current_pods',
    replicas: 1,
    scaledToZero: false,
    rows: [],
    actionableRequests: [],
    sampleAvailable: false,
    reason: 'Insufficient history',
    window: '7d',
  },
}
let root: ReturnType<typeof createRoot>
let client: QueryClient
let host: HTMLDivElement

beforeEach(() => {
  state.support = 'supported'
  state.capabilitiesFailed = false
  state.fetch.mockReset()
  Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })
  client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  host = document.createElement('div')
  root = createRoot(host)
})
afterEach(async () => {
  await act(async () => root.unmount())
  client.clear()
})

async function render(name = 'api') {
  await act(async () =>
    root.render(
      <QueryClientProvider client={client}>
        <DatadogRightsizingPanel kind="Deployment" namespace="team" name={name} />
      </QueryClientProvider>,
    ),
  )
}
it('skips sizing reads for spokes without the feature', async () => {
  state.support = 'unsupported'
  await render()
  expect(host.textContent).toContain('Upgrade this cluster')
  expect(state.fetch).not.toHaveBeenCalled()
})

it.each([
  { failed: false, message: 'Checking this cluster' },
  { failed: true, message: 'Refresh to try again' },
])('explains unknown agent support (failed=$failed) without reading metrics', async ({ failed, message }) => {
  state.support = 'unknown'
  state.capabilitiesFailed = failed
  await render()
  expect(host.querySelector('[role="status"]')?.textContent).toContain(message)
  expect(state.fetch).not.toHaveBeenCalled()
})

it('reads the selected workload and displays its measured coverage', async () => {
  state.fetch.mockResolvedValueOnce(detail)
  await render()
  await vi.waitFor(() => expect(host.textContent).toContain('Insufficient history'))
  expect(host.textContent).toContain('1 current replica')
  expect(state.fetch).toHaveBeenCalledWith(
    '/metrics/datadog/rightsizing/Deployment/team/api',
    expect.any(AbortSignal),
  )
})

it('removes cached workload evidence when a fresh read is denied', async () => {
  state.fetch
    .mockResolvedValueOnce(detail)
    .mockRejectedValueOnce(new Error('Kubernetes denied this read'))
  await render()
  await vi.waitFor(() => expect(host.textContent).toContain('Insufficient history'))
  await act(async () => {
    await client.invalidateQueries({
      queryKey: [
        'datadog-rightsizing',
        getApiBase(),
        '/metrics/datadog/rightsizing/Deployment/team/api',
      ],
    })
  })
  await vi.waitFor(() =>
    expect(host.querySelector('[role="alert"]')?.textContent).toContain(
      'Kubernetes denied this read',
    ),
  )
  expect(host.textContent).not.toContain('Insufficient history')
})

it('reports disabled cluster approval', async () => {
  state.fetch.mockResolvedValueOnce({ configured: false })
  await render()
  await vi.waitFor(() => expect(host.textContent).toContain('unavailable for this cluster'))
})
