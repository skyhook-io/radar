// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { renderToStaticMarkup } from 'react-dom/server'
import { expect, it, vi } from 'vitest'
import { CNPGScreenGate, CoverageNotice } from './shared'
import { buildCNPGFleet, CNPG_WORKSPACE_KEYS, type CNPGWorkspaceResponse } from '@skyhook-io/k8s-ui'
vi.mock('../../context/ConnectionContext', () => ({ useConnection: () => ({ connection: { context: 'test' } }) }))
Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })
it('retries an initial failure, disables duplicate retries and shows recovered data', async () => {
  const refetch = vi.fn(async () => ({}))
  const query = { isLoading: false, isFetching: false, error: new Error('Connection timed out'), refetch } as any
  const host = document.createElement('div')
  document.body.appendChild(host)
  const root = createRoot(host)
  const render = () => root.render(<CNPGScreenGate query={query} fleet={{} as any}>{() => <span>recovered workspace</span>}</CNPGScreenGate>)
  await act(async () => render())
  expect(host.textContent).toContain('Connection timed out')
  await act(async () => host.querySelector('button')!.click())
  expect(refetch).toHaveBeenCalledOnce()
  query.isFetching = true
  await act(async () => render())
  expect(host.querySelector('button')!.disabled).toBe(true)
  expect(host.textContent).toContain('Retrying…')
  query.data = { installed: true }
  query.isFetching = false
  await act(async () => render())
  expect(host.textContent).toContain('recovered workspace')
  expect(host.textContent).not.toContain('CloudNativePG data unavailable')
  act(() => root.unmount())
  host.remove()
})
it('keeps workspace data with its failed-refresh reason and age', () => {
  const query = { data: { installed: true }, isRefetchError: true, error: new Error('Workspace timeout'), dataUpdatedAt: Date.now() - 120_000 } as any
  const html = renderToStaticMarkup(<CNPGScreenGate query={query} fleet={{} as any}>{() => <span>retained Cluster</span>}</CNPGScreenGate>)
  expect(html).toContain('retained Cluster')
  expect(html).toContain('Last refresh failed: Workspace timeout')
  expect(html).toContain('2m ago')
})

it('scopes unread kinds to the screen and its namespace, with details folded', () => {
  const data: CNPGWorkspaceResponse = { installed: true, context: 'test', namespaces: null, objects: {}, coverage: { ...Object.fromEntries(CNPG_WORKSPACE_KEYS.map((k) => [k, { state: 'full' as const }])), backups: { state: 'partial', allowedNamespaces: ['prod'], deniedNamespaces: ['staging'] }, clusterImageCatalogs: { state: 'uncached' } }, issues: [], audit: [], backupsOmitted: 0, jobCoverage: { state: 'denied' } }
  const fleet = buildCNPGFleet(data)
  const html = renderToStaticMarkup(<CoverageNotice data={data} fleet={fleet} kinds={['backups', 'scheduledBackups', 'objectStores']} />)
  expect(html).toContain('Some data used on this screen was not read.')
  expect(html).toContain('Backup (no access in staging)')
  expect(html).toContain('aria-expanded="false"')
  expect(html).not.toContain('ClusterImageCatalog')
  expect(html).not.toContain('Job (')
  expect(renderToStaticMarkup(<CoverageNotice data={data} fleet={fleet} kinds={['backups']} namespace="prod" />)).toBe('')
})
