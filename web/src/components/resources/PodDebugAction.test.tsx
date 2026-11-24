// @vitest-environment jsdom
import { act } from 'react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, expect, it, vi } from 'vitest'
import { DockProvider, useDock } from '@skyhook-io/k8s-ui'
import { observeConnectionStatus, resetConnectionBindingForTest } from '../../api/connectionBinding'
import { PodDebugAction } from './PodDebugAction'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true
const running = { running: {} }
const pod = {
  metadata: { namespace: 'demo', name: 'api', uid: 'reviewed', annotations: { 'kubectl.kubernetes.io/default-container': 'worker' } },
  spec: { containers: [{ name: 'api' }, { name: 'worker' }], initContainers: [{ name: 'mesh', restartPolicy: 'Always' }] },
  status: {
    containerStatuses: [{ name: 'api', state: { waiting: {} } }, { name: 'worker', state: running }],
    initContainerStatuses: [{ name: 'mesh', state: running }],
  },
}
function Tabs() {
  const { tabs } = useDock()
  return <output>{tabs.map(t => t.containerName).join(',')}</output>
}
let root: Root
let client: QueryClient
function render(existing = new QueryClient()) {
  client = existing
  const host = document.createElement('div')
  document.body.append(host)
  root = createRoot(host)
  act(() => root.render(<QueryClientProvider client={client}><DockProvider><PodDebugAction pod={pod} /><Tabs /></DockProvider></QueryClientProvider>))
}
afterEach(() => {
  act(() => root.unmount())
  document.body.replaceChildren()
  resetConnectionBindingForTest()
  vi.unstubAllGlobals()
})
function click(label: string) {
  const button = [...document.querySelectorAll('button')].find(b => b.textContent?.trim() === label)
  expect(button).toBeDefined()
  act(() => button!.click())
}

it.each([false, true])('submits the reviewed running target once, keeping the default image on the server (switch=%s)', async switched => {
  let finish!: (r: Response) => void
  const fetch = vi.fn(() => new Promise<Response>(resolve => { finish = resolve }))
  vi.stubGlobal('fetch', fetch)
  observeConnectionStatus({ connectionId: 'reviewed-connection' })
  render()
  click('Debug')
  const select = document.querySelector('select')!
  expect(Array.from(select.options, o => o.value)).toEqual(['worker', 'mesh'])
  expect(select.value).toBe('worker')
  click('Start debug container')
  click('Start debug container')
  await act(async () => { await Promise.resolve() })
  expect(fetch).toHaveBeenCalledTimes(1)
  const [url, init] = fetch.mock.calls[0] as unknown as [string, RequestInit]
  expect(url).toBe('/api/pods/demo/api/debug')
  expect(new Headers(init.headers).get('X-Radar-Connection')).toBe('reviewed-connection')
  expect(JSON.parse(String(init.body))).toEqual({ uid: 'reviewed', targetContainer: 'worker' })
  if (switched) observeConnectionStatus({ connectionId: 'next-cluster' })
  await act(async () => { finish(new Response(JSON.stringify({ containerName: 'debug-1', status: 'pending' }))) })
  expect(document.querySelector('output')!.textContent).toBe(switched ? '' : 'debug-1')
})

it.each([
  [503, { error: 'No serving connection', error_code: 'cluster_unavailable' }, false],
  [502, { error: 'Proxy lost the response' }, true],
] as const)('distinguishes a retryable refusal from an uncertain write (%s)', async (status, body, unknown) => {
  vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(JSON.stringify(body), { status })))
  render()
  click('Debug')
  await act(async () => { click('Start debug container') })
  const start = [...document.querySelectorAll('button')].find(button => button.textContent === 'Start debug container')!
  expect(start.disabled).toBe(unknown)
  if (unknown) {
    act(() => root.unmount())
    render(client)
    click('Debug')
    const retry = [...document.querySelectorAll('button')].find(button => button.textContent === 'Start debug container')!
    expect(retry.disabled).toBe(true)
    click('I checked the Pod YAML')
    expect(retry.disabled).toBe(false)
  }
})
