// @vitest-environment jsdom
import { act, type ReactNode } from 'react'
import { createRoot } from 'react-dom/client'
import { MemoryRouter } from 'react-router-dom'
import { expect, it, vi } from 'vitest'
vi.mock('../../../api/cnpg-ha', () => ({ useCNPGClusterHA: () => ({ data: { rwEndpoints: { state: 'ok', pods: ['orders-1'] }, pods: { state: 'ok' }, instances: [{ pod: 'orders-1', role: 'primary', ready: true }] } }) }))
vi.mock('../useCNPGKubectlContext', () => ({ useCNPGKubectlContext: () => ({ name: 'kind-test', source: 'test-config' }) }))
import { CNPGConnectButton } from './CNPGConnectButton'
const state = vi.hoisted(() => ({ refetch: vi.fn(), retained: false, row: undefined as any }))
vi.mock('../useCNPGSidebarWorkspace', () => ({ useCNPGFleet: () => ({ fleet: state.row ? { rows: [state.row] } : state.retained ? { rows: [] } : null, query: { data: state.retained ? { installed: true } : undefined, error: new Error('Read failed'), refetch: state.refetch, isRefetchError: state.retained, dataUpdatedAt: Date.now() - 120_000 } }) }))
vi.mock('@skyhook-io/k8s-ui', async (original) => ({ ...(await original<typeof import('@skyhook-io/k8s-ui')>()), DialogPortal: ({ open, children }: { open: boolean; children: ReactNode }) => open ? <div>{children}</div> : null }))
;(globalThis as any).IS_REACT_ACT_ENVIRONMENT = true
it('shows and retries a failed Cluster read in Connect', () => {
  const host = document.createElement('div'); document.body.append(host); const root = createRoot(host)
  act(() => root.render(<MemoryRouter><CNPGConnectButton namespace="db" name="pg" /></MemoryRouter>))
  act(() => [...host.querySelectorAll('button')].find((b) => b.textContent === 'Connect')!.click())
  expect(host.textContent).toContain('Cluster could not be read: Read failed')
  expect(host.textContent).not.toContain('Reading the Cluster')
  act(() => [...host.querySelectorAll('button')].find((b) => b.textContent === 'Retry')!.click())
  expect(state.refetch).toHaveBeenCalledTimes(1)
  state.retained = true
  act(() => root.render(<MemoryRouter><CNPGConnectButton namespace="db" name="pg" /></MemoryRouter>))
  expect(host.textContent).toContain('Radar cannot read this Cluster with your access')
  expect(host.textContent).toContain('Last refresh failed: Read failed')
  expect(host.textContent).toContain('2m ago')
  expect(host.textContent).not.toContain('Cluster could not be read:')
  act(() => root.unmount()); host.remove()
})

it('passes the known host context and independently read HA availability into the Connect dialog', () => {
  state.row = { namespace: 'db', name: 'orders', cluster: { metadata: { name: 'orders', namespace: 'db' }, spec: {} } }
  const host = document.createElement('div'); document.body.append(host); const root = createRoot(host)
  act(() => root.render(<MemoryRouter><CNPGConnectButton namespace="db" name="orders" /></MemoryRouter>))
  act(() => [...host.querySelectorAll('button')].find((b) => b.textContent === 'Connect')!.click())
  expect(host.textContent).toContain('kubectl --context kind-test -n db port-forward service/orders-rw')
  expect(host.textContent).toContain('Context as named in your kubeconfig (test-config); kubectl must read the same kubeconfig file.')
  expect(host.textContent).toContain('Unavailable: no ready standby')
  expect(host.textContent).toContain('Ready endpoints')
  act(() => root.unmount()); host.remove(); state.row = undefined
})
