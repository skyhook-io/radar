// @vitest-environment jsdom
import { act, useEffect } from 'react'
import { createRoot } from 'react-dom/client'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { getRadarUpgradeRequirement } from '@skyhook-io/k8s-ui'
import { ApiError, isNotFoundError, useApplications, useCapacityPoolDetail, useDrainPlan, usePodEnvironment, useWorkloadHistory } from './client'
import { getApiBase } from './config'
import { usePolicyResource } from './policy'
import { isRadarFeatureUnsupported } from './radarFeatures'

// By default the host knows the agent as v1.7.2 before /capabilities,
// /version-check and /api-resources answer; the tests settle those by seeding
// the cache.
const host = vi.hoisted(() => ({ radarVersion: 'v1.7.2' as string | undefined }))
vi.mock('../context/RadarUpgradeHost', () => ({ useRadarUpgradeHost: () => host }))

const CHI_404 = () => new Response('404 page not found\n', { status: 404, headers: { 'Content-Type': 'text/plain; charset=utf-8' } })
let chiRoutes: string[] = []

vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true)
const requested: string[] = []
let element: HTMLDivElement
let root: ReturnType<typeof createRoot>
let client: QueryClient

beforeEach(() => {
  requested.length = 0
  host.radarVersion = 'v1.7.2'
  chiRoutes = []
  vi.stubGlobal('fetch', vi.fn((input: RequestInfo | URL) => {
    const url = String(input)
    requested.push(url)
    if (chiRoutes.some((route) => url.includes(route))) return Promise.resolve(CHI_404())
    if (url.includes('/capacity/pools/')) return Promise.resolve(Response.json({ pool: { name: 'default' } }))
    if (url.endsWith('/environment')) return Promise.resolve(Response.json({ containers: [], coverage: {} }))
    if (url.includes('/policy/resource/')) return Promise.resolve(Response.json({ evaluated: true, status: 'ready' }))
    return new Promise<Response>(() => {})
  }))
  element = document.createElement('div')
  root = createRoot(element)
  client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
})

afterEach(async () => {
  await act(async () => root.unmount())
  client.clear()
  vi.unstubAllGlobals()
  vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true)
})

async function render(node: React.ReactNode) {
  await act(async () => { root.render(<QueryClientProvider client={client}>{node}</QueryClientProvider>) })
  await settle()
}

// Lets queries started by the last render run to completion.
async function settle() {
  await act(async () => { await new Promise((resolve) => setTimeout(resolve, 0)) })
}

const asked = (suffix: string) => requested.some((url) => url.includes(suffix))

function Environment() {
  const { data, error } = usePodEnvironment('shop', 'web')
  return <span>{data ? 'data' : getRadarUpgradeRequirement(error) ? 'upgrade' : 'pending'}</span>
}

function Policy() {
  const { data, error, isLoading, isError } = usePolicyResource('pods', 'shop', 'web')
  if (isLoading) return <span>loading</span>
  if (data && isError) return <span>inconsistent</span>
  return <span>{data?.status ?? (getRadarUpgradeRequirement(error) ? 'upgrade' : 'pending')}</span>
}

it('fetches for real once the agent turns out to advertise the feature', async () => {
  await render(<Environment />)
  expect(element.textContent).toBe('upgrade')
  expect(asked('/environment')).toBe(false)

  await act(async () => { client.setQueryData(['capabilities'], { features: { podEnvironment: true } }) })
  await settle()
  await settle()
  expect(asked('/environment')).toBe(true)
  expect(element.textContent).toBe('data')
})

it('turns an unsupported policy answer into not installed once discovery shows no policy engine', async () => {
  await render(<Policy />)
  // No upgrade note yet: discovery may still withdraw it.
  expect(element.textContent).toBe('loading')

  await act(async () => { client.setQueryData(['api-resources'], [{ group: '', name: 'pods' }]) })
  await settle()
  expect(element.textContent).toBe('not_installed')
  expect(asked('/policy/resource/')).toBe(false)
})

it('keeps the upgrade note when discovery shows an OpenReports policy engine', async () => {
  client.setQueryData(['api-resources'], [{ group: 'openreports.io', name: 'reports' }])
  await render(<Policy />)
  expect(element.textContent).toBe('upgrade')
})

it('shows the upgrade note when discovery fails', async () => {
  const answer = vi.mocked(fetch).getMockImplementation()!
  vi.mocked(fetch).mockImplementation((input, init) =>
    String(input).endsWith('/api-resources')
      ? Promise.resolve(Response.json({ error: 'discovery down' }, { status: 500 }))
      : answer(input, init),
  )
  await render(<Policy />)
  await settle()
  expect(element.textContent).toBe('upgrade')
})

function PoolDetail() {
  const { data, error } = useCapacityPoolDetail('default')
  if (data) return <span>data</span>
  if (isNotFoundError(error)) return <span>pool not found</span>
  return <span>{getRadarUpgradeRequirement(error) ? 'upgrade' : 'pending'}</span>
}

it('never asks an old Radar for a capacity deep link, and does not call the pool missing', async () => {
  await render(<PoolDetail />)
  expect(element.textContent).toBe('upgrade')
  expect(asked('/capacity/')).toBe(false)
})

it("reads the router's 404 on a capacity deep link as unsupported when the version is unknown", async () => {
  host.radarVersion = undefined
  chiRoutes = ['/capacity/pools/']
  await render(<PoolDetail />)
  expect(element.textContent).toBe('upgrade')
  expect(requested.filter((url) => url.includes('/capacity/pools/'))).toHaveLength(1)
})

it("prefers the agent's own version over a stale host version", async () => {
  await render(<PoolDetail />)
  expect(element.textContent).toBe('upgrade')

  await act(async () => { client.setQueryData(['version-check', getApiBase()], { currentVersion: 'v1.12.0' }) })
  await settle()
  await settle()
  expect(asked('/capacity/pools/default')).toBe(true)
  expect(element.textContent).toBe('data')
})

const probe: { drainPlan?: ReturnType<typeof useDrainPlan> } = {}
function DrainPlanProbe() {
  const drainPlan = useDrainPlan()
  useEffect(() => { probe.drainPlan = drainPlan })
  return null
}

it('never asks a Radar older than v1.14 for a drain plan', async () => {
  host.radarVersion = 'v1.13.1'
  await render(<DrainPlanProbe />)
  const error = await probe.drainPlan!.mutateAsync({ name: 'worker-1', options: { deleteEmptyDirData: false, force: false } })
    .catch((e: unknown) => e)
  expect(isRadarFeatureUnsupported(error, 'drainPlan')).toBe(true)
  expect(asked('/drain-plan')).toBe(false)
})

it("reads the router's 404 on a drain plan as unsupported, and a handler 404 as an error", async () => {
  host.radarVersion = undefined
  chiRoutes = ['/nodes/worker-1/drain-plan']
  await render(<DrainPlanProbe />)
  const options = { deleteEmptyDirData: false, force: false }
  const unsupported = await probe.drainPlan!.mutateAsync({ name: 'worker-1', options }).catch((e: unknown) => e)
  expect(isRadarFeatureUnsupported(unsupported, 'drainPlan')).toBe(true)

  vi.mocked(fetch).mockImplementationOnce(() =>
    Promise.resolve(Response.json({ error: 'nodes "worker-2" not found' }, { status: 404 })),
  )
  const missing = await probe.drainPlan!.mutateAsync({ name: 'worker-2', options }).catch((e: unknown) => e)
  expect(missing).toBeInstanceOf(ApiError)
  expect(isRadarFeatureUnsupported(missing)).toBe(false)
})

function Applications() {
  const { data, error } = useApplications([])
  return <span>{data ? 'data' : getRadarUpgradeRequirement(error) ? 'upgrade' : 'pending'}</span>
}

it('never asks a Radar older than v1.8 for applications', async () => {
  await render(<Applications />)
  expect(element.textContent).toBe('upgrade')
  expect(asked('/applications')).toBe(false)
})

function History() {
  const { data, error } = useWorkloadHistory('deployments', 'shop', 'web', 'apps')
  return <span>{data ? 'data' : isRadarFeatureUnsupported(error, 'workloadHistory') ? 'fallback' : 'pending'}</span>
}

it('never asks a Radar whose capabilities omit workload history', async () => {
  client.setQueryData(['capabilities'], { features: { policyResource: true } })
  await render(<History />)
  expect(element.textContent).toBe('fallback')
  expect(asked('/history')).toBe(false)
})

it("reads the router's 404 on workload history as unsupported before capabilities answer", async () => {
  host.radarVersion = undefined
  chiRoutes = ['/workloads/deployments/shop/web/history']
  await render(<History />)
  expect(element.textContent).toBe('fallback')
  expect(requested.filter((url) => url.includes('/history'))).toHaveLength(1)
})

it('asks again once capabilities confirm workload history after a probe hit an older Radar', async () => {
  host.radarVersion = undefined
  chiRoutes = ['/workloads/deployments/shop/web/history']
  await render(<History />)
  expect(element.textContent).toBe('fallback')

  chiRoutes = []
  await act(async () => { client.setQueryData(['capabilities'], { features: { workloadHistory: true } }) })
  await settle()
  await settle()
  expect(requested.filter((url) => url.includes('/history'))).toHaveLength(2)
})
