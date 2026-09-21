import { describe, expect, it } from 'vitest'

import {
  GONE_BACKOFF_MS,
  goneBackoffMs,
  nextGoneState,
  initialGoneState,
  isSuppressed,
  releaseForProbe,
} from './gone-suppression'

const T0 = 1_700_000_000_000

describe('nextGoneState', () => {
  it('stays live while the resource is there', () => {
    const s = nextGoneState(initialGoneState, { type: 'settled', isGone: false, now: T0 })
    expect(isSuppressed(s, T0)).toBe(false)
  })

  it('goes quiet once the server says the resource is gone', () => {
    // The prod shape: an open view on a pod that was rescheduled away, kept
    // refetching by unrelated sibling churn, 404 every time.
    const s = nextGoneState(initialGoneState, { type: 'settled', isGone: true, now: T0 })
    expect(isSuppressed(s, T0)).toBe(true)
    expect(isSuppressed(s, T0 + GONE_BACKOFF_MS[0] - 1)).toBe(true)
  })

  it('probes again after the cooldown, so a rebuilt resource is picked up', () => {
    // A StatefulSet replaces a pod under the same name. Quiet forever would
    // leave the view permanently wrong.
    const s = nextGoneState(initialGoneState, { type: 'settled', isGone: true, now: T0 })
    expect(isSuppressed(s, T0 + GONE_BACKOFF_MS[0])).toBe(false)
  })

  it('goes live again the moment the resource comes back', () => {
    const back = nextGoneState(initialGoneState, { type: 'settled', isGone: false, now: T0 + GONE_BACKOFF_MS[0] })
    expect(isSuppressed(back, T0 + GONE_BACKOFF_MS[0])).toBe(false)
  })

  it('waits longer each time the probe finds it still gone', () => {
    // A view left open on a deleted pod should approach free, not settle on a
    // rate comparable to the storm this replaced.
    let state = initialGoneState
    let now = T0
    const waits: number[] = []
    for (let i = 0; i < 5; i++) {
      state = nextGoneState(state, { type: 'settled', isGone: true, now })
      waits.push((state.suppressedUntil as number) - now)
      now = state.suppressedUntil as number
    }
    expect(waits).toEqual([...GONE_BACKOFF_MS, GONE_BACKOFF_MS[GONE_BACKOFF_MS.length - 1]])
  })

  it('caps the wait rather than growing without bound', () => {
    expect(goneBackoffMs(99)).toBe(GONE_BACKOFF_MS[GONE_BACKOFF_MS.length - 1])
  })

  it('starts the backoff over once the resource comes back', () => {
    let state = nextGoneState(initialGoneState, { type: 'settled', isGone: true, now: T0 })
    state = nextGoneState(state, { type: 'settled', isGone: true, now: T0 + 1 })
    const back = nextGoneState(state, { type: 'settled', isGone: false, now: T0 + 2 })
    expect(back).toEqual(initialGoneState)
    const goneAgain = nextGoneState(back, { type: 'settled', isGone: true, now: T0 + 3 })
    expect((goneAgain.suppressedUntil as number) - (T0 + 3)).toBe(GONE_BACKOFF_MS[0])
  })

  it('never inherits the previous object answer when the view switches target', () => {
    const switched = nextGoneState(initialGoneState, { type: 'target-changed' })
    expect(isSuppressed(switched, T0)).toBe(false)
  })

  it('suppresses only a definite gone, never a transient failure', () => {
    // isGone is set from a 404 alone. A 403, a 500 or a dropped connection
    // must keep retrying, so they arrive here as isGone false.
    const s = nextGoneState(initialGoneState, { type: 'settled', isGone: false, now: T0 })
    expect(s).toEqual(initialGoneState)
  })
})

describe('releaseForProbe', () => {
  it('ends the quiet period without restarting the backoff', () => {
    // Clearing strikes here would pin every probe to the shortest interval,
    // which is how the backoff silently flattened to a fixed 30s.
    let state = nextGoneState(initialGoneState, { type: 'settled', isGone: true, now: T0 })
    state = releaseForProbe(state)
    expect(isSuppressed(state, T0)).toBe(false)

    const afterProbe = nextGoneState(state, { type: 'settled', isGone: true, now: T0 })
    expect((afterProbe.suppressedUntil as number) - T0).toBe(GONE_BACKOFF_MS[1])
  })
})
