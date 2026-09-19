import { afterEach, describe, expect, it, vi } from 'vitest'

import { handleSSEError } from './log-format'

// The SSE `error` events the log-stream handler emits arrive as MessageEvents
// carrying a JSON body. A genuine transport failure arrives as a bare Event
// with no data at all. Both reach the same listener, so the distinction is
// only visible in `data`.
const errorEvent = (data?: string): Event =>
  ({ type: 'error', ...(data === undefined ? {} : { data }) }) as Event

const TRANSPORT_FALLBACK = 'Log stream connection failed'
const noop = () => {}

afterEach(() => {
  vi.restoreAllMocks()
})

describe('handleSSEError', () => {
  it('returns the reason the server sent instead of the transport fallback', () => {
    // The shape handlePodLogsStream emits when the container has never
    // started, so the kubelet has no logs to hand back.
    const serverReason =
      'Failed to open log stream: container "checkout" in pod ' +
      '"checkout-7d9f8b6c4-2xk9p" is waiting to start: ' +
      'trying and failing to pull image'
    vi.spyOn(console, 'error').mockImplementation(() => {})

    const shown = handleSSEError(
      errorEvent(JSON.stringify({ error: serverReason })),
      TRANSPORT_FALLBACK,
      noop,
    )

    expect(shown).toBe(serverReason)
  })

  it('accepts `message` as well as `error` as the payload key', () => {
    vi.spyOn(console, 'error').mockImplementation(() => {})

    expect(
      handleSSEError(errorEvent(JSON.stringify({ message: 'namespace is gone' })), TRANSPORT_FALLBACK, noop),
    ).toBe('namespace is gone')
  })

  it('falls back to the raw body when the payload is not JSON', () => {
    vi.spyOn(console, 'error').mockImplementation(() => {})

    expect(handleSSEError(errorEvent('upstream closed'), TRANSPORT_FALLBACK, noop)).toBe('upstream closed')
  })

  it('falls back to the transport message when a JSON payload carries no reason', () => {
    vi.spyOn(console, 'error').mockImplementation(() => {})

    expect(handleSSEError(errorEvent(JSON.stringify({ code: 500 })), TRANSPORT_FALLBACK, noop)).toBe(
      TRANSPORT_FALLBACK,
    )
  })

  it('ignores a non-string reason rather than showing the reader an object', () => {
    vi.spyOn(console, 'error').mockImplementation(() => {})

    expect(
      handleSSEError(errorEvent(JSON.stringify({ error: { code: 500 } })), TRANSPORT_FALLBACK, noop),
    ).toBe(TRANSPORT_FALLBACK)
  })

  it('uses the transport message when the event carries no body at all', () => {
    // A real dropped connection: EventSource fires a bare Event, so the
    // fallback is the honest thing to show.
    vi.spyOn(console, 'error').mockImplementation(() => {})

    expect(handleSSEError(errorEvent(), TRANSPORT_FALLBACK, noop)).toBe(TRANSPORT_FALLBACK)
  })

  it('still logs the server reason for debugging', () => {
    const spy = vi.spyOn(console, 'error').mockImplementation(() => {})

    handleSSEError(errorEvent(JSON.stringify({ error: 'boom' })), TRANSPORT_FALLBACK, noop)

    expect(spy).toHaveBeenCalledWith(`${TRANSPORT_FALLBACK}:`, 'boom')
  })

  it('still invokes onClose when one is passed', () => {
    vi.spyOn(console, 'error').mockImplementation(() => {})
    const onClose = vi.fn()

    handleSSEError(errorEvent(JSON.stringify({ error: 'boom' })), TRANSPORT_FALLBACK, onClose)

    expect(onClose).toHaveBeenCalledOnce()
  })
})
