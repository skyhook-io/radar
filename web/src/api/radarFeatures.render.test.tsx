// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { getRadarUpgradeRequirement } from '@skyhook-io/k8s-ui'
import { usePodEnvironment } from './client'
import { usePolicyResource } from './policy'

// The host knows the agent as v1.7.2 before /capabilities, /version-check and
// /api-resources answer; the tests settle those by seeding the cache.
vi.mock('../context/NavCustomization', () => ({ useNavCustomization: () => ({ radarVersion: 'v1.7.2' }) }))

vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true)
const requested: string[] = []
let element: HTMLDivElement
let root: ReturnType<typeof createRoot>
let client: QueryClient

beforeEach(() => {
  requested.length = 0
  vi.stubGlobal('fetch', vi.fn((input: RequestInfo | URL) => {
    const url = String(input)
    requested.push(url)
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
  const { data, error } = usePolicyResource('pods', 'shop', 'web')
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
  expect(element.textContent).toBe('upgrade')

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
