// @vitest-environment jsdom
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { SearchCoverageEntry } from './searchCoverageNote'
import { coverageOf } from './searchCoverage.fixtures'

vi.mock('../../api/usage-data', () => ({ recordUsageEvent: vi.fn() }))
vi.mock('../../hooks/useRecentResources', () => ({ loadRecentResources: () => [], recordRecentResource: vi.fn() }))
vi.mock('../../api/apiResources', async (importOriginal) => ({ ...(await importOriginal<object>()), useAPIResources: () => ({ data: [] }) }))
vi.mock('./command-items', async (importOriginal) => ({ ...(await importOriginal<object>()), useCommandItems: () => [] }))
const caps = vi.hoisted(() => ({ data: undefined as undefined | { authEnabled?: boolean } }))
vi.mock('../../api/client', async (importOriginal) => ({
  ...(await importOriginal<object>()),
  useNamespaceScope: () => ({ data: undefined }),
  useContexts: () => ({ data: [] }),
  useCapabilities: () => caps,
}))

import { RadarOmnibar } from './RadarOmnibar'

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })
let root: Root
let host: HTMLDivElement
let unsearched: SearchCoverageEntry[]
let searches = 0

const pending: SearchCoverageEntry[] = [{ kind: '*', group: '', reason: 'kind_sync_pending' }]
const failed: SearchCoverageEntry[] = [{ kind: 'SpStall', group: 'stall.sp-live.test', reason: 'kind_sync_timed_out' }]

beforeEach(() => {
  vi.useFakeTimers()
  caps.data = undefined
  searches = 0
  vi.stubGlobal('fetch', vi.fn(async (input: string) => {
    const url = new URL(input, 'http://localhost')
    if (!url.pathname.endsWith('/search')) throw new Error(`Unexpected request: ${input}`)
    searches++
    return new Response(JSON.stringify({ hits: [], total: 0, searched: 0, total_matched: 0, coverage: coverageOf(unsearched) }), { status: 200 })
  }))
  host = document.createElement('div')
  document.body.appendChild(host)
  root = createRoot(host)
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  const noop = vi.fn()
  act(() => {
    root.render(
      <QueryClientProvider client={client}>
        <RadarOmnibar onOpenResource={noop} onNavigateView={noop} onNavigateKind={noop} onSwitchContext={noop} onSetNamespaces={noop} onToggleTheme={noop} />
      </QueryClientProvider>,
    )
  })
})

afterEach(() => {
  act(() => root.unmount())
  host.remove()
  document.body.innerHTML = ''
  vi.unstubAllGlobals()
  vi.useRealTimers()
})

async function typeQuery(text: string) {
  const input = host.querySelector('input')!
  act(() => input.focus())
  act(() => {
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')!.set!.call(input, text)
    input.dispatchEvent(new Event('input', { bubbles: true }))
  })
  await act(async () => { await vi.advanceTimersByTimeAsync(200) })
  await act(async () => { await vi.advanceTimersByTimeAsync(100) })
}

const advance = (ms: number) => act(async () => { await vi.advanceTimersByTimeAsync(ms) })
const page = () => document.body.textContent ?? ''

describe('RadarOmnibar while results load', () => {
  it('re-queries a loading search and ends on the failure once Radar gives up on the kind', async () => {
    unsearched = pending
    await typeQuery('sp-live')
    expect(page()).toContain('Still loading — results may change.')

    await advance(25_000)
    expect(searches).toBeGreaterThanOrEqual(5)
    expect(page()).toContain('Still loading — results may change.')

    // Radar reports a kind that never synced as failed after about 30 s, so
    // the omnibar must still be asking then.
    await advance(6_000)
    unsearched = failed
    await advance(10_000)
    expect(page()).toContain("Couldn't read SpStall; matches of that kind may be missing.")
    expect(page()).not.toContain('Still loading')

    const settled = searches
    await advance(30_000)
    expect(searches).toBe(settled)
  })

  it('still catches a kind that fails 30 s after a watch that started late in warm-up', async () => {
    unsearched = pending
    await typeQuery('sp-live')
    await advance(48_000)
    expect(page()).toContain('Still loading — results may change.')
    unsearched = failed
    await advance(6_000)
    expect(page()).toContain("Couldn't read SpStall; matches of that kind may be missing.")
    const settled = searches
    await advance(30_000)
    expect(searches).toBe(settled)
  })

  it('stops re-querying after its budget and says so', async () => {
    unsearched = pending
    await typeQuery('sp-live')
    await advance(55_000)
    expect(page()).toContain('Still loading — results may change.')
    await advance(6_000)
    expect(page()).toContain('Still loading — try again shortly.')
    const settled = searches
    await advance(30_000)
    expect(searches).toBe(settled)
  })
})

describe('RadarOmnibar namespace notes', () => {
  // Radar decides whether a missing namespace may be named as absent: only a
  // caller who can see every namespace is told it doesn't exist.
  it('says a namespace does not exist when Radar says so, whatever the auth mode', async () => {
    for (const data of [{}, { authEnabled: true }]) {
      caps.data = data
      unsearched = [{ kind: '*', group: '', reason: 'namespace_not_found', namespaces: ['payments-prd'] }]
      await typeQuery(`ns:payments-prd web${'authEnabled' in data ? 's' : ''}`)
      expect(page()).toContain("Namespace 'payments-prd' doesn't exist on this cluster.")
    }
  })

  it('stays scoped to what the caller can see when Radar does not say', async () => {
    for (const data of [{}, { authEnabled: true }]) {
      caps.data = data
      unsearched = [{ kind: '*', group: '', reason: 'user_namespace_not_visible', namespaces: ['payments-prd'] }]
      await typeQuery(`ns:payments-prd web${'authEnabled' in data ? 's' : ''}`)
      expect(page()).toContain("Namespace 'payments-prd' wasn't found in what you can see.")
    }
  })
})

describe('RadarOmnibar read recovery', () => {
  it.each([
    ['alone', []],
    ['followed by a namespace note', [{ kind: '*', group: '', reason: 'radar_namespace_not_collected', namespaces: ['rel-owner'] }]],
    ['preceded by a loading note', pending],
  ] as [string, SearchCoverageEntry[]][])('adds the suggested kind %s, preserves modifiers and terms, and re-checks while open', async (_, siblings) => {
    unsearched = [{ kind: 'Certificate', group: 'cert-manager.io', reason: 'read_error' }, ...siblings]
    await typeQuery('ns:prod')
    act(() => host.querySelector('input')!.dispatchEvent(new KeyboardEvent('keydown', { key: ' ', bubbles: true })))
    await typeQuery('label:app=web image:nginx renewal')
    expect(page()).toContain("Couldn't read Certificate; matches of that kind may be missing. Search kind:Certificate to re-check.")
    const button = [...document.body.querySelectorAll('button')].find((b) => b.textContent === 'kind:Certificate')!
    expect(button).toBeDefined()
    const recoveryText = "Couldn't read Certificate; matches of that kind may be missing. Search kind:Certificate to re-check."
    const siblingText = siblings === pending ? 'Still loading — results may change.' : siblings.length ? "Namespace 'rel-owner' wasn't searched: Radar here watches only some namespaces." : ''
    expect(button.parentElement!.textContent?.trim()).toBe(recoveryText)
    expect(button.parentElement!.parentElement!.textContent).toBe(siblings === pending ? `${siblingText} ${recoveryText}` : `${recoveryText}${siblingText ? ` ${siblingText}` : ''}`)
    act(() => button.click())
    await advance(300)
    await advance(100)
    const requests = vi.mocked(fetch).mock.calls.map(([input]) => new URL(String(input), 'http://localhost').searchParams.get('q'))
    expect(requests.at(-1)).toBe('ns:prod kind:Certificate label:app=web image:nginx renewal')
    expect(host.querySelector('input')!.value).toContain('renewal')
    expect(page()).toContain("Couldn't read Certificate; matches of that kind may be missing.")
    expect([...document.body.querySelectorAll('button')].some((b) => b.textContent === 'kind:Certificate')).toBe(false)
    expect(page()).not.toContain('to re-check.')
  })

  it.each(['kind:Certificate renewal', 'k:certificates renewal'])('keeps an explicit failed search honest: %s', async (query) => {
    unsearched = [{ kind: 'Certificate', group: 'cert-manager.io', reason: 'read_error' }]
    await typeQuery(query)
    expect(page()).toContain("Couldn't read Certificate; matches of that kind may be missing.")
    expect(page()).not.toContain('to re-check.')
  })
})
