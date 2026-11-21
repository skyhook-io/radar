import { useSyncExternalStore } from 'react'
import { getApiBase } from './config'

// Which cluster connection this tab's views come from, per API base.
//
// The server admits a connection (an ID unique across restarts) each time it
// starts serving a cluster, and retires it at the point of no return of a
// switch, rescope or rebuild. The tab sends the ID it is showing on every
// request: the server refuses writes for another connection before any
// Kubernetes work, and labels reads with the connection that served them so
// the tab can drop data from a connection it isn't showing.
//
// The tab adopts a new ID only together with resetting its views: when the
// views were already reset for a switch (context_changed), the next ID is
// adopted quietly; otherwise adopting it triggers the reset.

export const CONNECTION_HEADER = 'X-Radar-Connection'
const NO_CONNECTION = 'none'
export const CONTEXT_CHANGED = 'context_changed'
export const CLUSTER_CHANGING = 'cluster_changing'

export interface ConnectionIdentity {
  connectionId?: string
  seq?: number
  bootAt?: string
  context?: string
  contextSwitch?: { id?: string; phase?: string } | null
}

interface BaseState {
  id?: string
  seq: number
  bootAt: string
  supported: boolean
  viewsAwaitingConnection: boolean
  // A connection that served data this tab accepted before it had adopted
  // any connection (its first status hadn't arrived yet).
  unboundStamp?: string
  // Bumped each time a new connection replaces one the tab had: views keyed
  // on it remount, which drops dialogs opened on the previous connection.
  generation: number
}

const states = new Map<string, BaseState>()
const listeners = new Set<() => void>()
let onReplaced: ((context: string, switchId?: string) => void) | undefined
let onMismatch: (() => void) | undefined
let mismatchScheduled = false

function stateFor(base: string): BaseState {
  let s = states.get(base)
  if (!s) {
    s = { seq: 0, bootAt: '', supported: false, viewsAwaitingConnection: false, generation: 0 }
    states.set(base, s)
  }
  return s
}

/** App registers the reset to run when the tab learns of a new connection it hadn't reset for. */
export function setConnectionReplacedHandler(fn: ((context: string, switchId?: string) => void) | undefined): void {
  onReplaced = fn
}

/** ConnectionContext registers a status refresh to run when a response came from another connection. */
export function setConnectionMismatchHandler(fn: (() => void) | undefined): void {
  onMismatch = fn
}

/**
 * Feed every connection status the tab receives (poll, SSE frame, initial
 * snapshot, retry result). Returns false for a publication older than one
 * already seen (within one server process by sequence number, across restarts
 * by the process start time): the caller must not display it either.
 */
export function observeConnectionStatus(status: ConnectionIdentity, base = getApiBase()): boolean {
  if (status.seq === undefined || !status.bootAt) return true // a server that doesn't bind connections
  const s = stateFor(base)
  if (s.bootAt && status.bootAt < s.bootAt) return false
  if (status.bootAt === s.bootAt && status.seq < s.seq) return false
  if (status.bootAt === s.bootAt && status.seq === s.seq) return true
  s.bootAt = status.bootAt
  s.seq = status.seq
  s.supported = true
  const next = status.connectionId
  if (!next || next === s.id) return true
  // Data accepted before the first adoption counts as showing that connection.
  const hadConnection = s.id !== undefined || (s.unboundStamp !== undefined && s.unboundStamp !== next)
  const replaced = hadConnection && !s.viewsAwaitingConnection
  s.id = next
  s.unboundStamp = undefined
  s.viewsAwaitingConnection = false
  if (hadConnection) {
    s.generation += 1
    listeners.forEach((l) => l())
  }
  // A completed switch's ID lets the reset dedupe against the switch's own.
  const switchId = status.contextSwitch?.phase === 'succeeded' ? status.contextSwitch.id : undefined
  // Only the mounted API base's views are reset; another base's late reply isn't theirs.
  if (replaced && base === getApiBase()) onReplaced?.(status.context ?? '', switchId)
  return true
}

/** Changes each time a new connection replaces the one this tab showed; key views on it. */
export function useConnectionGeneration(base = getApiBase()): number {
  return useSyncExternalStore(
    (listener) => {
      listeners.add(listener)
      return () => listeners.delete(listener)
    },
    () => states.get(base)?.generation ?? 0,
  )
}

/** The views were just reset for a switch: the next connection is adopted without another reset. */
export function markViewsAwaitingConnection(base = getApiBase()): void {
  stateFor(base).viewsAwaitingConnection = true
}

/** The connection ID to send, "unknown" when the server binds but the tab has none yet, or undefined. */
export function connectionHeaderValue(base = getApiBase()): string | undefined {
  const s = states.get(base)
  if (!s?.supported) return undefined
  return s.id ?? 'unknown'
}

/**
 * Whether a response stamped `stamp` may reach the views of a tab that sent
 * `sent`. Unlabelled responses always may. "none" (no connection served it)
 * drops data for a tab that shows a connection but lets errors through, so a
 * refusal keeps its own code. A tab
 * with no connection yet accepts any connection's data and remembers it, so
 * adopting a different first connection still resets the views.
 */
export function responseMatchesConnection(sent: string | undefined, stamp: string | null, ok = true, base = getApiBase()): boolean {
  if (stamp === null) return true
  // Before its first connection (startup warm-up) a tab takes what is served.
  if (stamp === NO_CONNECTION) return !ok || !sent || sent === 'unknown'
  if (!sent || sent === 'unknown') {
    const s = stateFor(base)
    if (s.id === undefined) {
      s.unboundStamp = stamp
      return true
    }
    return stamp === s.id
  }
  return stamp === sent
}

/** Whether a labelled result may still be committed: the tab hasn't moved to another connection since. */
export function stampStillCurrent(stamp: string | null, base = getApiBase()): boolean {
  if (stamp === null || stamp === NO_CONNECTION) return true
  const id = states.get(base)?.id
  return id === undefined || id === stamp
}

/** A response came from another connection: refresh the status once, so the tab can catch up. */
export function reportConnectionMismatch(): void {
  if (mismatchScheduled) return
  mismatchScheduled = true
  queueMicrotask(() => {
    mismatchScheduled = false
    onMismatch?.()
  })
}

/** Errors that mean "the cluster changed under this view": retrying the same request can't help. */
export function isConnectionChangeCode(code: unknown): boolean {
  return code === CONTEXT_CHANGED || code === CLUSTER_CHANGING
}

/** Test hook: forget everything. */
export function resetConnectionBindingForTest(): void {
  states.clear()
  listeners.clear()
  onReplaced = undefined
  onMismatch = undefined
  mismatchScheduled = false
}
