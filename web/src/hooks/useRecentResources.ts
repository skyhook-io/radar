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

function keyOf(r: RecentResource): string {
  return `${r.kind}\x00${r.group || ''}\x00${r.namespace || ''}\x00${r.name}`
}

export function loadRecentResources(): RecentResource[] {
  try {
    const raw = localStorage.getItem(KEY)
    if (raw) return JSON.parse(raw)
  } catch {
    // ignore parse/storage errors — recents are best-effort
  }
  return []
}

export function recordRecentResource(r: RecentResource): void {
  if (!r.name || !r.kind) return
  try {
    const k = keyOf(r)
    const next = [r, ...loadRecentResources().filter((x) => keyOf(x) !== k)].slice(0, MAX)
    localStorage.setItem(KEY, JSON.stringify(next))
  } catch {
    // ignore storage errors
  }
}
