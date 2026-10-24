// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { afterEach, expect, it, vi } from 'vitest'
import { HelmSourceSelectionError, useHelmPreviewValues } from './client'

vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true)
afterEach(() => {
  vi.unstubAllGlobals()
  vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true)
})

it.each([
  { code: undefined, sourceError: false },
  { code: 'release_changing', sourceError: false },
  { code: 'chart_source_changed', sourceError: true },
  { code: 'chart_source_required', sourceError: true },
])('classifies a preview conflict by error code: $code', async ({ code, sourceError }) => {
  vi.stubGlobal('fetch', vi.fn(async () => new Response(
    JSON.stringify({ error: 'preview conflict', errorCode: code }),
    { status: 409, headers: { 'Content-Type': 'application/json' } },
  )))
  let mutate: ReturnType<typeof useHelmPreviewValues>['mutateAsync'] | undefined
  function Probe() {
    mutate = useHelmPreviewValues().mutateAsync
    return null
  }
  const client = new QueryClient({ defaultOptions: { mutations: { retry: false } } })
  const root = createRoot(document.createElement('div'))
  try {
    await act(async () => root.render(<QueryClientProvider client={client}><Probe /></QueryClientProvider>))
    let failure: unknown
    await act(async () => {
      if (!mutate) throw new Error('Preview hook did not mount')
      try {
        await mutate({ namespace: 'default', name: 'demo', values: {} })
      } catch (error) {
        failure = error
      }
    })
    expect(failure).toBeInstanceOf(Error)
    expect(failure instanceof HelmSourceSelectionError).toBe(sourceError)
  } finally {
    await act(async () => root.unmount())
    client.clear()
  }
})
