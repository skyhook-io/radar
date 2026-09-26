import { QueryClient, QueryObserver } from '@tanstack/react-query'
import { describe, expect, it } from 'vitest'
import { invalidateHelmAfterSourceChange } from './client'

describe('invalidateHelmAfterSourceChange', () => {
  it('refreshes OCI inventory and an open release source status query', async () => {
    const queryClient = new QueryClient()
    const inventoryKey = ['helm-oci-sources']
    const statusKey = ['helm-source-status', 'production', 'release']
    let sourceStatus = {
      configured: true,
      available: true,
      candidates: [{ type: 'oci' as const, reference: 'oci://registry.example.com/charts/example' }],
    }
    const observer = new QueryObserver(queryClient, {
      queryKey: statusKey,
      queryFn: async () => sourceStatus,
    })
    const unsubscribe = observer.subscribe(() => undefined)

    queryClient.setQueryData(inventoryKey, ['oci://registry.example.com/charts'])
    await observer.refetch()

    sourceStatus = { configured: false, available: false, candidates: [] }
    await invalidateHelmAfterSourceChange(queryClient)

    expect(queryClient.getQueryState(inventoryKey)?.isInvalidated).toBe(true)
    expect(observer.getCurrentResult().data).toEqual({
      configured: false,
      available: false,
      candidates: [],
    })

    unsubscribe()
  })
})
