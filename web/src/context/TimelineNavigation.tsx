import { createContext, useContext } from 'react'
import type { IssueTimelineEventTarget, IssueTimelineLinks, TimeRange } from '@skyhook-io/k8s-ui'

/**
 * Opens the Timeline on one event. The app shell provides it because opening
 * the Timeline in a namespace moves the app's namespace scope, the URL and the
 * server's pick together. Absent outside the shell, where change rows keep
 * their link to the changed object.
 */
const TimelineNavigationContext = createContext<Required<Pick<IssueTimelineLinks, 'onTimelineEventClick' | 'canOpenTimelineEvent'>> | undefined>(undefined)

export const TimelineNavigationProvider = TimelineNavigationContext.Provider

/** The change-row Timeline links the app shell offers, or undefined outside it. */
export function useTimelineEventLinks(): IssueTimelineLinks | undefined {
  return useContext(TimelineNavigationContext)
}

// How much of the Timeline around the event the link shows. The range is
// frozen there, so an event older than the Timeline's default last hour is
// still in view.
const EVENT_CONTEXT_MS = 30 * 60 * 1000

const HOUR_MS = 60 * 60 * 1000
// The list's own range choices, narrowest first, at or above its 1h default;
// past them it offers only 'all'.
const LIST_RANGES: [TimeRange, number][] = [['1h', HOUR_MS], ['6h', 6 * HOUR_MS], ['24h', 24 * HOUR_MS]]

/** The /timeline query that opens target's event: the event id and a frozen
 *  range around it, which the scrubber and the swimlane use. A list without a
 *  scrubber (a cluster too large for one without a namespace filter) keeps its
 *  own range instead, so `time` names the narrowest of its ranges that reaches
 *  back to the event. Namespace scope is the caller's (see openTimelineEventIn). */
export function timelineEventSearch(target: IssueTimelineEventTarget, now = Date.now()): URLSearchParams {
  const params = new URLSearchParams({ event: target.eventId })
  const at = Date.parse(target.timestamp)
  if (Number.isFinite(at)) {
    const to = Math.min(now, at + EVENT_CONTEXT_MS)
    const from = Math.min(at, to) - EVENT_CONTEXT_MS
    params.set('from', String(from))
    params.set('to', String(to))
    params.set('time', LIST_RANGES.find(([, ms]) => ms >= now - from)?.[0] ?? 'all')
  }
  return params
}

/** What opening the Timeline in a namespace scope needs from the app shell. */
export interface TimelineScopeControls {
  /** The namespaces the app is scoped to now; empty is all. */
  currentNamespaces: string[]
  /** The cache itself is scoped to namespaces (--namespace-scope), so the
   *  view can't widen to all of them. */
  cacheScoped: boolean
  /** The app's own scope state. */
  setNamespaces: (namespaces: string[]) => void
  /** Records the pick with the server. It can fail (Radar Hub's proxy passes
   *  GET only), so nothing waits on it. */
  requestNamespaces: (namespaces: string[]) => void
  navigate: (search: URLSearchParams) => void
}

/** Whether the Timeline can show target's event: a cluster-scoped event shows
 *  only with no namespace filter, which a namespace-scoped cache can't drop. */
export function canOpenTimelineEvent(target: IssueTimelineEventTarget, cacheScoped: boolean): boolean {
  return Boolean(target.namespace) || !cacheScoped
}

/** Opens the Timeline on target's event in the event's namespace, or in all
 *  namespaces for a cluster-scoped event. Scope state and URL move together,
 *  as the Timeline's own namespace prompt does: the URL-write effect would
 *  otherwise restore the previous scope over the destination's `namespaces`
 *  while the URL-read effect applies it, and the two would alternate. The
 *  server's pick is requested alongside, never awaited. Only a Radar that
 *  reports change event ids gets here, and such a Radar never applies a
 *  stored pick to a read that names no namespace. */
export function openTimelineEventIn(target: IssueTimelineEventTarget, controls: TimelineScopeControls, now = Date.now()) {
  const params = timelineEventSearch(target, now)
  const scope = target.namespace ? [target.namespace] : []
  if (target.namespace) params.set('namespaces', target.namespace)
  const unchanged = scope.length === controls.currentNamespaces.length && scope.every((ns, i) => ns === controls.currentNamespaces[i])
  if (!unchanged) {
    controls.setNamespaces(scope)
    controls.requestNamespaces(scope)
  }
  controls.navigate(params)
}
