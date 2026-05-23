import type { RightsizingTone } from '../../api/client'

/**
 * Severity ordering for the rightsizing tone vocabulary — the only enforcement
 * of this ordering in TS-land. Keep aligned with the backend's
 * `Tone` constants in `internal/prometheus/rightsizing.go`. Adding a new tone
 * triggers a TypeScript exhaustiveness error here.
 */
export const TONE_RANK: Record<RightsizingTone, number> = {
  ok: 0,
  info: 1,
  warning: 2,
  alert: 3,
  critical: 4,
}

/** Pick the worst tone in a list. Returns 'ok' for empty input. */
export function worstTone(tones: RightsizingTone[]): RightsizingTone {
  return tones.reduce<RightsizingTone>(
    (acc, t) => (TONE_RANK[t] > TONE_RANK[acc] ? t : acc),
    'ok',
  )
}
