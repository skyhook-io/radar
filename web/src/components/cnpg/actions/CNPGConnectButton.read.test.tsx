// @vitest-environment jsdom
import { act, type ReactNode } from 'react'
import { createRoot } from 'react-dom/client'
import { MemoryRouter } from 'react-router-dom'
import { expect, it, vi } from 'vitest'
import { CNPGConnectButton } from './CNPGConnectButton'
const state = vi.hoisted(() => ({ refetch: vi.fn(), retained: false }))
vi.mock('../useCNPGSidebarWorkspace', () => ({ useCNPGFleet: () => ({ fleet: state.retained ? { rows: [] } : null, query: { data: state.retained ? { installed: true } : undefined, error: new Error('Read failed'), refetch: state.refetch, isRefetchError: state.retained, dataUpdatedAt: Date.now() - 120_000 } }) }))
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
