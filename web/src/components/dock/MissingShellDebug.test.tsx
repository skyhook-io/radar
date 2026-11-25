// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { usePodDetailAccess } from '../resources/PodDebugAction'
import { observeConnectionStatus, resetConnectionBindingForTest } from '../../api/connectionBinding'
import { MissingShellDebug } from './MissingShellDebug'

const f = vi.hoisted(() => ({
  pod: {} as any, installation: {} as Record<string, unknown>, checkError: undefined as Error | undefined, namedExec: false, embedded: false, support: 'supported', access: 'denied', image: undefined as string | undefined,
  recheck: vi.fn(), open: vi.fn(), copy: vi.fn(), probe: vi.fn(),
}))
vi.mock('../../context/ConnectionContext', () => ({ useConnection: () => ({connection:{context:'fixture-cluster'}}) }))
vi.mock('../../context/NavCustomization', () => ({ useNavCustomization: () => ({ embedded: f.embedded }) }))
vi.mock('../../contexts/CapabilitiesContext', () => ({ useCapabilitiesContext: () => f.installation, useNamespacedCapabilities: () => ({ podDebugImage: f.image }) }))
vi.mock('../../api/client', async original => ({ ...await original<typeof import('../../api/client')>(), useResource: () => ({ data: f.pod }) }))
vi.mock('../../api/podDebug', () => ({ usePodDebugAccess: (...args: unknown[]) => {
  f.probe(...args)
  return {support: f.support, recheckAccess: f.recheck, unverifiedReview: !args[4] && f.access === 'unknown' && f.namedExec && !f.checkError ? {} : undefined, unverifiedImage: !args[4] && f.access === 'unknown' && f.namedExec && !f.checkError ? 'operator-tools' : undefined, query: { error: f.checkError, data: {state: f.access, image: 'toolbox', exec: f.namedExec}, refetch: f.recheck }}
} }))
vi.mock('@skyhook-io/k8s-ui', async original => ({ ...await original<typeof import('@skyhook-io/k8s-ui')>(), useOpenTerminal: () => f.open }))
vi.mock('@skyhook-io/k8s-ui/utils/clipboard', () => ({ copyText: f.copy }))
vi.mock('../resources/PodDebugAction', async original => ({ ...await original<typeof import('../resources/PodDebugAction')>(),
  PodDebugAction: ({ failedContainer, unverifiedReview, hideLauncher }: { failedContainer: string; unverifiedReview?: object; hideLauncher?: boolean }) => hideLauncher ? null : <button>{unverifiedReview ? 'Review attempt for' : 'Start for'} {failedContainer}</button> }))

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })
let host: HTMLDivElement, root: ReturnType<typeof createRoot>
const render = () => act(() => root.render(<MissingShellDebug namespace="payments" name="checkout" uid="uid-1" target="app" base="/api"/>))
const button = (label: string) => [...host.querySelectorAll('button')].find(b => b.textContent === label)
beforeEach(() => {
  vi.clearAllMocks()
  resetConnectionBindingForTest()
  Object.assign(f, { installation: {}, checkError: undefined, embedded: false, namedExec: false, support: 'supported', access: 'denied', image: undefined,
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
  expect(f.copy).toHaveBeenCalledWith('Could you grant me permission to update pods/ephemeralcontainers for Pod checkout in namespace payments? I need to troubleshoot container app. Please limit the rule to resourceNames: ["checkout"]. A replacement Pod with another name would need its own grant. Radar context: fixture-cluster.')
  act(() => button('Recheck access')!.click())
  expect(f.recheck).toHaveBeenCalledOnce()
  f.access='allowed'; render()
  expect(button('Start for app')).toBeDefined()
})
it('asks for verification on an unknown result without requesting a grant', async () => {
  f.access='unknown'; render()
  expect(host.textContent).toContain('cluster access check returned no decision')
  expect(button('Copy access request')).toBeUndefined()
  expect(button('Recheck access')).toBeDefined()
  await act(async () => button('Copy verification request')!.click())
  expect(f.copy).toHaveBeenCalledWith('Could you check whether my Kubernetes identity can update pods/ephemeralcontainers for Pod checkout in namespace payments? Radar couldn’t verify access while I was troubleshooting container app. Radar context: fixture-cluster.')
})
it('distinguishes a failed request from an inconclusive cluster answer', () => {
  f.access = 'unknown'
  render()
  expect(host.textContent).toContain('cluster access check returned no decision')
  f.checkError = new Error('request timed out')
  render()
  expect(host.textContent).toContain('permission request failed')
  expect(button('Copy access request')).toBeUndefined()
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

it('identifies where to find the real ServiceAccount when Radar has no signed-in user', async () => {
  f.installation = { deployment: { mode: 'in-cluster' } }
  render()
  await act(async () => button('Copy access request')!.click())
  expect(host.textContent).toContain('Radar’s ServiceAccount doesn’t have permission')
  const [request] = f.copy.mock.lastCall!
  expect(request).toContain('the ServiceAccount used by this Radar installation')
  expect(request).toContain('Radar context: fixture-cluster.')
  expect(request).toContain('running Radar Pod’s namespace and its spec.serviceAccountName')
})

it('explains how to start a debugger outside a hosted session without issuing a named check', () => {
  f.embedded = true
  render()
  expect(host.textContent).toContain('standalone Radar')
  expect(f.probe.mock.lastCall?.[3]).toBe(false)
})

it.each(['unknown', 'unavailable'])('rechecks hosted exec access to a running debugger when %s', state => {
  f.embedded = true
  f.access = state
  f.pod.spec.ephemeralContainers = [{ name: 'debugger', image: 'tools', targetContainerName: 'app' }]
  f.pod.status.ephemeralContainerStatuses = [{ name: 'debugger', state: { running: {} } }]
  render()
  expect(f.probe.mock.lastCall?.slice(3, 5)).toEqual([true, true])
  expect(button('Copy access request')).toBeUndefined()
  act(() => button('Recheck access')!.click())
  expect(f.recheck).toHaveBeenCalledOnce()
  f.namedExec = true
  render()
  expect(button('Open debug terminal')).toBeDefined()
})

it('names other running containers without suggesting a shell or listing stopped siblings', () => {
  f.pod.spec.containers.push({ name: 'tools' }, { name: 'stopped' })
  f.pod.status.containerStatuses.push({ name: 'tools', state: { running: {} } }, { name: 'stopped', state: { terminated: {} } })
  render()
  expect(host.textContent).toContain('Other running container: tools')
  expect(host.textContent).not.toContain('stopped')
})

it('offers confirmation only while the named response verifies exec, on a writable Radar', () => {
  f.namedExec = true
  f.access = 'unknown'
  render()
  expect(button('Review attempt for app')).toBeDefined()
  f.checkError = new Error('transport failure')
  render()
  expect(button('Review attempt for app')).toBeUndefined()
  f.checkError = undefined
  f.access = 'denied'
  render()
  expect(button('Review attempt for app')).toBeUndefined()
  f.access = 'unknown'
  f.embedded = true
  render()
  expect(button('Review attempt for app')).toBeUndefined()
})
it('shows no alternative when every sibling has stopped', () => {
  f.pod.spec.containers.push({ name: 'stopped' })
  f.pod.status.containerStatuses.push({ name: 'stopped', state: { terminated: {} } })
  render()
  expect(host.textContent).not.toContain('Other running')
})

it('identifies an untargeted running debugger without offering it as the failed container’s debugger', () => {
  f.pod.spec.ephemeralContainers = [{name:'network-tools', image:'tools'}]
  f.pod.status.ephemeralContainerStatuses = [{name:'network-tools', state:{ running:{} }}]
  render()
  expect(host.textContent).toContain('Running without a target: network-tools')
  expect(button('Open debug terminal')).toBeUndefined()
})
it('explains how to find the local effective Kubernetes username before requesting a grant', async () => {
  f.installation = {deployment:{mode:'local'}}
  render()
  await act(async () => button('Copy access request')!.click())
  expect(f.copy.mock.lastCall?.[0]).toContain('kubectl auth whoami in this context')
})

it('keeps verified named exec when the separate update review failed, with no write preview', async () => {
  f.access = 'check_failed'
  f.namedExec = true
  render()
  expect(host.textContent).toContain('Kubernetes couldn’t complete the check')
  expect(button('Copy verification request')).toBeDefined()
  expect(button('Review debug container')).toBeUndefined()
  expect(button('Copy access request')).toBeUndefined()
  function Details() {
    const access = usePodDetailAccess(f.pod, { canExec: false, canOpenEphemeralTerminal: false })
    return <output>{String(access.canExec)} {access.debugImage ?? 'no preview'}</output>
  }
  act(() => root.render(<Details/>))
  expect(host.textContent).toBe('true no preview')
})
