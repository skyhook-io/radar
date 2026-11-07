// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { expect, it, vi } from 'vitest'
import { useRefreshRevisionsOnNewRollout } from './client'

vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true)

async function observe(revisions: (string | undefined)[]) {
  const client = new QueryClient()
  const invalidate = vi.spyOn(client, 'invalidateQueries')
  function Probe({ revision }: { revision?: string }) {
    useRefreshRevisionsOnNewRollout('deployments', 'prod', 'web', revision)
    return null
  }
  const root = createRoot(document.createElement('div'))
  for (const revision of revisions) {
    await act(async () => {
      root.render(<QueryClientProvider client={client}><Probe revision={revision} /></QueryClientProvider>)
    })
  }
  act(() => root.unmount())
  client.clear()
  return invalidate
}

it('refreshes the revision list when the Deployment records a new revision', async () => {
  const invalidate = await observe(['1', '2'])

  expect(invalidate).toHaveBeenCalledExactlyOnceWith({ queryKey: ['workload-revisions', 'deployments', 'prod', 'web'] })
})

it('refreshes when a Deployment created paused records its first revision', async () => {
  expect(await observe(['', '2'])).toHaveBeenCalledTimes(1)
})

it('leaves the list alone while the revision stays the same', async () => {
  expect(await observe(['1', '1', '1'])).not.toHaveBeenCalled()
})

it('leaves the list alone when the resource first loads', async () => {
  expect(await observe([undefined, '2'])).not.toHaveBeenCalled()
})
