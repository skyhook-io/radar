import type { QueryClient } from '@tanstack/react-query'

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
 * name, so this goes quiet for a cooldown rather than forever. A watch event
 * that names the object ends the quiet period at once; the cooldown probe is
 * the fallback for when no such event arrives.
 */

/**
 * Quiet periods between probes, in ms. A resource that is still missing on the
 * second look is usually gone for good, so the gap widens rather than settling
 * on one interval: a short first wait still catches a resource rebuilt under
 * the same name when its watch event was missed, and the cap keeps a view
 * left open overnight to about one request every five minutes.
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

/** A fetch settled. `isGone` is true only for a definite 404. */
export interface SettledFetch {
  isGone: boolean
  now: number
}

export function nextGoneState(prev: GoneState, fetch: SettledFetch): GoneState {
  if (!fetch.isGone) return initialGoneState
  return { suppressedUntil: fetch.now + goneBackoffMs(prev.strikes), strikes: prev.strikes + 1 }
}

/**
 * Ends the quiet period so the query can probe once. The strike count carries
 * over: clearing it here would restart the backoff on every probe and pin the
 * view to the shortest interval forever.
 */
export function releaseForProbe(state: GoneState): GoneState {
  return { suppressedUntil: null, strikes: state.strikes }
}

/**
 * Gone state for one view, tagged with the target it describes and the last
 * fetch it has counted.
 */
export interface TrackedGone {
  identity: string
  /** When the last counted fetch settled (epoch ms), 0 before any. */
  settledAt: number
  /** The cluster has reported the object since that fetch settled. */
  presentSinceSettle: boolean
  gone: GoneState
}

/**
 * The state for `identity`: `prev` itself when it already describes that
 * target, otherwise a clean start, so a view that switches target never
 * inherits the previous target's quiet period.
 */
export function trackedFor(prev: TrackedGone, identity: string): TrackedGone {
  return prev.identity === identity ? prev : { identity, settledAt: 0, presentSinceSettle: false, gone: initialGoneState }
}

/**
 * Counts a fetch that settled at `settledAt`, at most once. A refetch of a
 * query with no data clears its error while in flight, so reading the error
 * on every render would count that as the resource coming back and keep the
 * backoff on its shortest step.
 *
 * A 404 that settles after the cluster reported the object was answered
 * before the object came back, so it starts no quiet period. The event that
 * reported it also refetches the kind, and that fetch finds it.
 */
export function observeSettled(prev: TrackedGone, settledAt: number, fetch: SettledFetch): TrackedGone {
  if (settledAt === 0 || settledAt === prev.settledAt) return prev
  if (fetch.isGone && prev.presentSinceSettle) return { ...prev, settledAt, presentSinceSettle: false }
  return { ...prev, settledAt, presentSinceSettle: false, gone: nextGoneState(prev.gone, fetch) }
}

/** The cluster says the object exists: end any quiet period and backoff. */
export function markPresent(prev: TrackedGone): TrackedGone {
  const live = prev.gone.suppressedUntil === null && prev.gone.strikes === 0
  if (live && prev.presentSinceSettle) return prev
  return { ...prev, presentSinceSettle: true, gone: initialGoneState }
}

/**
 * An object the cluster has just reported as existing. `kind` is the plural
 * resource name and `group` the canonical API group, '' for core.
 */
export interface PresentResource {
  kind: string
  group: string
  namespace: string
  name: string
}

type PresenceListener = (resource: PresentResource) => void

// Keyed by QueryClient so two Radar instances on one page never wake each
// other's views.
const presenceListeners = new WeakMap<QueryClient, Set<PresenceListener>>()

/** Tells every view of this object that it exists, ending any quiet period. */
export function announceResourcePresent(client: QueryClient, resource: PresentResource): void {
  presenceListeners.get(client)?.forEach((listener) => listener(resource))
}

/** Subscribes to {@link announceResourcePresent}. Returns the unsubscribe. */
export function onResourcePresent(client: QueryClient, listener: PresenceListener): () => void {
  let listeners = presenceListeners.get(client)
  if (!listeners) {
    listeners = new Set()
    presenceListeners.set(client, listeners)
  }
  listeners.add(listener)
  return () => {
    listeners.delete(listener)
  }
}
