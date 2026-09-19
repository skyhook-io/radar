import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { attachLogStreamListeners, type LogStreamControls } from './log-stream-listeners'

// What the pod log handler sends when the container has never started, so
// there is nothing to stream. A pod stuck in ImagePullBackOff produces it.
const NEVER_STARTED =
  'Failed to open log stream: container "checkout" in pod ' +
  '"checkout-7d9f8b6c4-2xk9p" is waiting to start: ' +
  'trying and failing to pull image'

const TRANSPORT_FALLBACK = 'Log stream connection failed'

class FakeEventSource {
  closed = false
  private listeners = new Map<string, ((event: Event) => void)[]>()

  addEventListener(type: string, listener: (event: Event) => void) {
    const existing = this.listeners.get(type) ?? []
    existing.push(listener)
    this.listeners.set(type, existing)
  }

  close() {
    this.closed = true
  }

  /** `data` omitted models the bare Event a dropped connection produces. */
  emit(type: string, data?: unknown) {
    const event = { type, ...(data === undefined ? {} : { data: JSON.stringify(data) }) } as Event
    for (const l of this.listeners.get(type) ?? []) l(event)
  }
}

function setup(overrides: Partial<LogStreamControls> = {}) {
  const es = new FakeEventSource()
  const state = {
    isStreaming: false as boolean,
    connecting: true as boolean,
    streamError: null as string | null,
    ended: null as string | null,
  }
  const ctl: LogStreamControls = {
    setIsStreaming: (v) => { state.isStreaming = v },
    setConnecting: (v) => { state.connecting = v },
    setStreamError: (v) => { state.streamError = v },
    setStreamEnded: (v) => { state.ended = v },
    isCurrent: () => true,
    ended: { current: false },
    ...overrides,
  }
  const onLog = vi.fn()
  attachLogStreamListeners(es as unknown as EventSource, { onLog }, TRANSPORT_FALLBACK, ctl)
  return { es, state, ctl, onLog }
}

beforeEach(() => {
  vi.spyOn(console, 'error').mockImplementation(() => {})
})

afterEach(() => {
  vi.restoreAllMocks()
})

describe('attachLogStreamListeners', () => {
  it('shows the reason the server gave when the stream fails before connecting', () => {
    // Radar 1.7.x reports the refusal and never sends `connected`.
    const { es, state } = setup()

    es.emit('error', { error: NEVER_STARTED })

    expect(state.streamError).toBe(NEVER_STARTED)
    expect(state.isStreaming).toBe(false)
    expect(state.connecting).toBe(false)
    expect(es.closed).toBe(true)
  })

  it('shows the reason when the stream connects first and then fails', () => {
    // Current Radar flushes `connected` before it opens the log stream, so the
    // same refusal arrives after the handshake.
    const { es, state } = setup()

    es.emit('connected', { pod: 'checkout-7d9f8b6c4-2xk9p' })
    expect(state.isStreaming).toBe(true)

    es.emit('error', { error: NEVER_STARTED })

    expect(state.streamError).toBe(NEVER_STARTED)
    expect(state.isStreaming).toBe(false)
  })

  it('falls back to the transport wording when the connection just drops', () => {
    const { es, state } = setup()

    es.emit('error')

    expect(state.streamError).toBe(TRANSPORT_FALLBACK)
  })

  it('stays silent on the error that follows a clean end', () => {
    const { es, state } = setup()

    es.emit('end', { reason: 'pod terminated' })
    es.emit('error')

    expect(state.streamError).toBeNull()
    expect(state.isStreaming).toBe(false)
  })

  it('ignores a late error from a superseded stream', () => {
    // A container switch replaces the EventSource; the old one can still fire.
    const { es, state } = setup({ isCurrent: () => false })

    es.emit('error', { error: 'stale failure' })

    expect(state.streamError).toBeNull()
    expect(es.closed).toBe(true)
  })

  it('keeps delivering log lines and clears the connecting state', () => {
    const { es, state, onLog } = setup()

    es.emit('log', { content: 'listening on :8080' })

    expect(onLog).toHaveBeenCalledWith({ content: 'listening on :8080' })
    expect(state.connecting).toBe(false)
  })

  it('survives a malformed payload without surfacing an error to the reader', () => {
    const { es, state, onLog } = setup()
    // Bypass the JSON.stringify in emit() to deliver a broken body.
    ;(es as unknown as { emit: (t: string, d?: unknown) => void }).emit('log')

    expect(onLog).not.toHaveBeenCalled()
    expect(state.streamError).toBeNull()
  })
})

describe('end reasons from the workload stream', () => {
  it('prefers the written message over the machine slug', () => {
    const { es, state } = setup()

    es.emit('end', {
      reason: 'no-pods',
      emptyReason: 'no-pods',
      emptyMessage: 'No pods are running for this workload.',
    })

    expect(state.ended).toBe('No pods are running for this workload.')
  })

  it('never surfaces the slug when no written message came with it', () => {
    const { es, state } = setup()

    es.emit('end', { reason: 'pods-gone', emptyReason: 'pods-gone' })

    expect(state.ended).toBe('')
  })

  it('still uses a plain reason when the payload carries no slug at all', () => {
    const { es, state } = setup()

    es.emit('end', { reason: 'container exited with code 137' })

    expect(state.ended).toBe('container exited with code 137')
  })
})
