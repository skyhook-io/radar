import { useState, useSyncExternalStore } from 'react'
import { getApiBase } from './config'

// Which cluster connection this tab is on, per API base.
//
// A Radar serving from a kubeconfig gives each connection an ID when it starts
// serving a cluster (unique across switches and restarts) and publishes it
// with the connection status. The tab keeps the latest ID it heard and sends
// it with every request; the server refuses a write naming a connection that
// is no longer serving. When the cluster changes, the tab's views reset on the
// context change itself (context_changed, or a status poll showing another
// context), so the ID only has to follow the latest status.

export const CONNECTION_HEADER = 'X-Radar-Connection'

const ids = new Map<string, string>()
const listeners = new Set<() => void>()

function subscribe(listener: () => void) {
  listeners.add(listener)
  return () => { listeners.delete(listener) }
}

/** Record the connection named by a status the tab received (poll, SSE frame, retry result). */
export function observeConnectionStatus(status: { connectionId?: string }, base = getApiBase()): void {
  if (!status.connectionId || ids.get(base) === status.connectionId) return
  ids.set(base, status.connectionId)
  listeners.forEach(listener => listener())
}

/** The connection ID to send, or undefined when the server publishes none. */
export function connectionHeaderValue(base = getApiBase()): string | undefined {
  return ids.get(base)
}

export function useCurrentConnectionId(base = getApiBase()): string | undefined {
  return useSyncExternalStore(subscribe, () => connectionHeaderValue(base), () => undefined)
}

/** A WebSocket URL naming a connection (a browser can't set headers on one); by default this tab's latest. */
export function withConnectionParam(url: string, id = connectionHeaderValue()): string {
  if (!id) return url
  return `${url}${url.includes('?') ? '&' : '?'}connection=${encodeURIComponent(id)}`
}

/** Headers naming a connection, for a request that must not follow the tab's latest one. */
export function connectionHeaders(id: string | undefined): Record<string, string> {
  return id ? { [CONNECTION_HEADER]: id } : {}
}

/**
 * The connection a component opened on, kept for its lifetime. A dock tab
 * outlives a switch, so its reconnects and actions name the cluster it was
 * opened on and are refused once that connection retires.
 */
export function useOpenedConnection(): string | undefined {
  const [id] = useState(() => connectionHeaderValue())
  return id
}

/** Test hook: forget everything. */
export function resetConnectionBindingForTest(): void {
  ids.clear()
}
