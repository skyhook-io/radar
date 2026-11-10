// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { afterEach, expect, it, vi } from 'vitest'
import { useCapabilities, useResourceAudit } from './client'
import { setApiBase } from './config'

afterEach(() => {
  setApiBase('/api')
  vi.unstubAllGlobals()
})

it.each(['supported', 'older', 'failed', 'namespaced'] as const)('avoids premature and duplicate audit requests across discovery: %s', async (state) => {
  vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true)
  setApiBase('/c/review/api')
  let settle!: (response: Response) => void
  let fail!: (error: Error) => void
  const capabilities = new Promise<Response>((resolve, reject) => { settle = resolve; fail = reject })
  const paths: string[] = []
  vi.stubGlobal('fetch', vi.fn(async (input: string) => {
    const path = new URL(input, 'http://localhost').pathname
    paths.push(path)
    if (path.endsWith('/capabilities')) return capabilities
    return new Response('[]', { status: 200, headers: { 'Content-Type': 'application/json' } })
  }))
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  const namespace = state === 'namespaced' ? 'shop' : ''
  const kind = namespace ? 'configmaps' : 'persistentvolumes'
  const segment = namespace || (state === 'supported' ? '_' : '')
  const auditPath = `/c/review/api/audit/resource/${kind}/${segment}/pv-a`
  function Probe() {
    const audit = useResourceAudit(kind, namespace, 'pv-a')
    const discovery = useCapabilities()
    return <span>{audit.isSuccess ? 'loaded' : 'waiting'}|{discovery.isPending ? 'pending' : 'settled'}</span>
  }
  const host = document.createElement('div')
  const root = createRoot(host)
  try {
    await act(async () => { root.render(<QueryClientProvider client={client}><Probe /></QueryClientProvider>) })
    await vi.waitFor(() => {
      expect(paths).toEqual(['/c/review/api/capabilities', ...(namespace ? [auditPath] : [])])
      expect(host.textContent).toBe(namespace ? 'loaded|pending' : 'waiting|pending')
    })
    await act(async () => {
      if (state === 'failed') fail(new Error('capabilities unavailable'))
      else settle(new Response(JSON.stringify({ features: state === 'supported' || state === 'namespaced' ? { clusterScopedPlaceholder: true } : {} }), { status: 200, headers: { 'Content-Type': 'application/json' } }))
    })
    await vi.waitFor(() => {
      expect(paths).toEqual(['/c/review/api/capabilities', auditPath])
      expect(host.textContent).toBe('loaded|settled')
    })
  } finally {
    act(() => root.unmount())
    client.clear()
  }
})
