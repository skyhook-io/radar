// @vitest-environment jsdom
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { ConnectionProvider, useConnection } from './ConnectionContext'
import { connectionHeaderValue, resetConnectionBindingForTest } from '../api/connectionBinding'

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })

let root: Root
let container: HTMLDivElement
const seen = vi.fn<(value: ReturnType<typeof useConnection>) => void>()
const latest = () => seen.mock.lastCall?.[0]
let polls: Array<(body: unknown) => void>

function Probe() {
  seen(useConnection())
  return null
}

const statusA = { state: 'connected', context: 'A', connectionId: 'n-1' }

beforeEach(async () => {
  vi.useFakeTimers()
  seen.mockClear()
  resetConnectionBindingForTest()
  polls = []
  vi.stubGlobal('fetch', vi.fn((input: string) => {
    const url = new URL(String(input), 'http://localhost')
    if (url.pathname !== '/api/connection') {
      return Promise.resolve(new Response('{}', { status: 404 }))
    }
    return new Promise<Response>(resolve => {
      polls.push(body => resolve(new Response(JSON.stringify(body), { status: 200, headers: { 'Content-Type': 'application/json' } })))
    })
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
})

afterEach(() => {
  act(() => root.unmount())
  container.remove()
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
  vi.useRealTimers()
  resetConnectionBindingForTest()
})

it('keeps the connection SSE delivered when an older poll resolves after it', async () => {
  await act(async () => {
    await vi.advanceTimersByTimeAsync(10)
  })
  expect(polls).toHaveLength(1)
  await act(async () => {
    polls[0](statusA)
    await vi.advanceTimersByTimeAsync(10)
  })
  expect(connectionHeaderValue()).toBe('n-1')

  // The next poll leaves while A still serves...
  await act(async () => {
    await vi.advanceTimersByTimeAsync(31_000)
  })
  expect(polls).toHaveLength(2)

  // ...then SSE reports B before that poll's answer arrives.
  act(() => {
    latest()?.updateFromSSE({ state: 'connected', context: 'B', connectionId: 'n-2' } as Parameters<NonNullable<ReturnType<typeof latest>>['updateFromSSE']>[0])
  })
  expect(connectionHeaderValue()).toBe('n-2')

  await act(async () => {
    polls[1](statusA)
    await vi.advanceTimersByTimeAsync(10)
  })
  expect(latest()?.connection.context).toBe('B')
  expect(connectionHeaderValue()).toBe('n-2')
})
