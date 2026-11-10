import { describe, it, expect, vi } from 'vitest'
import { isInvalidStateError, isWebKitGTK, startViewTransitionSafe } from './view-transition'

// SKY-833 bug 49: rapid tab / route switches surfaced
// "InvalidStateError: Transition was aborted because of invalid state"
// as a console exception. The View Transitions API rejects the
// `finished` promise with that error whenever a new transition
// supersedes an in-flight one — which is the user's intent (they
// clicked again), not a bug. These tests pin the rejection-
// classification helper and the fallback path that's used when the
// API isn't available (Firefox, older Safari, jsdom).

describe('isInvalidStateError', () => {
  it('returns true for objects whose name is "InvalidStateError"', () => {
    expect(isInvalidStateError(new DOMException('boom', 'InvalidStateError'))).toBe(true)
    expect(isInvalidStateError({ name: 'InvalidStateError', message: 'x' })).toBe(true)
  })

  it('returns false for any other shape', () => {
    expect(isInvalidStateError(new Error('boom'))).toBe(false)
    expect(isInvalidStateError({ name: 'TypeError' })).toBe(false)
    expect(isInvalidStateError({})).toBe(false)
    expect(isInvalidStateError('InvalidStateError')).toBe(false)
    expect(isInvalidStateError(null)).toBe(false)
    expect(isInvalidStateError(undefined)).toBe(false)
  })
})

describe('startViewTransitionSafe', () => {
  it('falls back to invoking update directly when the API is missing', () => {
    const originalDoc = (globalThis as { document?: unknown }).document
    ;(globalThis as { document: object }).document = {} // no startViewTransition

    const update = vi.fn()
    startViewTransitionSafe(update)
    expect(update).toHaveBeenCalledTimes(1)

    ;(globalThis as { document?: unknown }).document = originalDoc
  })

  it('uses the API when available and swallows InvalidStateError on the finished promise', async () => {
    const originalDoc = (globalThis as { document?: unknown }).document

    const update = vi.fn()
    const startSpy = vi.fn((cb: () => void) => {
      void cb // the real API invokes this; test doesn't need to
      return {
        finished: Promise.reject(new DOMException('superseded', 'InvalidStateError')),
        ready: Promise.resolve(),
        updateCallbackDone: Promise.resolve(),
        skipTransition: () => {},
      }
    })
    ;(globalThis as { document: object }).document = { startViewTransition: startSpy }

    startViewTransitionSafe(update)
    expect(startSpy).toHaveBeenCalledTimes(1)
    // Argument is the update callback (the API itself invokes it).
    expect(startSpy).toHaveBeenCalledWith(update)

    // The unhandled rejection would surface here if we hadn't caught
    // it. Awaiting a microtask yield is enough to flush the promise
    // chain inside startViewTransitionSafe.
    await Promise.resolve()
    await Promise.resolve()

    ;(globalThis as { document?: unknown }).document = originalDoc
  })

  it('only swallows InvalidStateError (verified via the classifier)', () => {
    // Rather than triggering an actual unhandled rejection (which
    // vitest treats as a test failure), pin the contract that
    // startViewTransitionSafe relies on: InvalidStateError is the
    // single name we suppress. Anything else returns false from the
    // classifier and therefore propagates through the promise chain.
    expect(isInvalidStateError(new DOMException('x', 'InvalidStateError'))).toBe(true)
    expect(isInvalidStateError(new Error('something else broke'))).toBe(false)
    expect(isInvalidStateError(new DOMException('x', 'AbortError'))).toBe(false)
    expect(isInvalidStateError(new TypeError('y'))).toBe(false)
  })
})

const WEBKITGTK_UA =
  'Mozilla/5.0 (X11; Ubuntu; Linux x86_64) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/26.0 Safari/605.1.15'

describe('isWebKitGTK', () => {
  it('matches WebKitGTK user agents', () => {
    expect(isWebKitGTK(WEBKITGTK_UA)).toBe(true)
    expect(isWebKitGTK('Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/605.1.15 (KHTML, like Gecko)')).toBe(true)
  })

  it('does not match other engines on Linux, or Safari', () => {
    expect(isWebKitGTK('Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/140.0.0.0 Safari/537.36')).toBe(false)
    expect(isWebKitGTK('Mozilla/5.0 (X11; Linux x86_64; rv:140.0) Gecko/20100101 Firefox/140.0')).toBe(false)
    expect(isWebKitGTK('Mozilla/5.0 (Linux; Android 15) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/140.0.0.0 Mobile Safari/537.36')).toBe(false)
    expect(isWebKitGTK('Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/26.0 Safari/605.1.15')).toBe(false)
  })
})

describe('startViewTransitionSafe under WebKitGTK', () => {
  it('applies the update directly instead of starting a transition', () => {
    const originalDoc = (globalThis as { document?: unknown }).document
    const startSpy = vi.fn()
    ;(globalThis as { document: object }).document = { startViewTransition: startSpy }
    const uaSpy = vi.spyOn(navigator, 'userAgent', 'get').mockReturnValue(WEBKITGTK_UA)

    const update = vi.fn()
    startViewTransitionSafe(update)
    expect(update).toHaveBeenCalledTimes(1)
    expect(startSpy).not.toHaveBeenCalled()

    uaSpy.mockRestore()
    ;(globalThis as { document?: unknown }).document = originalDoc
  })
})
