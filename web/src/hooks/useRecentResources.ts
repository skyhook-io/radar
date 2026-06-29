// Recently-opened resources, persisted to localStorage so the omnibar's empty
// state can offer "jump back to what I was just looking at" across reloads.
// Plain functions (not a stateful hook): the omnibar reads fresh each time it
// opens and writes on open, so two component instances never need to sync.

export interface RecentResource {
  kind: string
  group?: string
  namespace?: string
  name: string
}

const KEY = 'radar-recent-resources'
const MAX = 7

// Partition by the current cluster/context: a resource is identified by
// (kind, ns, name) WITHIN a cluster, so a global store would surface the prior
// cluster's names after a context switch and open ns/name in the wrong cluster.
// An UNKNOWN context (key still loading) is a hard no-op — no shared fallback
// bucket — so a recent is never read or written against an indeterminate cluster.
function storageKey(contextKey: string): string {
  return `${KEY}::${contextKey}`
}

function keyOf(r: RecentResource): string {
  return `${r.kind}\x00${r.group || ''}\x00${r.namespace || ''}\x00${r.name}`
}

export function loadRecentResources(contextKey: string): RecentResource[] {
  if (!contextKey) return []
  try {
    const raw = localStorage.getItem(storageKey(contextKey))
    if (raw) return JSON.parse(raw)
  } catch {
    // ignore parse/storage errors — recents are best-effort
  }
  return []
}

export function recordRecentResource(r: RecentResource, contextKey: string): void {
  if (!r.name || !r.kind || !contextKey) return
  try {
    const k = keyOf(r)
    const next = [r, ...loadRecentResources(contextKey).filter((x) => keyOf(x) !== k)].slice(0, MAX)
    localStorage.setItem(storageKey(contextKey), JSON.stringify(next))
  } catch {
    // ignore storage errors
  }
}
