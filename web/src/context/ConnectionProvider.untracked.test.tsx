// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { afterEach, expect, it, vi } from 'vitest'
import { ConnectionProvider } from './ConnectionContext'
import { connectionHeaderValue, resetConnectionBindingForTest } from '../api/connectionBinding'

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })

afterEach(() => {
  vi.unstubAllGlobals()
  vi.useRealTimers()
  resetConnectionBindingForTest()
})

it('a poll that shows another cluster after an untracked switch drops what the tab was showing', async () => {
  vi.useFakeTimers()
  let status = { state: 'connected', context: 'prod', connectionId: 'n-1' }
  vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify(status), { status: 200, headers: { 'Content-Type': 'application/json' } })))
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  const container = document.createElement('div')
  const root = createRoot(container)
  await act(async () => root.render(<QueryClientProvider client={client}><ConnectionProvider>{null}</ConnectionProvider></QueryClientProvider>))
  await act(async () => { await vi.advanceTimersByTimeAsync(100) })
  client.setQueryData(['resource', 'deployments', 'default', 'web'], { cluster: 'prod' })

  // A CAPI connect from another tab; this tab missed its SSE frame.
  status = { state: 'connected', context: 'workload', connectionId: 'n-2' }
  await act(async () => { await client.refetchQueries({ queryKey: ['connection-status'] }) })
  await act(async () => { await vi.advanceTimersByTimeAsync(10) })

  expect(connectionHeaderValue()).toBe('n-2')
  expect(client.getQueryData(['resource', 'deployments', 'default', 'web'])).toBeUndefined()
  act(() => root.unmount())
})
