// @vitest-environment jsdom
import { renderToString } from 'react-dom/server'
import { MemoryRouter } from 'react-router-dom'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { DockProvider, ToastProvider } from '@skyhook-io/k8s-ui'
import { expect, it, vi } from 'vitest'
import * as api from '../../api/client'
import { WorkloadView, WorkloadViewRoute } from './WorkloadView'

it.each([
  ['full-page route', undefined, true],
  ['active drawer layer', true, true],
  ['prewarm or outgoing drawer layer', false, false],
] as const)('sets ownership polling for the %s', (_, active, expected) => {
  const client = new QueryClient()
  const hook = vi.spyOn(api, 'useResourceWithRelationships')
  try {
    renderToString(
      <QueryClientProvider client={client}>
        <ToastProvider><DockProvider>
          <MemoryRouter initialEntries={['/workload/configmaps/shop/settings']}>
            {active === undefined
              ? <WorkloadViewRoute />
              : <WorkloadView kind="configmaps" namespace="shop" name="settings" group="" onBack={() => {}} active={active} />}
          </MemoryRouter>
        </DockProvider></ToastProvider>
      </QueryClientProvider>,
    )
    expect(hook).toHaveBeenCalledWith('configmaps', 'shop', 'settings', '', { pollPendingOwnership: expected })
  } finally {
    hook.mockRestore()
    client.clear()
  }
})
