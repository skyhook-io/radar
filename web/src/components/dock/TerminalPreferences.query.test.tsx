// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { QueryClient, QueryClientProvider, useQuery } from '@tanstack/react-query'
import { expect, it, vi } from 'vitest'
import { useTerminalCopyOnSelect } from '../../../../packages/k8s-ui/src/components/dock/terminalPreferences'
import { TerminalPreferences } from './TerminalPreferences'

const { request } = vi.hoisted(() => ({ request: vi.fn<(signal: AbortSignal) => Promise<{ authEnabled: boolean; username: string }>>() }))
vi.mock('../../api/client', () => ({
  useAuthMe: () => useQuery({
    queryKey: ['auth-me'], queryFn: ({ signal }) => request(signal), staleTime: 300000,
  }),
}))
Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })

it('does not verify cached success when removeQueries cancels an account recheck', async () => {
  const responses: Array<(value: { authEnabled: boolean; username: string }) => void> = []
  request.mockImplementation((signal: AbortSignal) => new Promise((resolve, reject) => {
    responses.push(resolve)
    signal.addEventListener('abort', () => reject(new DOMException('Aborted', 'AbortError')), { once: true })
  }))
  let clock = 1000
  const now = vi.spyOn(Date, 'now').mockImplementation(() => clock)
  const client = new QueryClient({ defaultOptions: { queries: { retry: false, refetchOnWindowFocus: false } } })
  client.setQueryData(['auth-me'], { authEnabled: true, username: 'owned-query-a' })
  localStorage.setItem('radar:terminal:copy-on-select:account:owned-query-a', 'true')
  const root = createRoot(document.createElement('div'))
  let preference: ReturnType<typeof useTerminalCopyOnSelect> | undefined
  function Terminal() { preference = useTerminalCopyOnSelect(); return null }
  try {
    await act(async () => root.render(<QueryClientProvider client={client}><TerminalPreferences><Terminal /></TerminalPreferences></QueryClientProvider>))
    clock = 2000
    await act(async () => responses.shift()?.({ authEnabled: true, username: 'owned-query-a' }))
    expect(preference?.getState()).toBe('on')
    clock = 3000
    await act(async () => window.dispatchEvent(new Event('focus')))
    expect(preference?.getState()).toBe('pending')
    expect(request).toHaveBeenCalledTimes(2)
    clock = 3100
    await act(async () => client.removeQueries({ queryKey: ['auth-me'] }))
    expect(preference?.getState()).toBe('pending')
    expect(preference?.available).toBe(false)
    expect(request.mock.calls.length).toBeGreaterThanOrEqual(3)
    responses.shift()
    clock = 4000
    await act(async () => responses.shift()?.({ authEnabled: true, username: 'owned-query-b' }))
    expect(preference?.available).toBe(true)
    expect(preference?.getState()).toBe('off')
  } finally {
    await act(async () => root.unmount())
    client.clear()
    now.mockRestore()
    request.mockReset()
    localStorage.clear()
  }
})
