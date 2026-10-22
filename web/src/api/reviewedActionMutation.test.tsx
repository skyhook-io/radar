// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { describe, expect, it, vi } from 'vitest'
import { useReviewedActionMutation } from './reviewedActionMutation'
import { ApiError } from './client'
import { actionErrorCode, actionCompleted } from './actions'
import { RadarFeatureUnsupportedError } from './radarFeatures'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

describe('reviewed action lifetime', () => {
  it.each([
    new TypeError('Connection lost'),
    new ApiError('One write completed', 409, { code: 'partial', completed: ['Pod removed'] }),
    new ApiError('upstream connect error or disconnect/reset before headers', 503),
  ])('retains an ambiguous result through remount and prevents another request: %s', async (failure) => {
    const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } })
    const host = document.body.appendChild(document.createElement('div'))
    const root = createRoot(host)
    const request = vi.fn(async () => { throw failure })
    let current: ReturnType<typeof useReviewedActionMutation<null, { request: { reviewedContext: string } }>>
    function Dialog({ target }: { target: string }) {
      current = useReviewedActionMutation<null, { request: { reviewedContext: string } }>(target, 'ctx', { mutationFn: request })
      return <div>{current.outcomeError?.message}</div>
    }
    const mount = (target?: string) => act(() => root.render(<QueryClientProvider client={client}>{target && <Dialog target={target} />}</QueryClientProvider>))
    const attempt = () => act(async () => {
      await current.mutateAsync({ request: { reviewedContext: 'ctx' } }).catch(() => {})
      await new Promise(resolve => setTimeout(resolve, 0))
    })
    try {
      mount('/api/cnpg/clusters/jobs/orders')
      await attempt()
      mount()
      client.removeQueries()
      client.clear()
      mount('/api/cnpg/clusters/jobs/orders')
      expect(current!.outcomeError?.message).toContain('reload this page')
      expect(actionErrorCode(current!.outcomeError)).toBe(actionErrorCode(failure))
      expect(actionCompleted(current!.outcomeError)).toEqual(actionCompleted(failure))
      await attempt()
      expect(request).toHaveBeenCalledTimes(1)
      mount('/other-cluster/api/cnpg/clusters/jobs/orders')
      expect(current!.outcomeError).toBeNull()
      mount('/api/cnpg/scheduledbackups/jobs/orders')
      expect(current!.outcomeError).toBeNull()
    } finally {
      act(() => root.unmount())
      client.clear()
      host.remove()
    }
  })
  it.each([
    { nextTarget: '/api/target', nextContext: 'other-context' },
    { nextTarget: '/other-cluster/api/target', nextContext: 'ctx' },
  ])('keeps a late result on the original cluster binding: %s', async ({ nextTarget, nextContext }) => {
    const client = new QueryClient()
    const host = document.body.appendChild(document.createElement('div'))
    const root = createRoot(host)
    let fail!: (error: Error) => void
    let current: ReturnType<typeof useReviewedActionMutation<null, { request: { reviewedContext: string } }>>
    function Dialog({ target, context }: { target: string; context: string }) {
      current = useReviewedActionMutation<null, { request: { reviewedContext: string } }>(target, context, {
        mutationFn: () => new Promise<null>((_, reject) => { fail = reject }),
      })
      return null
    }
    const mount = (target: string, context: string) => act(() => root.render(<QueryClientProvider client={client}><Dialog target={target} context={context} /></QueryClientProvider>))
    try {
      mount('/api/target', 'ctx')
      let attempted!: Promise<unknown>
      await act(async () => {
        attempted = current.mutateAsync({ request: { reviewedContext: 'ctx' } }).catch(() => {})
        await Promise.resolve()
      })
      mount(nextTarget, nextContext)
      await act(async () => {
        fail(new TypeError('Connection lost'))
        await attempted
        await new Promise(resolve => setTimeout(resolve, 0))
      })
      expect(current!.outcomeError).toBeNull()
      mount('/api/target', 'ctx')
      expect(current!.outcomeError?.message).toContain('Connection lost')
    } finally {
      act(() => root.unmount())
      client.clear()
      host.remove()
    }
  })
  it.each([
    new ApiError('Facts changed', 409, { code: 'changed' }),
    new ApiError('The operator webhook did not answer', 503, { code: 'operator_webhook_unavailable' }),
    new RadarFeatureUnsupportedError('cnpgWorkspace', {}),
  ])('keeps a known refusal retryable after remount: %s', async (failure) => {
    const client = new QueryClient()
    const host = document.body.appendChild(document.createElement('div'))
    const root = createRoot(host)
    let current: ReturnType<typeof useReviewedActionMutation<null, { request: { reviewedContext: string } }>>
    const request = vi.fn(async () => { throw failure })
    function Dialog() {
      current = useReviewedActionMutation<null, { request: { reviewedContext: string } }>('/api/target', 'ctx', { mutationFn: request })
      return null
    }
    const render = (open: boolean) => act(() => root.render(<QueryClientProvider client={client}>{open && <Dialog />}</QueryClientProvider>))
    try {
      render(true)
      await act(async () => { await current.mutateAsync({ request: { reviewedContext: 'ctx' } }).catch(() => {}) })
      render(false)
      render(true)
      expect(current!.outcomeError).toBeNull()
      await act(async () => { await current.mutateAsync({ request: { reviewedContext: 'ctx' } }).catch(() => {}) })
      expect(request).toHaveBeenCalledTimes(2)
    } finally {
      act(() => root.unmount())
      client.clear()
      host.remove()
    }
  })

  it('retains an in-flight request through remount and cache clearing without sending twice', async () => {
    const client = new QueryClient()
    const host = document.body.appendChild(document.createElement('div'))
    const root = createRoot(host)
    let complete!: (data: null) => void
    const request = vi.fn(() => new Promise<null>(resolve => { complete = resolve }))
    let current: ReturnType<typeof useReviewedActionMutation<null, { request: { reviewedContext: string } }>>
    function Dialog() {
      current = useReviewedActionMutation<null, { request: { reviewedContext: string } }>('/api/target', 'ctx', { mutationFn: request })
      return null
    }
    const render = (open: boolean) => act(() => root.render(<QueryClientProvider client={client}>{open && <Dialog />}</QueryClientProvider>))
    try {
      render(true)
      let attempted!: Promise<null>
      await act(async () => {
        attempted = current.mutateAsync({ request: { reviewedContext: 'ctx' } })
        await Promise.resolve()
      })
      render(false)
      client.removeQueries()
      client.clear()
      render(true)
      expect(current!.isPending).toBe(true)
      await act(async () => {
        await expect(current.mutateAsync({ request: { reviewedContext: 'ctx' } })).rejects.toThrow('still in progress')
      })
      expect(request).toHaveBeenCalledTimes(1)
      await act(async () => {
        complete(null)
        await attempted
        await new Promise(resolve => setTimeout(resolve, 0))
      })
      expect(current!.isPending).toBe(false)
      expect(current!.outcomeError).toBeNull()
    } finally {
      act(() => root.unmount())
      client.clear()
      host.remove()
    }
  })
})
