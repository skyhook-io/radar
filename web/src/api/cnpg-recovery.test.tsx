// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { expect, it, vi } from 'vitest'
import { fetchJSON } from './client'
import { apiUrl } from './config'
import { useReviewedActionMutation } from './reviewedActionMutation'
import { useRecordCNPGRestoreValidation, type CNPGRestoreValidationRequest } from './cnpg-recovery'

vi.mock('./client', async (importOriginal) => ({
  ...await importOriginal<typeof import('./client')>(),
  fetchJSON: vi.fn(),
  useRadarFeature: () => ({ guard: <T,>(read: () => T) => read() }),
}))

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

it('retains an unknown validation-note result through remount without changing its HTTP body', async () => {
  const client = new QueryClient({ defaultOptions: { mutations: { retry: false } } })
  const invalidate = vi.spyOn(client, 'invalidateQueries')
  const host = document.body.appendChild(document.createElement('div'))
  const root = createRoot(host)
  const send = vi.mocked(fetchJSON).mockRejectedValue(new TypeError('Connection lost'))
  let current!: ReturnType<typeof useRecordCNPGRestoreValidation>
  let operational!: ReturnType<typeof useReviewedActionMutation<void, { request: { reviewedContext: string } }>>
  const operationalWrite = vi.fn(async () => {})
  function Dialog() {
    current = useRecordCNPGRestoreValidation('db', 'restored', 'ctx')
    operational = useReviewedActionMutation(apiUrl('/cnpg/clusters/db/restored'), 'ctx', { mutationFn: operationalWrite })
    return null
  }
  const mount = (open: boolean) => act(() => root.render(<QueryClientProvider client={client}>{open && <Dialog />}</QueryClientProvider>))
  const request: CNPGRestoreValidationRequest = { reviewedContext: 'ctx', uid: 'restored-uid', params: { checked: 'Application query passed' } }
  const attempt = () => act(async () => {
    await current.mutateAsync({ request }).catch(() => {})
    await new Promise(resolve => setTimeout(resolve, 0))
  })
  try {
    mount(true)
    await attempt()
    expect(send).toHaveBeenCalledWith('/cnpg/clusters/db/restored/restore-validation', expect.objectContaining({ body: JSON.stringify(request) }))
    expect(invalidate).toHaveBeenCalledWith({ queryKey: ['cnpg', 'recovery', 'db', 'restored'] })
    expect(invalidate).toHaveBeenCalledWith({ queryKey: ['cnpg', 'workspace'] })
    expect(client.getMutationCache().getAll()[0].options.meta?.errorMessage).toBeUndefined()
    await act(async () => { await operational.mutateAsync({ request: { reviewedContext: 'ctx' } }) })
    expect(operationalWrite).toHaveBeenCalledOnce()
    mount(false)
    client.clear()
    mount(true)
    expect(current.outcomeError?.message).toContain('Connection lost')
    await attempt()
    expect(send).toHaveBeenCalledTimes(1)
  } finally {
    act(() => root.unmount())
    client.clear()
    host.remove()
    send.mockReset()
  }
})
