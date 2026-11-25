// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { MutationCache, QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { afterEach, expect, it, vi } from 'vitest'
import { useSetContextProxy } from './client'

vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true)
afterEach(() => {
  vi.unstubAllGlobals()
  vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true)
})

it('announces a saved proxy through the global success toast', async () => {
  vi.stubGlobal('fetch', vi.fn<typeof fetch>(async () => new Response('{"context":"prod","current":false}', { status: 200 })))
  const toasts: (string | undefined)[] = []
  const client = new QueryClient({
    mutationCache: new MutationCache({ onSuccess: (_d, _v, _c, m) => { toasts.push(m.options.meta?.successMessage) } }),
  })
  let mutate: (() => Promise<unknown>) | undefined
  function Probe() {
    const save = useSetContextProxy()
    mutate = () => save.mutateAsync({ name: 'prod', proxyUrl: 'http://proxy:3128' })
    return null
  }
  const root = createRoot(document.createElement('div'))
  await act(async () => { root.render(<QueryClientProvider client={client}><Probe /></QueryClientProvider>) })
  await act(async () => { await mutate!() })
  act(() => root.unmount())
  expect(toasts).toEqual([expect.stringMatching(/proxy/i)])
})
