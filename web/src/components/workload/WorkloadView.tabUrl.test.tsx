// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { createMemoryRouter, RouterProvider } from 'react-router-dom'
import { onlineManager, QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { DockProvider, ToastProvider } from '@skyhook-io/k8s-ui'
import { expect, it } from 'vitest'
import { WorkloadViewRoute } from './WorkloadView'

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })

it('replaces a stale Deploys URL with Overview and preserves the rest of the location', async () => {
  const path = '/workload/configmaps/shop/settings'
  const client = new QueryClient()
  client.setQueryData(['resource', 'configmaps', 'shop', 'settings', ''], {
    resource: { apiVersion: 'v1', kind: 'ConfigMap', metadata: { namespace: 'shop', name: 'settings' } },
  })
  const router = createMemoryRouter([{ path: '*', element: <WorkloadViewRoute /> }], {
    initialEntries: [{ pathname: path, search: '?tab=deploys&namespaces=shop', state: { from: 'apps' } }],
  })
  const host = document.createElement('div')
  const root = createRoot(host)
  const wasOnline = onlineManager.isOnline()
  onlineManager.setOnline(false)
  try {
    await act(async () => root.render(
      <QueryClientProvider client={client}>
        <ToastProvider><DockProvider><RouterProvider router={router} /></DockProvider></ToastProvider>
      </QueryClientProvider>,
    ))
    expect(host.querySelector('[role="tab"][aria-selected="true"]')?.textContent).toBe('Overview')
    expect(router.state.location).toMatchObject({ pathname: path, search: '?namespaces=shop', state: { from: 'apps' } })
    expect(router.state.historyAction).toBe('REPLACE')
  } finally {
    act(() => root.unmount())
    router.dispose()
    client.clear()
    onlineManager.setOnline(wasOnline)
  }
})
