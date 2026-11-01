// @vitest-environment jsdom
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ConnectionProvider, useConnection } from './ConnectionContext'

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })

let root: Root
let container: HTMLDivElement
let retries: number
let latest: ReturnType<typeof useConnection> | null
let focused: boolean
let pollContext: string

function Probe() {
  latest = useConnection()
  return null
}

function jsonResponse(status: number, body: unknown) {
  return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })
}

async function advance(ms: number) {
  await act(async () => {
    await vi.advanceTimersByTimeAsync(ms)
  })
}

beforeEach(async () => {
  vi.useFakeTimers()
  retries = 0
  latest = null
  focused = false
  pollContext = 'prod'
  vi.spyOn(document, 'hasFocus').mockImplementation(() => focused)
  vi.stubGlobal('fetch', vi.fn(async (input: string) => {
    const url = new URL(String(input), 'http://localhost')
    if (url.pathname === '/api/connection/retry') {
      retries += 1
      return jsonResponse(503, { error: 'cluster unreachable', errorType: 'network' })
    }
    if (url.pathname === '/api/connection') {
      return jsonResponse(200, {
        state: 'disconnected',
        context: pollContext,
        error: 'cluster unreachable',
        errorType: 'network',
        disconnectedAt: '2026-10-30T14:02:00Z',
      })
    }
    return jsonResponse(404, { error: 'not found' })
  }))

  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  await act(async () => {
    root.render(
      <QueryClientProvider client={client}>
        <ConnectionProvider>
          <Probe />
        </ConnectionProvider>
      </QueryClientProvider>,
    )
  })
  // Let the first status poll land.
  await advance(100)
})

afterEach(() => {
  act(() => root.unmount())
  container.remove()
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
  vi.useRealTimers()
})

// Attempts land at 10s, 30s, 70s, 130s and 190s (10s doubling to a 60s cap).
const FIVE_ATTEMPTS_MS = 190_000

describe('ConnectionProvider auto-retry', () => {
  it('carries the outage start from the server', () => {
    expect(latest?.connection.state).toBe('disconnected')
    expect(latest?.connection.disconnectedAt).toBe('2026-10-30T14:02:00Z')
  })

  it('keeps retrying every minute while someone is looking', async () => {
    focused = true
    await advance(FIVE_ATTEMPTS_MS)
    expect(retries).toBe(5)
    await advance(60_000)
    expect(retries).toBe(6)
  })

  it('slows to every five minutes when nobody is looking, then retries at once on return', async () => {
    await advance(FIVE_ATTEMPTS_MS)
    expect(retries).toBe(5)

    await advance(60_000)
    expect(retries).toBe(5)
    await advance(240_000)
    expect(retries).toBe(6)

    focused = true
    await act(async () => {
      window.dispatchEvent(new Event('focus'))
      document.dispatchEvent(new Event('visibilitychange'))
    })
    await advance(10)
    // focus and visibilitychange together are one return, so one retry.
    expect(retries).toBe(7)
  })

  it('retries at once when the network comes back', async () => {
    focused = true
    await advance(15_000)
    const before = retries
    await act(async () => {
      window.dispatchEvent(new Event('online'))
    })
    await advance(10)
    expect(retries).toBe(before + 1)
  })
})

describe('ConnectionProvider auto-retry across contexts and background windows', () => {
  it('retries at once when the network comes back, even in a hidden window', async () => {
    await advance(FIVE_ATTEMPTS_MS)
    const before = retries
    const visibility = vi.spyOn(document, 'visibilityState', 'get').mockReturnValue('hidden')
    await act(async () => {
      window.dispatchEvent(new Event('online'))
    })
    await advance(10)
    expect(retries).toBe(before + 1)
    visibility.mockRestore()
  })
  it('starts a fresh count and backoff for another context', async () => {
    await advance(FIVE_ATTEMPTS_MS)
    expect(retries).toBe(5)
    // The next status poll reports a different context that is also down.
    pollContext = 'staging'
    await advance(30_000)
    await advance(100)
    expect(latest?.connection.context).toBe('staging')
    const before = retries
    // A fresh outage retries after the initial 10s, not the unattended 5 minutes.
    await advance(10_500)
    expect(retries).toBe(before + 1)
  })
})
