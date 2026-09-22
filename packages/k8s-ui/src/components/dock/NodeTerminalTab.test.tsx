// @vitest-environment jsdom
import { act, StrictMode } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { NodeTerminalTab, type NodeDebugPod } from './NodeTerminalTab'

vi.mock('./TerminalTab', () => ({ TerminalTab: () => <div>terminal</div> }))
Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })

const roots = new Set<Root>()
const pod = (uid: string): NodeDebugPod => ({ namespace: 'default', podName: `debug-${uid}`, uid, containerName: 'debug' })
function deferred<T>() {
  let resolve!: (value: T) => void
  const promise = new Promise<T>(done => { resolve = done })
  return { promise, resolve }
}
async function mount(create: () => Promise<NodeDebugPod>, cleanup = vi.fn(async (_nodeName: string, _pod: NodeDebugPod) => {}), strict = false) {
  const element = document.createElement('div')
  document.body.appendChild(element)
  const root = createRoot(element)
  roots.add(root)
  const component = <NodeTerminalTab nodeName="same-node" createNodeDebugPod={create} cleanupNodeDebugPod={cleanup} createSession={async () => ({ wsUrl: '' })} />
  await act(async () => { root.render(strict ? <StrictMode>{component}</StrictMode> : component) })
  return { root, element, cleanup }
}
async function unmount(root: Root) {
  await act(async () => root.unmount())
  roots.delete(root)
}
afterEach(async () => {
  for (const root of roots) await unmount(root)
  document.body.replaceChildren()
})

describe('node terminal pod ownership', () => {
  it('closing one terminal cleans only its pod on a shared node', async () => {
    const cleanup = vi.fn(async (_nodeName: string, _pod: NodeDebugPod) => {})
    const first = await mount(async () => pod('a'), cleanup)
    const second = await mount(async () => pod('b'), cleanup)
    await unmount(first.root)
    expect(cleanup.mock.calls).toEqual([['same-node', pod('a')]])
    expect(second.element.textContent).toBe('terminal')
    await unmount(second.root)
    expect(cleanup.mock.calls).toEqual([['same-node', pod('a')], ['same-node', pod('b')]])
  })

  it('cleans a late creation exactly once after unload and unmount', async () => {
    const pending = deferred<NodeDebugPod>()
    const terminal = await mount(() => pending.promise)
    await act(async () => { window.dispatchEvent(new Event('beforeunload')) })
    await unmount(terminal.root)
    expect(terminal.cleanup).not.toHaveBeenCalled()
    await act(async () => { pending.resolve(pod('late')) })
    expect(terminal.cleanup.mock.calls).toEqual([['same-node', pod('late')]])
  })

  it('does not duplicate cleanup when unload precedes unmount', async () => {
    const terminal = await mount(async () => pod('ready'))
    await act(async () => { window.dispatchEvent(new Event('beforeunload')) })
    await unmount(terminal.root)
    expect(terminal.cleanup.mock.calls).toEqual([['same-node', pod('ready')]])
  })

  it('gives a retry its own cleanup even when it resolves after unmount', async () => {
    const pending = deferred<NodeDebugPod>()
    const create = vi.fn<() => Promise<NodeDebugPod>>()
      .mockRejectedValueOnce(new Error('creation failed'))
      .mockReturnValueOnce(pending.promise)
    const terminal = await mount(create)
    await act(async () => { terminal.element.querySelector('button')!.click() })
    expect(create).toHaveBeenCalledTimes(2)
    await unmount(terminal.root)
    await act(async () => { pending.resolve(pod('retry')) })
    expect(terminal.cleanup.mock.calls).toEqual([['same-node', pod('retry')]])
  })

  it('isolates Strict Mode creation results resolving out of order', async () => {
    const old = deferred<NodeDebugPod>()
    const current = deferred<NodeDebugPod>()
    const create = vi.fn<() => Promise<NodeDebugPod>>()
      .mockReturnValueOnce(old.promise).mockReturnValueOnce(current.promise)
    const terminal = await mount(create, undefined, true)
    await act(async () => { current.resolve(pod('current')); old.resolve(pod('old')) })
    expect(terminal.cleanup.mock.calls).toEqual([['same-node', pod('old')]])
    expect(terminal.element.textContent).toBe('terminal')
    await unmount(terminal.root)
    expect(terminal.cleanup.mock.calls).toEqual([['same-node', pod('old')], ['same-node', pod('current')]])
  })
})
