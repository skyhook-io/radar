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
}

interface BaseState {
  id?: string
  seq: number
  bootAt: string
  supported: boolean
  viewsAwaitingConnection: boolean
  // Bumped each time a new connection replaces one the tab had: views keyed
  // on it remount, which drops dialogs opened on the previous connection.
  generation: number
}

const states = new Map<string, BaseState>()
const listeners = new Set<() => void>()
let onReplaced: ((context: string, connectionId: string) => void) | undefined
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
export function setConnectionReplacedHandler(fn: ((context: string, connectionId: string) => void) | undefined): void {
  onReplaced = fn
}

/** ConnectionContext registers a status refresh to run when a response came from another connection. */
export function setConnectionMismatchHandler(fn: (() => void) | undefined): void {
  onMismatch = fn
}

/**
 * Feed every connection status the tab receives (poll, SSE frame, initial
 * snapshot, retry result). Older publications are ignored: within one server
 * process by sequence number, across restarts by the process start time.
 */
export function observeConnectionStatus(status: ConnectionIdentity, base = getApiBase()): void {
  if (status.seq === undefined || !status.bootAt) return // a server that doesn't bind connections
  const s = stateFor(base)
  if (s.bootAt && status.bootAt < s.bootAt) return
  if (status.bootAt === s.bootAt && status.seq <= s.seq) return
  s.bootAt = status.bootAt
  s.seq = status.seq
  s.supported = true
  const next = status.connectionId
  if (!next || next === s.id) return
  const hadConnection = s.id !== undefined
  const replaced = hadConnection && !s.viewsAwaitingConnection
  s.id = next
  s.viewsAwaitingConnection = false
  if (hadConnection) {
    s.generation += 1
    listeners.forEach((l) => l())
  }
  if (replaced) onReplaced?.(status.context ?? '', next)
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

/** Whether a response stamped `stamp` belongs to the connection `sent` named. */
export function responseMatchesConnection(sent: string | undefined, stamp: string | null): boolean {
  if (!sent || sent === 'unknown' || stamp === null) return true
  return stamp !== NO_CONNECTION && stamp === sent
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
