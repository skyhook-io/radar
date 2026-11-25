// @vitest-environment jsdom
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { useSwitchContext } from '../api/client'
import { useContextSwitchOutcome } from '../components/useContextSwitchFlow'
import { ToastProvider } from '../components/ui/Toast'
import { parseContextName } from '../utils/context-name'
import { ContextSwitchProvider, useContextSwitch } from './ContextSwitchContext'
import { ConnectionProvider, useConnection } from './ConnectionContext'

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })

let client: QueryClient
let root: Root
let container: HTMLDivElement
let retries: number
const seen = vi.fn<(value: ReturnType<typeof useConnection>) => void>()
const latest = () => seen.mock.lastCall?.[0]
let focused: boolean
let pollContext: string

function Probe() {
  seen(useConnection())
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
  seen.mockClear()
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
  client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
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
    expect(latest()?.connection.state).toBe('disconnected')
    expect(latest()?.connection.disconnectedAt).toBe('2026-10-30T14:02:00Z')
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
    await advance(30_000)

    focused = true
    await act(async () => {
      window.dispatchEvent(new Event('focus'))
      document.dispatchEvent(new Event('visibilitychange'))
    })
    await advance(10)
    // focus and visibilitychange together are one return, so one retry.
    expect(retries).toBe(7)
  })

  it('alt-tabbing back and forth retries no more than every ten seconds', async () => {
    focused = true
    await advance(15_000)
    expect(retries).toBe(1) // the scheduled attempt at 10s
    // Back to the window every 3s for 30s, as when switching to a terminal
    // to fix the VPN and back.
    for (let i = 0; i < 10; i++) {
      await act(async () => {
        window.dispatchEvent(new Event('blur'))
        window.dispatchEvent(new Event('focus'))
        document.dispatchEvent(new Event('visibilitychange'))
      })
      await advance(3_000)
    }
    // 30s of returns: at most one attempt per ten seconds, scheduled ones included.
    expect(retries).toBeLessThanOrEqual(4)
    expect(retries).toBeGreaterThanOrEqual(3)
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
    expect(latest()?.connection.context).toBe('staging')
    const before = retries
    // A fresh outage retries after the initial 10s, not the unattended 5 minutes.
    await advance(10_500)
    expect(retries).toBe(before + 1)
  })
})

it.each(['failed', 'succeeded'] as const)('ends the switch overlay when a poll sees %s before acknowledgement recovery', async phase => {
  const operation = {
    id: `switch-${phase}`, target: 'staging', phase,
    startedAt: '2026-10-30T14:02:00Z', syncTimeoutSeconds: 30, progress: '',
    ...(phase === 'failed' && { error: 'Credentials expired', errorType: 'auth' }),
  }
  let loseAcknowledgement!: () => void
  vi.mocked(fetch).mockImplementation(async (_input, init) => {
    if (init?.method === 'POST') {
      return new Promise<Response>((_, reject) => { loseAcknowledgement = () => reject(new TypeError('Failed to fetch')) })
    }
    return jsonResponse(200, { state: 'connected', context: phase === 'succeeded' ? operation.target : 'prod', contextSwitch: operation })
  })

  const applyContextChange = vi.fn()
  function SwitchProbe() {
    const { startSwitch, isSwitching, failedSwitch } = useContextSwitch()
    const mutation = useSwitchContext()
    useContextSwitchOutcome(applyContextChange)
    return <button onClick={() => {
      startSwitch(parseContextName(operation.target))
      mutation.mutate({ name: operation.target })
    }}>{isSwitching ? 'Switching' : failedSwitch?.error ?? 'Idle'}</button>
  }

  act(() => root.render(
    <QueryClientProvider client={client}>
      <ConnectionProvider>
        <ToastProvider>
          <ContextSwitchProvider>
            <Probe />
            <SwitchProbe />
          </ContextSwitchProvider>
        </ToastProvider>
      </ConnectionProvider>
    </QueryClientProvider>,
  ))
  await act(async () => container.querySelector('button')!.click())
  await act(async () => { await client.refetchQueries({ queryKey: ['connection-status'] }) })
  await advance(10)
  expect(latest()?.contextSwitch).toEqual(operation)
  expect(container.textContent).toBe('Switching')
  expect(applyContextChange).not.toHaveBeenCalled()

  await act(async () => loseAcknowledgement())
  await advance(10)
  expect(container.textContent).toBe(phase === 'failed' ? 'Credentials expired' : 'Idle')
  if (phase === 'succeeded') expect(applyContextChange).toHaveBeenCalledExactlyOnceWith(operation.target, operation.id)
})

it('does not replay a dismissed switch failure when a lost acknowledgement recovers late', async () => {
  const failed = {
    id: 'switch-dismissed', target: 'staging', phase: 'failed' as const,
    startedAt: '2026-10-30T14:02:00Z', syncTimeoutSeconds: 30, progress: '',
    error: 'Credentials expired', errorType: 'auth',
  }
  let phase: 'preflight' | 'failed' = 'preflight'
  let loseAcknowledgement!: () => void
  vi.mocked(fetch).mockImplementation(async (_input, init) => {
    if (init?.method === 'POST') {
      return new Promise<Response>((_, reject) => { loseAcknowledgement = () => reject(new TypeError('Failed to fetch')) })
    }
    const contextSwitch = phase === 'failed' ? failed : { ...failed, phase, error: undefined, errorType: undefined }
    return jsonResponse(200, { state: 'connected', context: 'prod', contextSwitch })
  })

  function SwitchProbe() {
    const { startSwitch, failedSwitch, setFailedSwitch } = useContextSwitch()
    const mutation = useSwitchContext()
    useContextSwitchOutcome(vi.fn())
    return (
      <>
        <button id="start" onClick={() => { startSwitch(parseContextName(failed.target)); mutation.mutate({ name: failed.target }) }} />
        <button id="dismiss" onClick={() => setFailedSwitch(null)} />
        <span id="failure">{failedSwitch?.error ?? 'none'}</span>
      </>
    )
  }

  act(() => root.render(
    <QueryClientProvider client={client}>
      <ConnectionProvider>
        <ToastProvider>
          <ContextSwitchProvider>
            <Probe />
            <SwitchProbe />
          </ContextSwitchProvider>
        </ToastProvider>
      </ConnectionProvider>
    </QueryClientProvider>,
  ))
  await act(async () => (container.querySelector('#start') as HTMLButtonElement).click())
  await act(async () => { await client.refetchQueries({ queryKey: ['connection-status'] }) })
  await advance(10)
  phase = 'failed'
  await act(async () => { await client.refetchQueries({ queryKey: ['connection-status'] }) })
  await advance(10)
  expect(container.querySelector('#failure')!.textContent).toBe('Credentials expired')

  await act(async () => (container.querySelector('#dismiss') as HTMLButtonElement).click())
  expect(container.querySelector('#failure')!.textContent).toBe('none')
  await act(async () => loseAcknowledgement())
  await act(async () => { await client.refetchQueries({ queryKey: ['connection-status'] }) })
  await advance(10)
  expect(container.querySelector('#failure')!.textContent).toBe('none')
})

it('a delayed acknowledgement does not reopen a switch whose failure was already shown', async () => {
  const failed = {
    id: 'switch-late-ack', target: 'staging', phase: 'failed' as const,
    startedAt: '2026-10-30T14:02:00Z', syncTimeoutSeconds: 30, progress: '',
    error: 'Credentials expired', errorType: 'auth',
  }
  const preflight = { ...failed, phase: 'preflight' as const, error: undefined, errorType: undefined }
  let phase: 'preflight' | 'failed' = 'preflight'
  let stallPolls = false
  let acknowledge!: () => void
  vi.mocked(fetch).mockImplementation(async (_input, init) => {
    if (init?.method === 'POST') {
      return new Promise<Response>(resolve => { acknowledge = () => resolve(jsonResponse(202, preflight)) })
    }
    if (stallPolls) return new Promise<Response>(() => {})
    return jsonResponse(200, { state: 'connected', context: 'prod', contextSwitch: phase === 'failed' ? failed : preflight })
  })

  function SwitchProbe() {
    const { startSwitch, failedSwitch, setFailedSwitch } = useContextSwitch()
    const mutation = useSwitchContext()
    useContextSwitchOutcome(vi.fn())
    return (
      <>
        <button id="start" onClick={() => { startSwitch(parseContextName(failed.target)); mutation.mutate({ name: failed.target }) }} />
        <button id="dismiss" onClick={() => setFailedSwitch(null)} />
        <span id="failure">{failedSwitch?.error ?? 'none'}</span>
      </>
    )
  }

  act(() => root.render(
    <QueryClientProvider client={client}>
      <ConnectionProvider>
        <ToastProvider>
          <ContextSwitchProvider>
            <Probe />
            <SwitchProbe />
          </ContextSwitchProvider>
        </ToastProvider>
      </ConnectionProvider>
    </QueryClientProvider>,
  ))
  await act(async () => (container.querySelector('#start') as HTMLButtonElement).click())
  await act(async () => { await client.refetchQueries({ queryKey: ['connection-status'] }) })
  await advance(10)
  phase = 'failed'
  await act(async () => { await client.refetchQueries({ queryKey: ['connection-status'] }) })
  await advance(10)
  expect(container.querySelector('#failure')!.textContent).toBe('Credentials expired')
  await act(async () => (container.querySelector('#dismiss') as HTMLButtonElement).click())

  stallPolls = true
  await act(async () => acknowledge())
  await advance(10)
  const status = client.getQueryData<{ contextSwitch?: { phase: string } }>(['connection-status'])
  expect(status?.contextSwitch?.phase).toBe('failed')
  expect(container.querySelector('#failure')!.textContent).toBe('none')
})
