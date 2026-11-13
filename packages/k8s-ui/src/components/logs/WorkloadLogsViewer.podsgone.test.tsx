// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { expect, it, vi } from 'vitest'
import { WorkloadLogsViewer, type WorkloadLogsResult } from './WorkloadLogsViewer'
vi.mock('../ui/Toast', () => ({ useToast: () => ({ showError: vi.fn(), showSuccess: vi.fn() }) }))
Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })

class FakeEventSource extends EventTarget {
  static last: FakeEventSource | undefined
  closed = false
  constructor() { super(); FakeEventSource.last = this }
  close() { this.closed = true }
  emit(type: string, data: unknown) { this.dispatchEvent(new MessageEvent(type, { data: JSON.stringify(data) })) }
}

const loki: WorkloadLogsResult = { pods: [{ name: 'run-1-abcde', containers: ['job'], ready: false }], logs: [{ pod: 'run-1-abcde', container: 'job', timestamp: '2026-11-13T02:00:00.000000000Z', content: 'done' }], notice: 'From Loki' }

async function streamEndsPodsGone(readSnapshotWhenPodsGone: boolean) {
  const host = document.createElement('div'); const root = createRoot(host)
  const fetchAll = vi.fn(async () => loki)
  await act(async () => root.render(<WorkloadLogsViewer name="run-1" fetchAll={fetchAll} createStream={() => new FakeEventSource() as unknown as EventSource} autoStream readSnapshotWhenPodsGone={readSnapshotWhenPodsGone} />))
  await act(async () => FakeEventSource.last?.emit('connected', {}))
  await act(async () => FakeEventSource.last?.emit('end', { reason: 'pods-gone', emptyReason: 'pods-gone', emptyMessage: 'This Job has finished, but its pods are no longer present' }))
  await act(async () => { await new Promise(r => setTimeout(r, 100)) })
  const text = host.textContent ?? ''
  await act(async () => root.unmount())
  return { fetchAll, text }
}

it('reads the snapshot once the stream says the pods are gone, when the host can serve them', async () => {
  const { fetchAll, text } = await streamEndsPodsGone(true)
  expect(fetchAll).toHaveBeenCalled()
  expect(text).toContain('From Loki')
})

it('keeps the stream\'s answer otherwise', async () => {
  const { fetchAll, text } = await streamEndsPodsGone(false)
  expect(fetchAll).not.toHaveBeenCalled()
  expect(text).toContain('no longer present')
})
