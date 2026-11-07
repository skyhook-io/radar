// @vitest-environment jsdom
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { helmInventoryRefetchInterval, useHelmInventory } from './client'
import type { HelmInventory } from '../types'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

vi.mock('../context/ConnectionContext', () => ({ useClusterConnected: () => true, useConnection: () => ({ connection: { state: 'connected' } }) }))

type Seen = { data?: HelmInventory; error: unknown }

const json = (status: number, body: unknown) => new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })

let root: Root | undefined

afterEach(() => {
  act(() => root?.unmount())
  root = undefined
  vi.unstubAllGlobals()
})

async function readInventory(features: Record<string, boolean>, routes: Record<string, () => Response>) {
  const paths: string[] = []
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => {
    const path = new URL(String(input), 'http://radar.test').pathname
    paths.push(path)
    if (path.endsWith('/capabilities')) return json(200, { features })
    const route = Object.keys(routes).find((key) => path.endsWith(key))
    return route ? routes[route]() : json(200, {})
  }))
  const seen: Seen = { error: null }
  function Probe() {
    const { data, error } = useHelmInventory([])
    seen.data = data
    seen.error = error
    return null
  }
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  root = createRoot(document.createElement('div'))
  await act(async () => {
    root!.render(<QueryClientProvider client={client}><Probe /></QueryClientProvider>)
  })
  await vi.waitFor(() => expect(seen.data !== undefined || seen.error !== null).toBe(true))
  return { seen, paths }
}

describe('useHelmInventory', () => {
  it('reads the inventory from a Radar that advertises it', async () => {
    const inventory: HelmInventory = { releases: [], observed: [{ name: 'api', namespace: 'apps', resourceCount: 1 }], storageDenied: { extent: 'all' } }
    const { seen, paths } = await readInventory({ helmInventory: true }, { '/helm/inventory': () => json(200, inventory) })
    expect(seen.data).toEqual(inventory)
    expect(paths.some((path) => path.endsWith('/helm/releases'))).toBe(false)
  })

  it('reads the release list from an older Radar, which is the whole answer', async () => {
    const release = { name: 'api', namespace: 'apps', chart: 'c', chartVersion: '1', appVersion: '1', status: 'deployed', revision: 1, updated: '2026-01-01T00:00:00Z' }
    const { seen, paths } = await readInventory({}, { '/helm/releases': () => json(200, [release]) })
    expect(seen.data).toEqual({ releases: [release], observed: [] })
    expect(paths.some((path) => path.includes('/helm/inventory'))).toBe(false)
  })

  it('keeps an older Radar’s denial an error', async () => {
    const { seen } = await readInventory({}, { '/helm/releases': () => json(403, { error: 'insufficient permissions to list Helm releases' }) })
    expect(seen.data).toBeUndefined()
    expect((seen.error as { status?: number }).status).toBe(403)
  })
})

describe('helmInventoryRefetchInterval', () => {
  const withGap = (action: string): HelmInventory => ({ releases: [], observed: [], objectCoverage: { gaps: [{ code: 'x', action }] } as unknown as HelmInventory['objectCoverage'] })

  it('leaves a settled list alone', () => {
    expect(helmInventoryRefetchInterval(undefined, 0)).toBe(false)
    expect(helmInventoryRefetchInterval({ releases: [], observed: [] }, 0)).toBe(false)
    expect(helmInventoryRefetchInterval(withGap('grant_access'), 0)).toBe(false)
  })

  it('follows both gaps that can clear by themselves', () => {
    expect(helmInventoryRefetchInterval(withGap('retry'), 0)).toBe(5000)
    expect(helmInventoryRefetchInterval(withGap('wait'), 0)).toBe(5000)
  })

  it('slows down, without stopping, once a gap outlasts the quick retries', () => {
    expect(helmInventoryRefetchInterval(withGap('retry'), 24)).toBe(60000)
    expect(helmInventoryRefetchInterval(withGap('wait'), 500)).toBe(60000)
  })
})
