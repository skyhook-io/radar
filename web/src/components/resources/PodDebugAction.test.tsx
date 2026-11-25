// @vitest-environment jsdom
import { act } from 'react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, expect, it, vi } from 'vitest'
import { DockProvider, ResourceActionsBar, useDock } from '@skyhook-io/k8s-ui'
import { observeConnectionStatus, resetConnectionBindingForTest } from '../../api/connectionBinding'
import { PodDebugAction } from './PodDebugAction'

vi.mock('../../context/ConnectionContext', () => ({ useConnection: () => ({connection:{context:'test-context'}}) }))

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true
const running = { running: {} }
const pod = {
  apiVersion: 'v1', kind: 'Pod',
  metadata: { namespace: 'demo', name: 'api', uid: 'reviewed', annotations: { 'kubectl.kubernetes.io/default-container': 'worker' } },
  spec: { containers: [{ name: 'api' }, { name: 'worker' }], initContainers: [{ name: 'mesh', restartPolicy: 'Always' }] },
  status: {
    phase: 'Running',
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
  act(() => root.render(<QueryClientProvider client={client}><DockProvider><PodDebugAction pod={pod} defaultImage="mirror.example/debug" /><Tabs /></DockProvider></QueryClientProvider>))
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
  expect(JSON.parse(String(init.body))).toEqual({ uid: 'reviewed', targetContainer: 'worker', returnImmediately: true })
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

it('opens an accepted session in the dock if its Pod details closed during the request', async () => {
  let finish!: (response: Response) => void
  vi.stubGlobal('fetch', vi.fn(() => new Promise<Response>(resolve => { finish = resolve })))
  render()
  click('Debug')
  await act(async () => { click('Start debug container') })
  act(() => root.render(<QueryClientProvider client={client}><DockProvider>{null}<Tabs /></DockProvider></QueryClientProvider>))
  await act(async () => { finish(new Response(JSON.stringify({ containerName: 'debug-name', image: 'mirror.example/debug', status: 'pending' }))) })
  expect(document.querySelector('output')!.textContent).toBe('debug-name')
})

it('keeps the header chooser open on a confirmed denial after a refused write', () => {
  const fetch = vi.fn()
  vi.stubGlobal('fetch', fetch)
  render()
  click('Debug')
  act(() => root.render(<QueryClientProvider client={client}><DockProvider>
    <PodDebugAction pod={pod} debugDenial={{ refusal: 'The image policy refused this request' }} /><Tabs />
  </DockProvider></QueryClientProvider>))
  const dialog = document.querySelector('[role="dialog"]')!
  expect(dialog.textContent).toContain('You don’t have permission to add a debug container to this Pod.')
  expect(dialog.textContent).toContain('The image policy refused this request')
  click('Start debug container')
  expect(fetch).not.toHaveBeenCalled()
  click('Cancel')
  click('Debug access denied for this Pod')
  expect(document.querySelector('[role="dialog"]')?.textContent).toContain('Copy access request')
})

it.each([
  ['allowed', 'mirror.example/debug', undefined, '', true],
  ['named denial', undefined, {}, 'You don’t have permission', false],
  ['completed undecided update with verified exec', 'mirror.example/debug', undefined, '', true],
  ['completed undecided review without verified exec', undefined, undefined, 'Access is unconfirmed', false],
  ['failed update review', undefined, undefined, 'Kubernetes couldn’t complete the check', false],
  ['Radar HTTP failure', undefined, undefined, 'The permission request failed', false],
  ['exec disabled', undefined, undefined, 'Debugging is unavailable', false],
  ['hosted', undefined, undefined, 'This view can’t add a debug container', false],
  ['changed connection', 'mirror.example/debug', undefined, 'The cluster connection changed', false],
] as const)('an open header review stays mounted through %s', (state, image, denial, message, canSubmit) => {
  const div = document.createElement('div')
  document.body.append(div)
  root = createRoot(div)
  client = new QueryClient()
  function draw(initial: boolean) {
    const unverified = !initial && state === 'completed undecided update with verified exec'
    const recheck = vi.fn().mockResolvedValue(undefined)
    act(() => root.render(<QueryClientProvider client={client}><DockProvider>
      <ResourceActionsBar resource={{ kind: 'pods', namespace: 'demo', name: 'api' }} data={pod} canExec={initial || canSubmit}
        podDebugAction={<PodDebugAction pod={pod} defaultImage={initial ? 'mirror.example/debug' : image} debugDenial={initial ? undefined : denial}
          unverifiedReview={unverified ? { refused: undefined, recheckAccess: recheck, rememberRefusal: vi.fn() } : undefined}
          reviewStatus={{ message, refusal: undefined, checking: false, recheck: state === 'hosted' ? undefined : recheck }} />} />
    </DockProvider></QueryClientProvider>))
  }
  draw(true)
  click('Debug')
  if (state === 'changed connection') observeConnectionStatus({ connectionId: 'replacement' })
  draw(false)
  const dialog = document.querySelector('[role="dialog"]')!
  expect(dialog.textContent).toContain(message)
  const start = [...dialog.querySelectorAll('button')].find(button => button.textContent === 'Start debug container')!
  expect(start.disabled).toBe(!canSubmit)
  expect(dialog.textContent?.includes('Copy access request')).toBe(state === 'named denial')
})
