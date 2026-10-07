import { QueryClient } from '@tanstack/react-query'
import { describe, expect, it, vi } from 'vitest'

import {
  GONE_BACKOFF_MS,
  announceResourcePresent,
  goneBackoffMs,
  initialGoneState,
  markPresent,
  nextGoneState,
  observeSettled,
  onResourcePresent,
  releaseForProbe,
  trackedFor,
  type TrackedGone,
} from './gone-suppression'

const T0 = 1_700_000_000_000

describe('nextGoneState', () => {
  it('stays live while the resource is there', () => {
    const s = nextGoneState(initialGoneState, { isGone: false, now: T0 })
    expect(s.suppressedUntil).toBeNull()
  })

  it('goes quiet for the first cooldown once the server says the resource is gone', () => {
    // The prod shape: an open view on a pod that was rescheduled away, kept
    // refetching by unrelated sibling churn, 404 every time.
    const s = nextGoneState(initialGoneState, { isGone: true, now: T0 })
    expect(s.suppressedUntil).toBe(T0 + GONE_BACKOFF_MS[0])
  })

  it('waits longer each time the probe finds it still gone', () => {
    // A view left open on a deleted pod should cost close to nothing, not a
    // steady request every 30 seconds.
    let state = initialGoneState
    let now = T0
    const waits: number[] = []
    for (let i = 0; i < 5; i++) {
      state = nextGoneState(state, { isGone: true, now })
      waits.push((state.suppressedUntil as number) - now)
      now = state.suppressedUntil as number
    }
    expect(waits).toEqual([...GONE_BACKOFF_MS, GONE_BACKOFF_MS[GONE_BACKOFF_MS.length - 1]])
  })

  it('caps the wait rather than growing without bound', () => {
    expect(goneBackoffMs(99)).toBe(GONE_BACKOFF_MS[GONE_BACKOFF_MS.length - 1])
  })

  it('starts the backoff over once the resource comes back', () => {
    let state = nextGoneState(initialGoneState, { isGone: true, now: T0 })
    state = nextGoneState(state, { isGone: true, now: T0 + 1 })
    const back = nextGoneState(state, { isGone: false, now: T0 + 2 })
    expect(back).toEqual(initialGoneState)
    const goneAgain = nextGoneState(back, { isGone: true, now: T0 + 3 })
    expect((goneAgain.suppressedUntil as number) - (T0 + 3)).toBe(GONE_BACKOFF_MS[0])
  })

  it('suppresses only a definite gone, never a transient failure', () => {
    // isGone is set from a 404 alone. A 403, a 500 or a dropped connection
    // must keep retrying, so they arrive here as isGone false.
    const s = nextGoneState(initialGoneState, { isGone: false, now: T0 })
    expect(s).toEqual(initialGoneState)
  })
})

describe('releaseForProbe', () => {
  it('ends the quiet period without restarting the backoff', () => {
    let state = nextGoneState(initialGoneState, { isGone: true, now: T0 })
    state = releaseForProbe(state)
    expect(state.suppressedUntil).toBeNull()

    const afterProbe = nextGoneState(state, { isGone: true, now: T0 })
    expect((afterProbe.suppressedUntil as number) - T0).toBe(GONE_BACKOFF_MS[1])
  })
})

const fresh = (identity: string): TrackedGone => ({ identity, settledAt: 0, presentSinceSettle: false, gone: initialGoneState })

describe('trackedFor', () => {
  it('never inherits the previous target quiet period', () => {
    const quiet = observeSettled(fresh('pods/a/old/'), T0, { isGone: true, now: T0 })
    expect(quiet.gone.suppressedUntil).not.toBeNull()
    expect(trackedFor(quiet, 'pods/a/new/')).toEqual(fresh('pods/a/new/'))
  })

  it('keeps the same object for the same target, so nothing re-renders', () => {
    const state = fresh('pods/a/x/')
    expect(trackedFor(state, 'pods/a/x/')).toBe(state)
  })
})

describe('observeSettled', () => {
  it('counts each settled fetch once', () => {
    // Effects run twice in development and re-run on unrelated renders; the
    // same settle must not add a strike each time.
    const once = observeSettled(fresh('k'), T0, { isGone: true, now: T0 })
    const twice = observeSettled(once, T0, { isGone: true, now: T0 + 5 })
    expect(twice).toBe(once)
    expect(twice.gone.strikes).toBe(1)
  })

  it('widens the backoff on a deep link to a resource that never existed', () => {
    // No data is ever cached, so each probe clears the error while it is in
    // flight. That render carries the old settle time and no error; counting
    // it as "not gone" would pin every probe to the first cooldown.
    let state = observeSettled(fresh('k'), T0, { isGone: true, now: T0 })
    const waits: number[] = []
    for (let i = 0; i < 3; i++) {
      const now = state.gone.suppressedUntil as number
      state = { ...state, gone: releaseForProbe(state.gone) }
      state = observeSettled(state, state.settledAt, { isGone: false, now })
      state = observeSettled(state, now + 50, { isGone: true, now: now + 50 })
      waits.push((state.gone.suppressedUntil as number) - (now + 50))
    }
    expect(waits).toEqual([GONE_BACKOFF_MS[1], GONE_BACKOFF_MS[2], GONE_BACKOFF_MS[3]])
  })

  it('ignores a view that has never settled a fetch', () => {
    const state = fresh('k')
    expect(observeSettled(state, 0, { isGone: false, now: T0 })).toBe(state)
  })
})

describe('markPresent', () => {
  it('ends the quiet period and the backoff at once', () => {
    let state = observeSettled(fresh('k'), T0, { isGone: true, now: T0 })
    state = observeSettled(state, T0 + 1, { isGone: true, now: T0 + 1 })
    const back = markPresent(state)
    expect(back.gone).toEqual(initialGoneState)
    expect(back.settledAt).toBe(state.settledAt)
  })

  it('changes a live view once per fetch, so routine events cause no render', () => {
    const marked = markPresent(fresh('k'))
    expect(markPresent(marked)).toBe(marked)
  })

  it('does not let a 404 answered before the object came back quiet the view', () => {
    // A fetch in flight across a delete and recreate can land its 404 after
    // the recreate event. That event already refetches the kind; going quiet
    // would turn that refetch away and hide the object until the next probe.
    const marked = markPresent(fresh('k'))
    const stale = observeSettled(marked, T0, { isGone: true, now: T0 })
    expect(stale.gone).toEqual(initialGoneState)
    expect(stale.presentSinceSettle).toBe(false)

    // The next 404 is the server's current answer and quiets as usual.
    const current = observeSettled(stale, T0 + 1, { isGone: true, now: T0 + 1 })
    expect(current.gone.suppressedUntil).toBe(T0 + 1 + GONE_BACKOFF_MS[0])
  })
})

describe('resource presence', () => {
  const pod = { kind: 'pods', group: '', namespace: 'a', name: 'x' }

  it('reaches every listener on the same client until it unsubscribes', () => {
    const client = new QueryClient()
    const listener = vi.fn()
    const unsubscribe = onResourcePresent(client, listener)
    announceResourcePresent(client, pod)
    expect(listener).toHaveBeenCalledWith(pod)
    unsubscribe()
    announceResourcePresent(client, pod)
    expect(listener).toHaveBeenCalledTimes(1)
  })

  it('stays within one client', () => {
    // Two embedded Radar instances watch different clusters; a pod named the
    // same in the other cluster says nothing about this one.
    const mine = new QueryClient()
    const listener = vi.fn()
    onResourcePresent(mine, listener)
    announceResourcePresent(new QueryClient(), pod)
    expect(listener).not.toHaveBeenCalled()
  })
})
