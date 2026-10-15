// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { renderToStaticMarkup } from 'react-dom/server'
import { expect, it, vi } from 'vitest'
import { CNPGConnectSection } from './CNPGConnectSection'

const cluster = { metadata: { name: 'orders', namespace: 'radar-cnpg-prod' }, spec: { bootstrap: { initdb: { database: 'appdb', owner: 'app' } } } }
;(globalThis as any).IS_REACT_ACT_ENVIRONMENT = true

it('pairs a namespace-qualified port-forward with matching loopback templates and copies each', async () => {
  const writeText = vi.fn().mockResolvedValue(undefined)
  Object.defineProperty(navigator, 'clipboard', { value: { writeText }, configurable: true })
  const host = document.createElement('div'); const root = createRoot(host)
  act(() => root.render(<CNPGConnectSection cluster={cluster} />))
  expect(host.textContent).toContain('Inside Kubernetes')
  expect(host.textContent).toContain('From this computer')
  for (const [label, expected] of [
    ['port-forward command', 'kubectl -n radar-cnpg-prod port-forward service/orders-rw 5432:5432'],
    ['local psql command', 'psql -h 127.0.0.1 -p 5432 -U app -d appdb'],
    ['local connection string', 'postgresql://app:<password>@127.0.0.1:5432/appdb'],
  ]) {
    await act(async () => host.querySelector<HTMLButtonElement>(`[aria-label="Copy ${label}"]`)!.click())
    expect(writeText).toHaveBeenLastCalledWith(expected)
  }
  act(() => root.unmount())
})

it('uses one three-column grid with every explanation under its own host', () => {
  const html = renderToStaticMarkup(<CNPGConnectSection cluster={cluster} poolers={[{ metadata: { name: 'orders-pooler', namespace: 'radar-cnpg-prod' }, spec: { cluster: { name: 'orders' } } }]} onOpenReachability={() => {}} />)
  const host = document.createElement('div'); host.innerHTML = html
  const list = host.querySelector('ul')!
  expect(list.className).toContain('grid-cols-[5rem_minmax(0,1fr)_auto]')
  expect(list.querySelectorAll('li')).toHaveLength(4)
  for (const row of list.querySelectorAll('li')) {
    expect(row.className).toBe('contents')
    expect(row.children).toHaveLength(3)
    expect(row.children[1].className).toContain('[overflow-wrap:anywhere]')
    expect(row.children[1].querySelector('div')).not.toBeNull()
    expect(row.children[2].textContent).toContain('Reachability')
  }
})

it('shows command context certainty and per-service availability beside each endpoint', () => {
  const host = document.createElement('div')
  host.innerHTML = renderToStaticMarkup(<CNPGConnectSection cluster={cluster} kubeconfigContext="kind-orders" kubeconfigSource="orders-config" ha={{ rwEndpoints: { state: 'ok', pods: ['orders-1'] }, pods: { state: 'ok' }, instances: [{ pod: 'orders-1', role: 'primary', ready: true }] } as any} />)
  expect(host.textContent).toContain('kubectl --context kind-orders')
  expect(host.textContent).not.toContain('uses your current kubectl context')
  expect(host.textContent).toContain('Context as named in your kubeconfig (orders-config); kubectl must read the same kubeconfig file.')
  expect([...host.querySelectorAll('li')].map((li) => li.textContent)).toEqual([expect.stringContaining('Ready endpoints'), expect.stringContaining('Unavailable: no ready standby'), expect.stringContaining('Ready instance observed')])
  host.innerHTML = renderToStaticMarkup(<CNPGConnectSection cluster={cluster} />)
  expect(host.textContent).toContain('uses your current kubectl context')
  expect(host.textContent).not.toContain('--context')
  expect([...host.querySelectorAll('li')].every((li) => li.textContent?.includes('Not checked'))).toBe(true)
})

it('keeps the unavailable reason beside each unchecked Service', () => {
  const host = document.createElement('div')
  host.innerHTML = renderToStaticMarkup(<CNPGConnectSection cluster={cluster} haUnavailableReason="Availability could not be read: timeout" />)
  for (const li of host.querySelectorAll('li')) expect(li.textContent).toContain('Not checked · Availability could not be read: timeout')
  host.innerHTML = renderToStaticMarkup(<CNPGConnectSection cluster={cluster} ha={{ rwEndpoints: { state: 'denied', grant: { verb: 'list', resource: 'endpointslices', group: 'discovery.k8s.io', namespace: 'radar-cnpg-prod' } }, pods: { state: 'denied', grant: { verb: 'list', resource: 'pods', namespace: 'radar-cnpg-prod' } }, instances: [] } as any} />)
  expect(host.querySelector('li')!.textContent).toContain('needs list endpointslices (discovery.k8s.io) in namespace radar-cnpg-prod')
  expect(host.querySelectorAll('li')[1].textContent).toContain('needs list pods in namespace radar-cnpg-prod')
})
