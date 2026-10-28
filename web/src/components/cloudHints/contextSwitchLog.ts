// Kept free of API imports so the context-switch mutation in api/client can
// record switches without an import cycle.
import { safeGet, safeSet, session } from './hintStorage'

// Context switching: three switches within ten minutes suggests someone
// comparing clusters. Kept per tab. The switch that crosses the threshold
// stores the pair it found, so the row can show once the app has settled on the
// new cluster (the switch itself clears every query, and the row's gate with
// it, so an event listener alone would miss the moment).
export const CONTEXT_SWITCH_THRESHOLD = 3
export const CONTEXT_SWITCH_WINDOW_MS = 10 * 60 * 1000
export const CONTEXT_SWITCHED_EVENT = 'radar:context-switched'

export interface ContextSwitchRecord {
  at: number
  from: string
  to: string
}

const switchLogKey = 'radar.cloudHint.context-switch.log'
const triggeredKey = 'radar.cloudHint.context-switch.pair'

export function readSwitchLog(): ContextSwitchRecord[] {
  try {
    const parsed = JSON.parse(safeGet(session, switchLogKey) ?? '[]')
    if (!Array.isArray(parsed)) return []
    return parsed.filter(
      (r): r is ContextSwitchRecord =>
        !!r && typeof r.at === 'number' && typeof r.from === 'string' && typeof r.to === 'string',
    )
  } catch {
    return []
  }
}

export function recentSwitches(log: ContextSwitchRecord[], now: number): ContextSwitchRecord[] {
  return log.filter((r) => now - r.at <= CONTEXT_SWITCH_WINDOW_MS && r.at <= now)
}

// The two clusters the person is going back and forth between: the latest
// destination and the most recent other one they touched.
export function comparedContexts(log: ContextSwitchRecord[]): [string, string] | null {
  if (log.length === 0) return null
  const latest = log[log.length - 1]
  for (let i = log.length - 1; i >= 0; i--) {
    for (const name of [log[i].from, log[i].to]) {
      if (name && name !== latest.to) return [name, latest.to]
    }
  }
  return null
}

export function recordContextSwitch(from: string | undefined, to: string, now = Date.now()) {
  const previous = readSwitchLog()
  // A switch right after another can land before cluster info refetches; the
  // last recorded destination is where this one started.
  const origin = from || previous[previous.length - 1]?.to
  if (!origin || origin === to) return
  const log = recentSwitches([...previous, { at: now, from: origin, to }], now).slice(-10)
  safeSet(session, switchLogKey, JSON.stringify(log))
  const pair = log.length >= CONTEXT_SWITCH_THRESHOLD ? comparedContexts(log) : null
  if (pair) safeSet(session, triggeredKey, JSON.stringify(pair))
  window.dispatchEvent(new CustomEvent(CONTEXT_SWITCHED_EVENT))
}

// The pair the last threshold-crossing switch found in this tab, if any.
export function readTriggeredPair(): [string, string] | null {
  try {
    const parsed = JSON.parse(safeGet(session, triggeredKey) ?? 'null')
    return Array.isArray(parsed) && parsed.length === 2 && parsed.every((v) => typeof v === 'string')
      ? [parsed[0], parsed[1]]
      : null
  } catch {
    return null
  }
}
