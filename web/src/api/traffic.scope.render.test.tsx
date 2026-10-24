// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { useTrafficFlows, useTrafficRecords } from './traffic'

vi.mock('../context/RadarUpgradeHost', () => ({ useRadarUpgradeHost: () => ({}) }))
vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true)

const requested: URL[] = []
let root: ReturnType<typeof createRoot>
let client: QueryClient

beforeEach(() => {
  requested.length = 0
  vi.stubGlobal('fetch', vi.fn((input: RequestInfo | URL) => {
    requested.push(new URL(String(input), 'http://radar.invalid'))
    return new Promise<Response>(() => {})
  }))
  root = createRoot(document.createElement('div'))
  client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
})

afterEach(async () => {
  await act(async () => root.unmount())
  client.clear()
  vi.unstubAllGlobals()
  vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true)
})

function Traffic({ namespaces }: { namespaces: string[] }) {
  useTrafficFlows({ namespaces })
  useTrafficRecords({ namespaces, match: { endpoints: [{ namespace: 'team-a', name: 'web' }] } })
  return null
}

async function scopeOf(namespaces: string[]) {
  await act(async () => { root.render(<QueryClientProvider client={client}><Traffic namespaces={namespaces} /></QueryClientProvider>) })
  await act(async () => { await new Promise((resolve) => setTimeout(resolve, 0)) })
  const flows = requested.find((url) => url.pathname.endsWith('/traffic/flows'))
  const records = requested.find((url) => url.pathname.endsWith('/traffic/flows/records'))
  if (!flows || !records) throw new Error(`missing traffic requests: ${requested.map(String).join(', ')}`)
  const scope = (url: URL) => ({ namespace: url.searchParams.get('namespace'), namespaces: url.searchParams.get('namespaces') })
  return { flows: scope(flows), records: scope(records) }
}

it('sends every namespace in view, since the server does not apply the stored pick', async () => {
  const want = { namespace: null, namespaces: 'team-a,team-b' }
  expect(await scopeOf(['team-a', 'team-b'])).toEqual({ flows: want, records: want })
})

it('sends a single namespace as namespace, and none for all namespaces', async () => {
  const one = { namespace: 'team-a', namespaces: null }
  expect(await scopeOf(['team-a'])).toEqual({ flows: one, records: one })
  requested.length = 0
  client.clear()
  const all = { namespace: null, namespaces: null }
  expect(await scopeOf([])).toEqual({ flows: all, records: all })
})
