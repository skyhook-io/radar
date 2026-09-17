/**
 * Keeps a detail view from re-asking for a resource the server has already
 * said does not exist.
 *
 * Any change to a kind invalidates every open detail view of that kind, so on
 * a namespace where pods are churning an open view refetches every couple of
 * seconds. While the object it is showing still exists that is what keeps it
 * live. Once the object is gone, every one of those refetches is a 404 that
 * can never resolve, and it continues for as long as the view stays open.
 *
 * Gone is not always permanent. A StatefulSet replaces a pod under the same
 * name, so this goes quiet for a cooldown rather than forever, and one probe
 * after the cooldown is enough to notice the resource came back.
 */

/**
 * Quiet periods between probes, in ms. A resource that is still missing on the
 * second look is usually gone for good, so the gap widens rather than settling
 * on one interval: a short first wait still catches a StatefulSet pod being
 * rebuilt under the same name, and the cap keeps a long-open view close to
 * free instead of merely cheaper than the storm it replaced.
 */
export const GONE_BACKOFF_MS = [30_000, 60_000, 120_000, 300_000]

export interface GoneState {
  /** Epoch ms until which refetching is suppressed, or null when live. */
  suppressedUntil: number | null
  /** Consecutive times the server has said this resource is gone. */
  strikes: number
}

export const initialGoneState: GoneState = { suppressedUntil: null, strikes: 0 }

export function goneBackoffMs(strikes: number): number {
  return GONE_BACKOFF_MS[Math.min(strikes, GONE_BACKOFF_MS.length - 1)]
}

export type GoneEvent =
  /** A fetch settled. `isGone` is true only for a definite 404. */
  | { type: 'settled'; isGone: boolean; now: number }
  /** The view switched to a different object; the previous answer is void. */
  | { type: 'target-changed' }

export function nextGoneState(prev: GoneState, event: GoneEvent): GoneState {
  if (event.type === 'target-changed') return initialGoneState
  if (!event.isGone) return initialGoneState
  return { suppressedUntil: event.now + goneBackoffMs(prev.strikes), strikes: prev.strikes + 1 }
}

/**
 * Ends the quiet period so the query can probe once. The strike count carries
 * over: clearing it here would restart the backoff on every probe and pin the
 * view to the shortest interval forever.
 */
export function releaseForProbe(state: GoneState): GoneState {
  return { suppressedUntil: null, strikes: state.strikes }
}

export function isSuppressed(state: GoneState, now: number): boolean {
  return state.suppressedUntil !== null && now < state.suppressedUntil
}
