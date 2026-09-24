// What one browser remembers about the poll. For local Radar the server
// also keeps state; for in-cluster Radar this is the only place, because the
// pod's home directory is shared by every viewer and resets on restart.

export const COOLDOWN_MS = 90 * 24 * 60 * 60 * 1000
// Separate days on which Radar was opened in this browser. Mileage, not a
// launch count: five launches can happen in one afternoon.
export const MIN_ACTIVE_DAYS = 5

export interface BrowserPollState {
  activeDays: number
  lastActiveDay?: string
  lastShownAt?: number
  never?: boolean
  submittedRound?: string
}

type StorageLike = Pick<Storage, 'getItem' | 'setItem'>

const KEY_PREFIX = 'radar.poll'

function keyFor(apiBase: string) {
  return `${KEY_PREFIX}:${apiBase}`
}

export function readPollState(storage: StorageLike, apiBase: string): BrowserPollState | null {
  try {
    const raw = storage.getItem(keyFor(apiBase))
    if (!raw) return { activeDays: 0 }
    const parsed = JSON.parse(raw)
    return typeof parsed === 'object' && parsed ? { activeDays: 0, ...parsed } : { activeDays: 0 }
  } catch {
    return null
  }
}

export function writePollState(storage: StorageLike, apiBase: string, state: BrowserPollState): void {
  try {
    storage.setItem(keyFor(apiBase), JSON.stringify(state))
  } catch {
    // Storage denied: the poll simply never shows here.
  }
}

function localDay(now: number): string {
  const d = new Date(now)
  return `${d.getFullYear()}-${d.getMonth() + 1}-${d.getDate()}`
}

/** Counts today as a day of use, once. */
export function recordActiveDay(state: BrowserPollState, now: number): BrowserPollState {
  const day = localDay(now)
  if (state.lastActiveDay === day) return state
  return { ...state, activeDays: state.activeDays + 1, lastActiveDay: day }
}

/**
 * Applies a change on top of what storage holds now, not on top of what this
 * tab read when it loaded. Another tab may have recorded "Don't ask again" in
 * the meantime, and a stale write must not erase it.
 */
export function mergePollState(storage: StorageLike, apiBase: string, patch: Partial<BrowserPollState>): BrowserPollState | null {
  const fresh = readPollState(storage, apiBase)
  if (!fresh) return null
  const next = { ...fresh, ...patch, never: fresh.never || patch.never }
  writePollState(storage, apiBase, next)
  return next
}

export function browserAllows(state: BrowserPollState | null, roundId: string, now: number): boolean {
  if (!state) return false
  if (state.never) return false
  if (state.submittedRound === roundId) return false
  if (state.lastShownAt && now - state.lastShownAt < COOLDOWN_MS) return false
  if (state.activeDays < MIN_ACTIVE_DAYS) return false
  return true
}
