// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { MemoryRouter } from 'react-router-dom'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { ResourcesView } from './ResourcesView'

vi.mock('../../context/ConnectionContext', () => ({ useConnection: () => ({ connection: { state: 'connected', context: 'test' } }) }))
vi.mock('../dock', () => ({ useOpenLogs: () => () => {}, useOpenWorkloadLogs: () => () => {} }))
vi.mock('../../context/RadarUpgradeHost', () => ({ useRadarUpgradeHost: () => ({}) }))
vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true)

const requested: URL[] = []
let root: ReturnType<typeof createRoot>
let client: QueryClient

beforeEach(() => {
  requested.length = 0
  vi.stubGlobal('matchMedia', () => ({ matches: false, addEventListener: () => {}, removeEventListener: () => {} }))
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

async function certExpiryScope(namespaces: string[]) {
  await act(async () => {
    root.render(
      <QueryClientProvider client={client}>
        <MemoryRouter initialEntries={['/resources/secrets']}>
          <ResourcesView namespaces={namespaces} selectedResource={null} onResourceClick={() => {}} />
        </MemoryRouter>
      </QueryClientProvider>,
    )
  })
  await act(async () => { await new Promise((resolve) => setTimeout(resolve, 0)) })
  const certExpiry = requested.filter((url) => url.pathname.endsWith('/secrets/certificate-expiry'))
  if (certExpiry.length === 0) throw new Error(`no certificate-expiry request: ${requested.map(String).join(', ')}`)
  return certExpiry.map((url) => url.searchParams.get('namespaces'))
}

it('scopes certificate expiry to the namespaces in view', async () => {
  expect(await certExpiryScope(['team-a', 'team-b'])).toEqual(['team-a,team-b'])
})

it('asks for every namespace when the view covers all', async () => {
  expect(await certExpiryScope([])).toEqual([null])
})
