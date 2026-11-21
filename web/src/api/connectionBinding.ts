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

/** Record the connection named by a status the tab received (poll, SSE frame, retry result). */
export function observeConnectionStatus(status: { connectionId?: string }, base = getApiBase()): void {
  if (status.connectionId) ids.set(base, status.connectionId)
}

/** The connection ID to send, or undefined when the server publishes none. */
export function connectionHeaderValue(base = getApiBase()): string | undefined {
  return ids.get(base)
}

/** A WebSocket URL naming this tab's connection (a browser can't set headers on one). */
export function withConnectionParam(url: string, base = getApiBase()): string {
  const id = ids.get(base)
  if (!id) return url
  return `${url}${url.includes('?') ? '&' : '?'}connection=${encodeURIComponent(id)}`
}

/** Test hook: forget everything. */
export function resetConnectionBindingForTest(): void {
  ids.clear()
}
