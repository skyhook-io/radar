// @vitest-environment jsdom
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { NavCustomizationProvider } from '../../context/NavCustomization'
import { useCloudHintsEnabled } from './cloudHints'
import { useContextSwitchCloudRow } from './ContextSwitchCloudRow'
import { recordContextSwitch } from './contextSwitchLog'

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })
let root: Root
let client: QueryClient
let capabilities: Record<string, unknown>

beforeEach(() => {
  client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  const element = document.createElement('div')
  document.body.appendChild(element)
  root = createRoot(element)
  vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify(capabilities), { status: 200 })))
})

afterEach(async () => {
  await act(async () => root.unmount())
  client.clear()
  document.body.replaceChildren()
  vi.unstubAllGlobals()
})

function Probe() {
  return <span data-testid="gate">{useCloudHintsEnabled() ? 'on' : 'off'}</span>
}

async function gate(embedded: boolean) {
  await act(async () => {
    root.render(
      <QueryClientProvider client={client}>
        <NavCustomizationProvider value={{ embedded }}>
          <Probe />
        </NavCustomizationProvider>
      </QueryClientProvider>,
    )
  })
  await vi.waitFor(async () => {
    await act(async () => { await new Promise(resolve => setTimeout(resolve, 0)) })
    expect(client.getQueryData(['capabilities'])).toBeDefined()
  })
  return document.querySelector('[data-testid="gate"]')?.textContent
}

describe('Radar Cloud hint gate', () => {
  it('is on for standalone Radar when the server offers Cloud', async () => {
    capabilities = { cloudConnect: { lane: 'wizard', appUrl: 'https://cloud.example' } }
    expect(await gate(false)).toBe('on')
  })

  it('stays off in an embedded host even when the capability is present', async () => {
    capabilities = { cloudConnect: { lane: 'wizard', appUrl: 'https://cloud.example' } }
    expect(await gate(true)).toBe('off')
  })

  it('stays off when the server offers nothing (RADAR_CLOUD_FUNNEL=off, cloud mode, tunneled)', async () => {
    capabilities = {}
    expect(await gate(false)).toBe('off')
  })
})

function RowProbe({ view }: { view: string }) {
  const row = useContextSwitchCloudRow(view)
  return <span data-testid="row">{row.names ? `See ${row.what} from ${row.names[0]} and ${row.names[1]}` : 'none'}</span>
}

describe('Switching row', () => {
  async function rowOn(view: string) {
    capabilities = { cloudConnect: { lane: 'wizard', appUrl: 'https://cloud.example' } }
    window.sessionStorage.clear()
    window.localStorage.clear()
    recordContextSwitch('prod', 'staging', Date.now() - 3000)
    recordContextSwitch('staging', 'prod', Date.now() - 2000)
    recordContextSwitch('prod', 'staging', Date.now() - 1000)
    await act(async () => {
      root.render(
        <QueryClientProvider client={client}>
          <NavCustomizationProvider value={{}}>
            <RowProbe view={view} />
          </NavCustomizationProvider>
        </QueryClientProvider>,
      )
    })
    await vi.waitFor(async () => {
      await act(async () => { await new Promise(resolve => setTimeout(resolve, 0)) })
      expect(client.getQueryData(['capabilities'])).toBeDefined()
    })
    return document.querySelector('[data-testid="row"]')?.textContent
  }

  it('shows on an aggregate page, naming what that page lists', async () => {
    expect(await rowOn('issues')).toBe('See issues from prod and staging')
  })

  it('stays away from pages without a fleet counterpart', async () => {
    expect(await rowOn('topology')).toBe('none')
  })
})
