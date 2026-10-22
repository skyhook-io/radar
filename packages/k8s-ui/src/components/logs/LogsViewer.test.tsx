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

it('keeps received logs visible and reports a node failure until streaming is restarted', async () => {
  const fetchLogs = vi.fn(async () => ({ app: '' }))
  await renderViewer(fetchLogs, true)
  await act(async () => FakeEventSource.last?.emit('connected', {}))
  await act(async () => FakeEventSource.last?.emit('log', { timestamp: '2026-10-20T10:00:00Z', content: 'before failure', container: 'app' }))
  // The buffer batches streamed records before rendering them.
  await act(async () => { await new Promise(resolve => setTimeout(resolve, 100)) })
  await act(async () => FakeEventSource.last?.emit('error', { error: 'Kubernetes did not return this container\'s logs', error_code: 'logs_unavailable' }))
  expect(element.querySelector('[role="alert"]')?.textContent).toContain('Kubernetes did not return')
  expect(element.textContent).toContain('Previously loaded logs remain below.')
  expect(element.textContent).toContain('OTHER 1')
  expect(FakeEventSource.last?.closed).toBe(true)
  await act(async () => button('Stream').click())
  await act(async () => FakeEventSource.last?.emit('connected', {}))
  expect(element.querySelector('[role="alert"]')).toBeNull()
})

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

async function renderViewer(fetchLogs: (params: LogsFetchParams) => Promise<{ [container: string]: string }>, autoStream = false, containers = ['app']) {
  await act(async () =>
    root.render(
      <ToastProvider>
        <LogsViewer
          namespace="default"
          podName="web-0"
          containers={containers}
          fetchLogs={fetchLogs}
          createStream={() => new EventSource('/stream')}
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

function refreshButton(): HTMLButtonElement {
  const found = element.querySelector<HTMLButtonElement>('button[aria-label="Refresh logs"]')
  if (!found) throw new Error('Refresh button not rendered')
  return found
}

it('keeps records after a failed Refresh of the same selection and replaces them on success', async () => {
  const fetchLogs = vi.fn(async () => ({ app: '2026-10-20T10:00:00Z retained record' }))
  await renderViewer(fetchLogs)
  for (let attempt = 0; attempt < 2; attempt++) {
    fetchLogs.mockRejectedValueOnce(new Error('snapshot failed'))
    await act(async () => refreshButton().click())
    expect(element.querySelector('[role="alert"]')?.textContent).toContain('snapshot failed')
    expect(element.textContent).toContain('OTHER 1')
  }

  fetchLogs.mockResolvedValueOnce({ app: '' })
  await act(async () => refreshButton().click())
  expect(element.querySelector('[role="alert"]')).toBeNull()
  expect(element.textContent).not.toContain('OTHER 1')
  expect(element.textContent).not.toContain('snapshot failed')
})

it('keeps streamed records when Refresh fails after the stream stops', async () => {
  const fetchLogs = vi.fn(async () => ({ app: '' }))
  await renderViewer(fetchLogs, true)
  await act(async () => FakeEventSource.last?.emit('connected', {}))
  await act(async () => FakeEventSource.last?.emit('log', { timestamp: '2026-10-20T10:00:00Z', content: 'retained record', container: 'app' }))
  await act(async () => { await new Promise(resolve => setTimeout(resolve, 100)) })
  await act(async () => button('Stop').click())
  fetchLogs.mockRejectedValueOnce(new Error('snapshot failed'))

  await act(async () => refreshButton().click())

  expect(element.querySelector('[role="alert"]')?.textContent).toContain('snapshot failed')
  expect(element.textContent).toContain('OTHER 1')
})

it.each(['container', 'previous', 'range'])('clears prior records when a new %s snapshot fails', async selection => {
  const fetchLogs = vi.fn(async () => ({ app: '2026-10-20T10:00:00Z old selection' }))
  await renderViewer(fetchLogs, false, ['app', 'sidecar'])
  expect(element.textContent).toContain('OTHER 1')
  fetchLogs.mockRejectedValueOnce(new Error('snapshot failed'))

  await act(async () => {
    if (selection === 'previous') {
      previousCheckbox().click()
      return
    }
    const select = element.querySelectorAll('select')[selection === 'container' ? 0 : 1]
    if (!select) throw new Error('selection control not rendered')
    select.value = selection === 'container' ? 'sidecar' : '1000'
    select.dispatchEvent(new Event('change', { bubbles: true }))
  })

  expect(element.textContent).toContain('snapshot failed')
  expect(element.textContent).not.toContain('OTHER 1')
  expect(element.textContent).not.toContain('Previously loaded logs remain below.')
  expect(messageClass('snapshot failed')).toMatch(/text-red/)
})

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

it('with auto-stream on, a range picked while the stream opens reopens it with that range', async () => {
  const fetchLogs = vi.fn(async () => ({ app: '' }))
  await renderViewer(fetchLogs, true)
  const opening = FakeEventSource.last

  const range = element.querySelector<HTMLSelectElement>('select')
  if (!range) throw new Error('range select not rendered')
  await act(async () => {
    range.value = range.options[range.options.length - 1].value
    range.dispatchEvent(new Event('change', { bubbles: true }))
  })

  expect(opening?.closed).toBe(true)
  expect(FakeEventSource.last).not.toBe(opening)
  expect(fetchLogs).not.toHaveBeenCalled()
})

it('with auto-stream on, after the stream ended and was started again, a range picked while it opens reopens it', async () => {
  const fetchLogs = vi.fn(async () => ({ app: '' }))
  await renderViewer(fetchLogs, true)
  await act(async () => FakeEventSource.last?.emit('connected', {}))
  await act(async () => FakeEventSource.last?.emit('end', {}))
  const range = element.querySelector<HTMLSelectElement>('select')
  if (!range) throw new Error('range select not rendered')
  const pick = (index: number) => act(async () => {
    range.value = range.options[index].value
    range.dispatchEvent(new Event('change', { bubbles: true }))
  })
  await pick(range.options.length - 1)
  await act(async () => button('Stream').click())
  const opening = FakeEventSource.last

  await pick(range.options.length - 2)

  expect(opening?.closed).toBe(true)
  expect(FakeEventSource.last).not.toBe(opening)
})

it('a range picked while a manually started stream opens does not cancel it', async () => {
  const fetchLogs = vi.fn(async () => ({ app: '' }))
  await renderViewer(fetchLogs, true)
  await act(async () => FakeEventSource.last?.emit('connected', {}))
  await act(async () => button('Stop').click())
  await act(async () => button('Stream').click())
  const opening = FakeEventSource.last
  const range = element.querySelector<HTMLSelectElement>('select')
  if (!range) throw new Error('range select not rendered')

  await act(async () => {
    range.value = range.options[range.options.length - 1].value
    range.dispatchEvent(new Event('change', { bubbles: true }))
  })

  expect(opening?.closed).toBe(false)
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
