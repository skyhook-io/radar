// @vitest-environment jsdom
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { ToastProvider } from '../ui/Toast'
import { LogsViewer, type LogsFetchParams } from './LogsViewer'

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })

class FakeEventSource extends EventTarget {
  static last: FakeEventSource | undefined
  closed = false
  constructor(readonly url: string) {
    super()
    FakeEventSource.last = this
  }
  close() { this.closed = true }
  emit(type: string, data: unknown) {
    this.dispatchEvent(new MessageEvent(type, { data: JSON.stringify(data) }))
  }
}

// The shape the app's API client throws for a JSON error body.
class ApiError extends Error {
  constructor(message: string, readonly data: Record<string, unknown>) {
    super(message)
  }
}

const NO_EARLIER_RUN = 'This container has not restarted, so there is no earlier run to show.'

let root: Root
let element: HTMLDivElement
beforeEach(() => {
  vi.stubGlobal('EventSource', FakeEventSource)
  element = document.createElement('div')
  document.body.appendChild(element)
  root = createRoot(element)
})
afterEach(async () => {
  await act(async () => root.unmount())
  element.remove()
  vi.unstubAllGlobals()
})

async function renderViewer(fetchLogs: (params: LogsFetchParams) => Promise<{ [container: string]: string }>, autoStream = false, podName = 'web-0') {
  await act(async () =>
    root.render(
      <ToastProvider>
        <LogsViewer
          namespace="default"
          podName={podName}
          containers={['app']}
          fetchLogs={fetchLogs}
          createStream={() => new EventSource(`/pods/default/${podName}/logs/stream`)}
          autoStream={autoStream}
        />
      </ToastProvider>,
    ),
  )
}

function previousCheckbox(): HTMLInputElement {
  const box = element.querySelector<HTMLInputElement>('input[type="checkbox"]')
  if (!box) throw new Error('Previous checkbox not rendered')
  return box
}

function button(label: string): HTMLButtonElement {
  const found = Array.from(element.querySelectorAll('button')).find(b => b.textContent?.trim() === label)
  if (!found) throw new Error(`${label} button not rendered`)
  return found
}

// The class of the box the message sits in: the innermost element holding the
// text is the message itself, and its parent carries the colour.
function messageClass(text: string): string {
  const message = Array.from(element.querySelectorAll('span, div')).filter(el => el.textContent === text).pop()
  if (!message?.parentElement) throw new Error(`"${text}" not rendered`)
  return message.parentElement.className
}

it('starting a stream leaves the Previous view and its answer behind', async () => {
  const fetchLogs = vi.fn(async (params: LogsFetchParams) => {
    if (params.previous) throw new ApiError(NO_EARLIER_RUN, { error: NO_EARLIER_RUN, error_code: 'no_previous_run' })
    return { app: '' }
  })
  await renderViewer(fetchLogs)
  await act(async () => previousCheckbox().click())
  expect(element.textContent).toContain(NO_EARLIER_RUN)
  const callsBeforeStream = fetchLogs.mock.calls.length

  await act(async () => button('Stream').click())
  await act(async () => FakeEventSource.last?.emit('connected', {}))

  expect(element.textContent).not.toContain(NO_EARLIER_RUN)
  expect(previousCheckbox().checked).toBe(false)
  // Unticking Previous must not also load a snapshot over the stream.
  expect(fetchLogs.mock.calls.length).toBe(callsBeforeStream)
})

it('with auto-stream on, Stop, Previous, then Stream returns to the live stream', async () => {
  const fetchLogs = vi.fn(async (params: LogsFetchParams) => {
    if (params.previous) throw new ApiError(NO_EARLIER_RUN, { error: NO_EARLIER_RUN, error_code: 'no_previous_run' })
    return { app: '' }
  })
  await renderViewer(fetchLogs, true)
  await act(async () => FakeEventSource.last?.emit('connected', {}))
  await act(async () => button('Stop').click())
  await act(async () => previousCheckbox().click())
  expect(element.textContent).toContain(NO_EARLIER_RUN)
  const callsBeforeStream = fetchLogs.mock.calls.length

  await act(async () => button('Stream').click())
  await act(async () => FakeEventSource.last?.emit('connected', {}))

  expect(element.textContent).not.toContain(NO_EARLIER_RUN)
  expect(previousCheckbox().checked).toBe(false)
  expect(button('Stop')).toBeTruthy()
  expect(fetchLogs.mock.calls.length).toBe(callsBeforeStream)
})

it('with auto-stream on, Previous loads the earlier run once the stream ended by itself', async () => {
  const fetchLogs = vi.fn(async (params: LogsFetchParams) =>
    ({ app: params.previous ? '2026-10-13T10:00:00Z earlier run crashed' : '' }))
  await renderViewer(fetchLogs, true)
  await act(async () => FakeEventSource.last?.emit('connected', {}))
  // The container exited: the stream ends without the user pressing Stop.
  await act(async () => FakeEventSource.last?.emit('end', {}))
  expect(button('Stream')).toBeTruthy()

  await act(async () => previousCheckbox().click())

  expect(fetchLogs).toHaveBeenCalledWith(expect.objectContaining({ previous: true }))
  // The earlier run's line is in the buffer: the level chips count it.
  expect(element.textContent).toContain('OTHER 1')
})

it('with auto-stream on, a time range picked after the stream ended loads it instead of reopening the stream', async () => {
  const fetchLogs = vi.fn(async () => ({ app: '' }))
  await renderViewer(fetchLogs, true)
  await act(async () => FakeEventSource.last?.emit('connected', {}))
  await act(async () => FakeEventSource.last?.emit('end', {}))
  const ended = FakeEventSource.last
  await act(async () => previousCheckbox().click())

  const range = element.querySelector<HTMLSelectElement>('select')
  if (!range) throw new Error('range select not rendered')
  await act(async () => {
    range.value = range.options[range.options.length - 1].value
    range.dispatchEvent(new Event('change', { bubbles: true }))
  })

  expect(FakeEventSource.last).toBe(ended)
  expect(previousCheckbox().checked).toBe(true)
  expect(fetchLogs).toHaveBeenLastCalledWith(expect.objectContaining({ previous: true, sinceSeconds: expect.any(Number) }))
})

it('with auto-stream on, the open stream is not replaced by a snapshot', async () => {
  const fetchLogs = vi.fn(async () => ({ app: '' }))
  await renderViewer(fetchLogs, true)
  await act(async () => FakeEventSource.last?.emit('connected', {}))
  expect(fetchLogs).not.toHaveBeenCalled()
  expect(button('Stop')).toBeTruthy()
})

it('a Previous answer that arrives after the stream started does not cover it', async () => {
  let reject: (err: Error) => void = () => {}
  const fetchLogs = vi.fn((params: LogsFetchParams) =>
    params.previous
      ? new Promise<{ [container: string]: string }>((_, r) => { reject = r })
      : Promise.resolve({ app: '' }))
  await renderViewer(fetchLogs)
  await act(async () => previousCheckbox().click())
  await act(async () => button('Stream').click())
  await act(async () => FakeEventSource.last?.emit('connected', {}))
  await act(async () => FakeEventSource.last?.emit('log', { timestamp: '2026-10-13T10:00:00Z', content: 'live line', container: 'app' }))
  // Appends are batched to the next animation frame.
  await act(async () => { await new Promise(resolve => requestAnimationFrame(() => resolve(null))) })
  await act(async () => reject(new ApiError(NO_EARLIER_RUN, { error: NO_EARLIER_RUN, error_code: 'no_previous_run' })))

  expect(element.textContent).not.toContain(NO_EARLIER_RUN)
  // The live line is still in the buffer: the level chips count it.
  expect(element.textContent).toContain('OTHER 1')
})

it('a Previous answer replaces an earlier stream failure', async () => {
  const fetchLogs = vi.fn(async (params: LogsFetchParams) => {
    if (params.previous) throw new ApiError(NO_EARLIER_RUN, { error: NO_EARLIER_RUN, error_code: 'no_previous_run' })
    return { app: '' }
  })
  await renderViewer(fetchLogs)
  await act(async () => button('Stream').click())
  await act(async () => FakeEventSource.last?.emit('error', { error: 'stream dropped' }))
  expect(element.textContent).toContain('stream dropped')

  await act(async () => previousCheckbox().click())
  expect(element.textContent).toContain(NO_EARLIER_RUN)
  expect(element.textContent).not.toContain('stream dropped')
})

it('shows a Kubernetes answer in the empty-pane style, not as an error', async () => {
  await renderViewer(async () => { throw new ApiError(NO_EARLIER_RUN, { error: NO_EARLIER_RUN, error_code: 'no_previous_run' }) })
  expect(element.textContent).toContain(NO_EARLIER_RUN)
  expect(messageClass(NO_EARLIER_RUN)).not.toMatch(/text-red/)
})

it('shows the live stream\'s Kubernetes answer in the empty-pane style, and a stream failure as an error', async () => {
  const unavailable = 'Kubernetes did not return this container\'s logs'
  await renderViewer(async () => ({ app: '' }))
  await act(async () => button('Stream').click())
  await act(async () => FakeEventSource.last?.emit('error', { error: unavailable, error_code: 'logs_unavailable' }))
  expect(messageClass(unavailable)).not.toMatch(/text-red/)

  await act(async () => button('Stream').click())
  await act(async () => FakeEventSource.last?.emit('error', { error: 'stream dropped' }))
  expect(messageClass('stream dropped')).toMatch(/text-red/)
})

it('a snapshot load replaces an ended stream\'s answer even when it succeeds empty', async () => {
  const unavailable = 'Kubernetes did not return this container\'s logs'
  await renderViewer(async () => ({ app: '' }))
  await act(async () => button('Stream').click())
  await act(async () => FakeEventSource.last?.emit('error', { error: unavailable, error_code: 'logs_unavailable' }))
  expect(element.textContent).toContain(unavailable)

  await act(async () => previousCheckbox().click())
  expect(element.textContent).not.toContain(unavailable)
})

it('still shows a Radar failure as an error', async () => {
  const failure = 'Failed to fetch logs: connection refused'
  await renderViewer(async () => { throw new ApiError(failure, { error: failure }) })
  expect(messageClass(failure)).toMatch(/text-red/)
})

function rangeSelect(): HTMLSelectElement {
  const range = element.querySelector<HTMLSelectElement>('select')
  if (!range) throw new Error('range select not rendered')
  return range
}

const nextFrame = () => act(async () => { await new Promise(resolve => requestAnimationFrame(() => resolve(null))) })

it('opening another pod with the same container name streams it, even after the first pod\'s stream ended', async () => {
  const fetchLogs = vi.fn(async () => ({ app: '' }))
  await renderViewer(fetchLogs, true, 'web-0')
  await act(async () => FakeEventSource.last?.emit('connected', {}))
  await act(async () => FakeEventSource.last?.emit('end', {}))
  const ended = FakeEventSource.last

  await renderViewer(fetchLogs, true, 'web-1')

  expect(FakeEventSource.last).not.toBe(ended)
  expect(FakeEventSource.last?.url).toBe('/pods/default/web-1/logs/stream')
  expect(fetchLogs).not.toHaveBeenCalled()
})

it('a manual Stop on one pod does not stop the next pod from streaming', async () => {
  const fetchLogs = vi.fn(async () => ({ app: '' }))
  await renderViewer(fetchLogs, true, 'web-0')
  await act(async () => FakeEventSource.last?.emit('connected', {}))
  await act(async () => button('Stop').click())

  await renderViewer(fetchLogs, true, 'web-1')

  expect(FakeEventSource.last?.url).toBe('/pods/default/web-1/logs/stream')
})

it('Previous and the range are locked while an auto-stream opens', async () => {
  await renderViewer(async () => ({ app: '' }), true)
  expect(previousCheckbox().disabled).toBe(true)
  expect(rangeSelect().disabled).toBe(true)

  await act(async () => FakeEventSource.last?.emit('connected', {}))
  await act(async () => FakeEventSource.last?.emit('end', {}))
  expect(previousCheckbox().disabled).toBe(false)
  expect(rangeSelect().disabled).toBe(false)
})

it('Previous and the range are locked while a manually started stream opens', async () => {
  await renderViewer(async () => ({ app: '' }))
  expect(previousCheckbox().disabled).toBe(false)

  await act(async () => button('Stream').click())

  expect(previousCheckbox().disabled).toBe(true)
  expect(rangeSelect().disabled).toBe(true)
})

it('a stream that is still opening can be stopped, which unlocks Previous', async () => {
  await renderViewer(async () => ({ app: '' }))
  await act(async () => button('Stream').click())
  const opening = FakeEventSource.last

  await act(async () => button('Stop').click())

  expect(opening?.closed).toBe(true)
  expect(previousCheckbox().disabled).toBe(false)
  expect(button('Stream')).toBeTruthy()
})

it('a stream failure after lines arrived is shown above them', async () => {
  await renderViewer(async () => ({ app: '' }))
  await act(async () => button('Stream').click())
  await act(async () => FakeEventSource.last?.emit('connected', {}))
  await act(async () => FakeEventSource.last?.emit('log', { timestamp: '2026-10-14T10:00:00Z', content: 'live line', container: 'app' }))
  await nextFrame()
  await act(async () => FakeEventSource.last?.emit('error', { error: 'Read error: unexpected EOF' }))

  expect(element.querySelector('[role="alert"]')?.textContent).toBe('Read error: unexpected EOF · Previously loaded logs remain below.')
  expect(element.textContent).toContain('OTHER 1')
})

const NOT_STARTED = 'This container has not started yet, so it has no logs. Kubernetes reports ContainerCreating.'

it('shows a container that has not started in the empty-pane style', async () => {
  await renderViewer(async () => { throw new ApiError(NOT_STARTED, { error: NOT_STARTED, error_code: 'container_not_started' }) })
  expect(messageClass(NOT_STARTED)).not.toMatch(/text-red/)
})

it('shows what the live stream is waiting for, and drops it once lines arrive', async () => {
  const waiting = `${NOT_STARTED} Logs will show here once it starts.`
  await renderViewer(async () => ({ app: '' }), true)
  await act(async () => FakeEventSource.last?.emit('connected', {}))
  await act(async () => FakeEventSource.last?.emit('notice', { notice: waiting, notice_code: 'container_not_started' }))
  expect(messageClass(waiting)).not.toMatch(/text-red/)
  expect(button('Stop')).toBeTruthy()

  await act(async () => FakeEventSource.last?.emit('notice', { notice: '' }))
  await act(async () => FakeEventSource.last?.emit('log', { timestamp: '2026-10-14T10:00:00Z', content: 'started', container: 'app' }))
  await nextFrame()

  expect(element.textContent).not.toContain(waiting)
  expect(element.textContent).toContain('OTHER 1')
})
