import { seriesColor, seriesFill } from '../components/charts/colors'

// Per-workload color encoding for the application topology graph. A workload's
// exclusive satellites (its Service, config, pods) carry its hue; shared and
// unattached resources stay neutral. Reuses SERIES_COLORS — the codebase's
// categorical palette for multi-series charts (10 well-separated 500-level
// shades, vetted on both themes) — so workload colors match the rest of the UI.
//
// Solid swatch for the rail legend; faint fill for node card ownership. Health
// remains encoded separately by the node border and status dot.

export interface WorkloadHue {
  /** Solid — the rail legend chip. */
  swatch: string
  /** Faint fill (~13% alpha) — the node card tint, layered over the surface. */
  wash: string
}

/** Sentinel owner for shared / unattached nodes — they get no hue (neutral).
 *  Collision-proof: real workload keys always contain two `/`. */
export const NEUTRAL_OWNER = '__neutral__'

/** The hover-focus channel's three states: `null` = no focus (everything lit),
 *  `NEUTRAL_OWNER` = focus the shared/unscoped bucket, any other string = a
 *  workload key (see `workloadKey`) whose neighborhood stays lit. */
export type WorkloadFocus = string | null

const NEUTRAL_FALLBACK = '#64748b' // slate-500

export function workloadHue(index: number): WorkloadHue {
  return {
    swatch: seriesColor(index, NEUTRAL_FALLBACK),
    wash: seriesFill(index, NEUTRAL_FALLBACK),
  }
}
