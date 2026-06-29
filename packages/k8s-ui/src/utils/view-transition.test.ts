import { describe, it, expect, vi } from 'vitest'
import { isInvalidStateError, startViewTransitionSafe } from './view-transition'

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
