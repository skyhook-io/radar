import { afterEach, describe, expect, it, vi } from 'vitest'

import { handleSSEError } from './log-format'

// The stream handler reports a failure it can explain as an SSE `error` event
// carrying the reason. A dropped connection fires the same listener with no
// body at all. Only the body separates them.
const errorEvent = (data?: string): Event =>
  ({ type: 'error', ...(data === undefined ? {} : { data }) }) as Event

const TRANSPORT_FALLBACK = 'Log stream connection failed'
const noop = () => {}

afterEach(() => {
  vi.restoreAllMocks()
})

describe('handleSSEError', () => {
  it('returns the reason the server sent, instead of the transport wording', () => {
    // A container that cannot start has no logs, and the handler says so. That
    // sentence used to be parsed, logged to the console, and discarded, leaving
    // the reader looking at "connection failed" for a healthy connection.
    const serverReason =
      'Failed to open log stream: container "checkout" in pod ' +
      '"checkout-7d9f8b6c4-2xk9p" is waiting to start: trying and failing to pull image'
    vi.spyOn(console, 'error').mockImplementation(noop)

    expect(handleSSEError(errorEvent(JSON.stringify({ error: serverReason })), TRANSPORT_FALLBACK, noop))
      .toBe(serverReason)
  })

  it('keeps the transport wording when the connection simply dropped', () => {
    vi.spyOn(console, 'error').mockImplementation(noop)

    expect(handleSSEError(errorEvent(), TRANSPORT_FALLBACK, noop)).toBe(TRANSPORT_FALLBACK)
  })

  it('keeps the transport wording when the payload carries no reason', () => {
    vi.spyOn(console, 'error').mockImplementation(noop)

    expect(handleSSEError(errorEvent(JSON.stringify({ code: 500 })), TRANSPORT_FALLBACK, noop))
      .toBe(TRANSPORT_FALLBACK)
  })

  it('ignores a non-string reason rather than showing the reader an object', () => {
    vi.spyOn(console, 'error').mockImplementation(noop)

    expect(handleSSEError(errorEvent(JSON.stringify({ error: { code: 500 } })), TRANSPORT_FALLBACK, noop))
      .toBe(TRANSPORT_FALLBACK)
  })

  it('still logs the reason, and still closes', () => {
    const spy = vi.spyOn(console, 'error').mockImplementation(noop)
    const onClose = vi.fn()

    handleSSEError(errorEvent(JSON.stringify({ error: 'boom' })), TRANSPORT_FALLBACK, onClose)

    expect(spy).toHaveBeenCalledWith(`${TRANSPORT_FALLBACK}:`, 'boom')
    expect(onClose).toHaveBeenCalledOnce()
  })
})
