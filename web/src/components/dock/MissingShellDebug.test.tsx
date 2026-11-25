// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { usePodDetailAccess } from '../resources/PodDebugAction'
import { observeConnectionStatus, resetConnectionBindingForTest } from '../../api/connectionBinding'
import { MissingShellDebug } from './MissingShellDebug'

const f = vi.hoisted(() => ({
  pod: {} as any, namedExec: false, embedded: false, support: 'supported', access: 'denied', image: undefined as string | undefined,
  recheck: vi.fn(), open: vi.fn(), copy: vi.fn(), probe: vi.fn(),
}))
vi.mock('../../context/NavCustomization', () => ({ useNavCustomization: () => ({ embedded: f.embedded }) }))
vi.mock('../../contexts/CapabilitiesContext', () => ({ useCapabilitiesContext: () => ({}), useNamespacedCapabilities: () => ({ podDebugImage: f.image }) }))
vi.mock('../../api/client', async original => ({ ...await original<typeof import('../../api/client')>(), useResource: () => ({ data: f.pod }) }))
vi.mock('../../api/podDebug', () => ({ usePodDebugAccess: (...args: unknown[]) => {
  f.probe(...args)
  return {support: f.support, query: { data: {state: f.access, image: 'toolbox', exec: f.namedExec}, refetch: f.recheck }}
} }))
vi.mock('@skyhook-io/k8s-ui', async original => ({ ...await original<typeof import('@skyhook-io/k8s-ui')>(), useOpenTerminal: () => f.open }))
vi.mock('@skyhook-io/k8s-ui/utils/clipboard', () => ({ copyText: f.copy }))
vi.mock('../resources/PodDebugAction', async original => ({ ...await original<typeof import('../resources/PodDebugAction')>(),
  PodDebugAction: ({ failedContainer }: { failedContainer: string }) => <button>Start for {failedContainer}</button> }))

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })
let host: HTMLDivElement, root: ReturnType<typeof createRoot>
const render = () => act(() => root.render(<MissingShellDebug namespace="payments" name="checkout" uid="uid-1" target="app" base="/api"/>))
const button = (label: string) => [...host.querySelectorAll('button')].find(b => b.textContent === label)
beforeEach(() => {
  vi.clearAllMocks()
  resetConnectionBindingForTest()
  Object.assign(f, { embedded: false, namedExec: false, support: 'supported', access: 'denied', image: undefined,
    pod: { apiVersion: 'v1', kind: 'Pod', metadata: {uid:'uid-1', namespace:'payments', name:'checkout'},
      spec: {containers:[{name:'app'}]}, status:{phase:'Running',containerStatuses:[{name:'app', state:{running:{}}}] } } })
  host = document.createElement('div')
  root = createRoot(host)
})
afterEach(() => act(() => root.unmount()))

it('offers only running debuggers targeting the failed container, with no access probe', () => {
  const names = ['already-running','second','wrong-target','stopped']
  f.pod.spec.ephemeralContainers = names.map(name => ({ name, image:'toolbox', targetContainerName:name === 'wrong-target' ? 'other' : 'app' }))
  f.pod.status.ephemeralContainerStatuses = names.map(name => ({ name, state:name === 'stopped' ? {terminated:{}} : {running:{}} }))
  render()
  const select = host.querySelector('select')!
  expect([...select.options].map(o=>o.value)).toEqual(names.slice(0,2).reverse())
  act(() => { select.value='second'; select.dispatchEvent(new Event('change',{bubbles:true})) })
  act(() => button('Open debug terminal')!.click())
  expect(f.open).toHaveBeenCalledWith(expect.objectContaining({containerName:'second',debugSession:expect.objectContaining({podUID:'uid-1'})}))
  expect(f.probe.mock.lastCall?.[3]).toBe(false)
  expect(button('Start for app')).toBeUndefined()
})

it('copies the precise access request and rechecks an updated grant', async () => {
  f.copy.mockResolvedValue(true)
  render()
  await act(async () => button('Copy access request')!.click())
  expect(button('Copied')).toBeDefined()
  expect(f.copy).toHaveBeenCalledWith('Could you grant me permission to update pods/ephemeralcontainers for Pod checkout in namespace payments? I need to add a debug container to troubleshoot app.')
  act(() => button('Recheck access')!.click())
  expect(f.recheck).toHaveBeenCalledOnce()
  f.access='allowed'; render()
  expect(button('Start for app')).toBeDefined()
})
it('rechecks unknown permission without offering a denial request', () => {
  f.access='unknown'; render()
  expect(host.textContent).toContain('couldn’t verify')
  expect(button('Copy access request')).toBeUndefined()
  expect(button('Recheck access')).toBeDefined()
})
it.each(['embedded', 'replaced', 'windows', 'stopped', 'old', 'disabled'])('suppresses the grant request on %s', mode => {
  if(mode==='embedded')f.embedded=true
  if(mode==='replaced')f.pod.metadata.uid='replacement'
  if(mode==='windows')f.pod.spec.os={name:'windows'}
  if(mode==='stopped')f.pod.status.containerStatuses[0].state={terminated:{}}
  if(mode==='old')f.support='unsupported'
  if(mode==='disabled')f.access='unavailable'
  render()
  expect(button('Copy access request')).toBeUndefined()
  expect(button('Start for app')).toBeUndefined()
})

it('uses a positive namespace grant when named access is unknown, but honors an exact denial', () => {
  f.image = 'operator-debug'; f.access = 'unknown'
  render()
  expect(button('Start for app')).toBeDefined()
  expect(f.probe.mock.lastCall?.[3]).toBe(false)
  f.access = 'denied'; render()
  expect(button('Copy access request')).toBeDefined()
})

it('updates a previously open recovery screen when the connection changes', () => {
  render()
  act(() => observeConnectionStatus({connectionId:'new-cluster'}))
  expect(host.textContent).toContain('Open the Pod on the current cluster')
  expect(button('Copy access request')).toBeUndefined()
})

it('supplies named-only exec to the Pod header and existing terminals independently of update', () => {
  function Consumer() {
    const access = usePodDetailAccess(f.pod, {canExec:false, canOpenEphemeralTerminal:false})
    return <>{access.canExec && 'Terminal '}{access.canOpenEphemeralTerminal && 'Reopen '}{access.debugImage && 'Debug'}</>
  }
  f.namedExec = true; f.access = 'denied'
  act(() => root.render(<Consumer/>))
  expect(host.textContent).toBe('Terminal Reopen ')
  f.access = 'allowed'
  act(() => root.render(<Consumer/>))
  expect(host.textContent).toBe('Terminal Reopen Debug')
  f.embedded = true
  act(() => root.render(<Consumer/>))
  expect(host.textContent).toBe('Terminal Reopen ')
  expect(f.probe.mock.lastCall?.[4]).toBe(true)
})
