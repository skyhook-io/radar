// @vitest-environment jsdom
//
// A reviewed YAML apply shows its failure in the review itself, so the global
// "Failed to update resource" toast would say the same thing twice.
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { QueryClientProvider } from '@tanstack/react-query'
import { afterEach, expect, it, vi } from 'vitest'

const toasts = vi.hoisted(() => ({ errors: [] as string[] }))
vi.mock('@skyhook-io/k8s-ui', async (importOriginal) => ({
  ...(await importOriginal<typeof import('@skyhook-io/k8s-ui')>()),
  showApiError: (title: string) => toasts.errors.push(title),
}))

const { makeDefaultQueryClient } = await import('../RadarApp')
const { useUpdateResource } = await import('./client')

vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true)
afterEach(() => {
  vi.unstubAllGlobals()
  vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true)
  toasts.errors = []
})

async function save(reviewed: boolean) {
  vi.stubGlobal('fetch', vi.fn(async () => new Response(
    JSON.stringify({ error: 'resource changed after review; review the latest version before applying' }),
    { status: 409, headers: { 'Content-Type': 'application/json' } },
  )))
  let mutate: ReturnType<typeof useUpdateResource>['mutateAsync'] | undefined
  function Probe() {
    mutate = useUpdateResource().mutateAsync
    return null
  }
  const host = document.createElement('div')
  const root = createRoot(host)
  await act(async () => {
    root.render(<QueryClientProvider client={makeDefaultQueryClient()}><Probe /></QueryClientProvider>)
  })
  await act(async () => {
    await mutate!({
      kind: 'configmaps', namespace: 'shop', name: 'app', yaml: 'kind: ConfigMap',
      ...(reviewed ? { reviewedResourceVersion: '42', reviewedContext: 'kind-a' } : {}),
    }).catch(() => {})
  })
  act(() => root.unmount())
}

it('does not toast a reviewed apply, whose review shows the failure', async () => {
  await save(true)
  expect(toasts.errors).toEqual([])
})

it('still toasts a save that did not come from a review', async () => {
  await save(false)
  expect(toasts.errors).toEqual(['Failed to update resource'])
})
