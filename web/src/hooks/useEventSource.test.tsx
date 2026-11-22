// @vitest-environment jsdom
import { act, createRef, useImperativeHandle } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, expect, test, vi } from 'vitest'
import { connectedSwitchContext, type ContextSwitchOperation } from '../api/contextSwitch'
import { useEventSource } from './useEventSource'

class TestStream extends EventTarget {
  static opened: TestStream[] = []
  onopen?: () => void
  onerror?: () => void
  close = vi.fn()
  constructor() {
    super()
    TestStream.opened.push(this)
  }
  send(type: string, data: unknown) {
    this.dispatchEvent(new MessageEvent(type, { data: JSON.stringify(data) }))
  }
}

let root: Root
const hook = createRef<ReturnType<typeof useEventSource>>()
const changed = vi.fn()
const complete = vi.fn()
const eventReceived = vi.fn()
function Probe() {
  const result = useEventSource([], 'resources', { onContextChanged: changed, onContextSwitchComplete: complete, onK8sEvent: eventReceived })
  useImperativeHandle(hook, () => result, [result])
  return null
}

beforeEach(() => {
  vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true)
  vi.stubGlobal('EventSource', TestStream)
  TestStream.opened = []
  vi.clearAllMocks()
  root = createRoot(document.createElement('div'))
  act(() => root.render(<Probe />))
})
afterEach(() => {
  act(() => root.unmount())
  vi.unstubAllGlobals()
})

test('polling a completed switch discards old queued frames until the new stream delivers topology', () => {
  const old = TestStream.opened[0]
  act(() => old.send('topology', { nodes: [{ id: 'source' }] }))
  expect(hook.current!.topology?.nodes[0].id).toBe('source')
  act(() => old.send('topology', { nodes: [{ id: 'pending-source' }] }))
  act(() => hook.current!.applyContextChange('target', 'op'))
  expect(old.close).toHaveBeenCalledOnce()
  const fresh = TestStream.opened[1]
  act(() => {
    old.onopen?.()
    old.onerror?.()
    old.send('k8s_event', { name: 'source-event' })
    old.send('topology', { nodes: [{ id: 'queued-source' }] })
    old.send('context_changed', { context: 'target', switchId: 'op' })
  })
  expect(hook.current!.connecting).toBe(true)
  expect(hook.current!.topology).toBeNull()
  expect(hook.current!.events).toEqual([])
  expect(eventReceived).not.toHaveBeenCalled()
  expect(complete).not.toHaveBeenCalled()
  act(() => {
    fresh.send('context_changed', { context: 'target', switchId: 'op' })
    fresh.send('topology', { nodes: [{ id: 'target' }] })
  })
  expect(changed).toHaveBeenCalledOnce()
  expect(complete).toHaveBeenCalledOnce()
  expect(hook.current!.topology?.nodes[0].id).toBe('target')
})

test('an ordered stream completion keeps its target topology when the matching poll arrives', () => {
  const stream = TestStream.opened[0]
  act(() => {
    stream.send('context_changed', { context: 'target', switchId: 'op' })
    stream.send('topology', { nodes: [{ id: 'target' }] })
    hook.current!.applyContextChange('target', 'op')
  })
  expect(TestStream.opened).toHaveLength(1)
  expect(changed).toHaveBeenCalledOnce()
  expect(complete).toHaveBeenCalledOnce()
  expect(hook.current!.topology?.nodes[0].id).toBe('target')
})

test('a failed switch only resets the view when the server admitted a restored context', () => {
  const failed: ContextSwitchOperation = {
    id: 'op', target: 'target', phase: 'failed', error: 'target failed',
    progress: '', startedAt: '', syncTimeoutSeconds: 300,
  }
  expect(connectedSwitchContext(failed)).toBeUndefined()
  const restored = connectedSwitchContext({ ...failed, restoredContext: 'source' })
  expect(restored).toBe('source')
  act(() => hook.current!.applyContextChange(restored!, failed.id))
  act(() => TestStream.opened[1].send('context_changed', { context: 'source', switchId: failed.id }))
  expect(changed.mock.calls).toEqual([['source']])
})
