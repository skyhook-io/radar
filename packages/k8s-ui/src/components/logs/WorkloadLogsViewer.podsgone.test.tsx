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

it('keeps lines the stream already showed when the pods then go', async () => {
  const host = document.createElement('div'); const root = createRoot(host)
  const fetchAll = vi.fn(async () => ({ pods: [], logs: [], emptyReason: 'pods-gone' }))
  await act(async () => root.render(<WorkloadLogsViewer name="run-1" fetchAll={fetchAll} createStream={() => new FakeEventSource() as unknown as EventSource} autoStream readSnapshotWhenPodsGone />))
  await act(async () => FakeEventSource.last?.emit('connected', {}))
  await act(async () => FakeEventSource.last?.emit('log', { pod: 'run-1-abcde', container: 'job', timestamp: '2026-11-13T02:00:00Z', content: 'last words' }))
  await act(async () => { await new Promise(r => setTimeout(r, 100)) })
  await act(async () => FakeEventSource.last?.emit('end', { reason: 'pods-gone', emptyReason: 'pods-gone' }))
  await act(async () => { await new Promise(r => setTimeout(r, 100)) })
  expect(fetchAll).not.toHaveBeenCalled()
  await act(async () => root.unmount())
})

it('reads the snapshot when the host becomes able to after the stream ended', async () => {
  const host = document.createElement('div'); const root = createRoot(host)
  const fetchAll = vi.fn(async () => loki)
  const view = (ready: boolean) => <WorkloadLogsViewer name="run-1" fetchAll={fetchAll} createStream={() => new FakeEventSource() as unknown as EventSource} autoStream readSnapshotWhenPodsGone={ready} />
  await act(async () => root.render(view(false)))
  await act(async () => FakeEventSource.last?.emit('connected', {}))
  await act(async () => FakeEventSource.last?.emit('end', { reason: 'pods-gone', emptyReason: 'pods-gone' }))
  expect(fetchAll).not.toHaveBeenCalled()
  await act(async () => root.render(view(true)))
  await act(async () => { await new Promise(r => setTimeout(r, 100)) })
  expect(fetchAll).toHaveBeenCalledTimes(1)
  await act(async () => root.unmount())
})

it('keeps the pods the user deselected while the stream ran', async () => {
  const host = document.createElement('div'); document.body.append(host); const root = createRoot(host)
  const two = [{ name: 'run-1-abcde', containers: ['job'], ready: false }, { name: 'run-1-fghij', containers: ['job'], ready: false }]
  const fetchAll = vi.fn(async () => ({ ...loki, pods: two }))
  await act(async () => root.render(<WorkloadLogsViewer name="run-1" fetchAll={fetchAll} createStream={() => new FakeEventSource() as unknown as EventSource} autoStream readSnapshotWhenPodsGone />))
  await act(async () => FakeEventSource.last?.emit('connected', { pods: two }))
  const filter = [...host.querySelectorAll('button')].find(b => b.textContent?.includes('pods'))!
  await act(async () => filter.click())
  const box = [...host.querySelectorAll('label')].find(l => l.textContent?.includes('run-1-fghij'))!.querySelector('input')!
  await act(async () => box.click())
  expect(host.textContent).toContain('1/2 pods')
  await act(async () => FakeEventSource.last?.emit('end', { reason: 'pods-gone', emptyReason: 'pods-gone' }))
  await act(async () => { await new Promise(r => setTimeout(r, 100)) })
  expect(fetchAll).toHaveBeenCalled()
  expect(host.textContent).toContain('1/2 pods')
  await act(async () => root.unmount())
  host.remove()
})

// A run read from a log store lists every container of the run, so after
// choosing one the user can switch straight to another.
it('switches container all → A → B on a snapshot host', async () => {
  const host = document.createElement('div'); document.body.append(host); const root = createRoot(host)
  const asked: (string | undefined)[] = []
  const fetchAll = vi.fn(async ({ container }: { container?: string }) => {
    asked.push(container)
    return { pods: [{ name: 'run-1-abcde', containers: ['setup', 'work'], ready: false }], logs: [{ pod: 'run-1-abcde', container: container ?? 'work', timestamp: '2026-11-13T02:00:00.000000000Z', content: `from ${container ?? 'all'}` }], notice: 'From Loki' }
  })
  await act(async () => root.render(<WorkloadLogsViewer name="run-1" fetchAll={fetchAll} />))
  await act(async () => { await new Promise(r => setTimeout(r, 50)) })
  const picker = () => host.querySelector('select')!
  const choose = async (value: string) => {
    await act(async () => { picker().value = value; picker().dispatchEvent(new Event('change', { bubbles: true })) })
    await act(async () => { await new Promise(r => setTimeout(r, 50)) })
  }
  await choose('setup')
  expect([...picker().options].map(o => o.value)).toEqual(['', 'setup', 'work'])
  await choose('work')
  expect(asked).toEqual([undefined, 'setup', 'work'])
  await act(async () => root.unmount())
  host.remove()
})

// A run read whole from a log store ignores the line count and time range, so
// the control is disabled while that answer is shown, and enabled again after.
it('disables the range control while the answer has a fixed range', async () => {
  const host = document.createElement('div'); document.body.append(host); const root = createRoot(host)
  let fixed = true
  const fetchAll = vi.fn(async () => ({ ...loki, fixedRangeReason: fixed ? 'Read whole from Loki' : undefined }))
  await act(async () => root.render(<WorkloadLogsViewer name="run-1" fetchAll={fetchAll} />))
  await act(async () => { await new Promise(r => setTimeout(r, 50)) })
  const range = () => [...host.querySelectorAll('select')].find(s => s.querySelector('optgroup[label="Lines"]'))!
  expect(range().disabled).toBe(true)
  fixed = false
  const refresh = [...host.querySelectorAll('button')].find(b => b.getAttribute('title')?.toLowerCase().includes('refresh') || b.getAttribute('aria-label')?.toLowerCase().includes('refresh'))
  expect(refresh).toBeTruthy()
  await act(async () => { refresh!.click(); await new Promise(r => setTimeout(r, 50)) })
  expect(range().disabled).toBe(false)
  await act(async () => root.unmount())
  host.remove()
})

// Choosing a container Loki has no lines for keeps the run's pods and
// containers, so the user can go on to one that has lines.
it('switches all → container without lines → container with lines', async () => {
  const host = document.createElement('div'); document.body.append(host); const root = createRoot(host)
  const asked: (string | undefined)[] = []
  const fetchAll = vi.fn(async ({ container }: { container?: string }): Promise<WorkloadLogsResult> => {
    asked.push(container)
    if (container === 'setup') return { pods: [], logs: [], keepPreviousSources: true, emptyMessage: 'Loki has no lines from container setup for this run' }
    return { pods: [{ name: 'run-1-abcde', containers: ['setup', 'work'], ready: false }], logs: [{ pod: 'run-1-abcde', container: 'work', timestamp: '2026-11-13T02:00:00.000000000Z', content: 'from work' }], notice: 'From Loki' }
  })
  await act(async () => root.render(<WorkloadLogsViewer name="run-1" fetchAll={fetchAll} />))
  await act(async () => { await new Promise(r => setTimeout(r, 50)) })
  const picker = () => host.querySelector('select')!
  const choose = async (value: string) => {
    await act(async () => { picker().value = value; picker().dispatchEvent(new Event('change', { bubbles: true })) })
    await act(async () => { await new Promise(r => setTimeout(r, 50)) })
  }
  await choose('setup')
  expect([...picker().options].map(o => o.value)).toEqual(['', 'setup', 'work'])
  expect(picker().value).toBe('setup')
  expect(host.textContent).toContain('Loki has no lines from container setup for this run')
  expect(host.textContent).toContain('1/1 pods')
  await choose('work')
  expect(asked).toEqual([undefined, 'setup', 'work'])
  expect(picker().value).toBe('work')
  expect([...picker().options].map(o => o.value)).toEqual(['', 'setup', 'work'])
  expect(host.textContent).not.toContain('Loki has no lines from container setup')
  await act(async () => root.unmount())
  host.remove()
})

// The picker always offers the container it shows, even before any source lists it.
it('offers the chosen container when no pod lists it', async () => {
  const host = document.createElement('div'); document.body.append(host); const root = createRoot(host)
  const fetchAll = vi.fn(async (): Promise<WorkloadLogsResult> => ({ pods: [], logs: [] }))
  await act(async () => root.render(<WorkloadLogsViewer name="run-1" fetchAll={fetchAll} initialContainer="setup" />))
  await act(async () => { await new Promise(r => setTimeout(r, 50)) })
  const picker = host.querySelector('select')!
  expect([...picker.options].map(o => o.value)).toContain('setup')
  expect(picker.value).toBe('setup')
  await act(async () => root.unmount())
  host.remove()
})

// The default host auto-streams. Once a run is read from the log store because
// its pods are gone, a container change reads the store again instead of
// opening a Kubernetes stream whose empty pod list would wipe the run's pods.
it('switches all → container without lines → container with lines while auto-streaming', async () => {
  const host = document.createElement('div'); document.body.append(host); const root = createRoot(host)
  const asked: (string | undefined)[] = []
  const fetchAll = vi.fn(async ({ container }: { container?: string }): Promise<WorkloadLogsResult> => {
    asked.push(container)
    if (container === 'setup') return { pods: [], logs: [], keepPreviousSources: true, emptyMessage: 'Loki has no lines from container setup for this run' }
    return { pods: [{ name: 'run-1-abcde', containers: ['setup', 'work'], ready: false }], logs: [{ pod: 'run-1-abcde', container: 'work', timestamp: '2026-11-13T02:00:00.000000000Z', content: 'from work' }], notice: 'From Loki' }
  })
  await act(async () => root.render(<WorkloadLogsViewer name="run-1" fetchAll={fetchAll} createStream={() => new FakeEventSource() as unknown as EventSource} autoStream readSnapshotWhenPodsGone />))
  const podsGone = async () => {
    await act(async () => FakeEventSource.last?.emit('connected', { pods: [] }))
    await act(async () => FakeEventSource.last?.emit('end', { reason: 'pods-gone', emptyReason: 'pods-gone', emptyMessage: 'This Job has finished, but its pods are no longer present' }))
    await act(async () => { await new Promise(r => setTimeout(r, 50)) })
  }
  await podsGone()
  const picker = () => host.querySelector('select')!
  const choose = async (value: string) => {
    const before = FakeEventSource.last
    await act(async () => { picker().value = value; picker().dispatchEvent(new Event('change', { bubbles: true })) })
    // A stream opened for the change ends as the real one would.
    if (FakeEventSource.last !== before) await podsGone()
    await act(async () => { await new Promise(r => setTimeout(r, 50)) })
  }
  expect([...picker().options].map(o => o.value)).toEqual(['', 'setup', 'work'])
  await choose('setup')
  expect([...picker().options].map(o => o.value)).toEqual(['', 'setup', 'work'])
  expect(host.textContent).toContain('1/1 pods')
  expect(host.textContent).toContain('Loki has no lines from container setup for this run')
  await choose('work')
  expect(asked).toEqual([undefined, 'setup', 'work'])
  expect([...picker().options].map(o => o.value)).toEqual(['', 'setup', 'work'])
  expect(picker().value).toBe('work')
  await act(async () => root.unmount())
  host.remove()
})
