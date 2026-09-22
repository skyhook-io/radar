// Copy and predicate for the "Radar" marker shown beside a problem the
// GitOps controller did not assess itself — Argo CD 3 keeps per-resource
// health out of the Application object, so Radar reads the live resource
// instead and must say so. Shared by the tree node card and the Changes
// table so the two never drift.

// One sentence in plain words; the engine's own message follows it when
// there is one.
export const RADAR_HEALTH_NOTE = "Found by Radar. Argo CD's own health for this resource isn't available."

export interface RadarHealthSubject {
  health?: string
  healthSource?: string
  healthMessage?: string
}

// radarHealthNote returns the marker tooltip for a row/node whose problem
// came from Radar rather than the controller, or '' when there is no
// problem to mark or the controller assessed it.
export function radarHealthNote(subject: RadarHealthSubject): string {
  if (subject.healthSource !== 'radar') return ''
  if (subject.health !== 'Degraded' && subject.health !== 'Missing') return ''
  return subject.healthMessage ? `${RADAR_HEALTH_NOTE} ${subject.healthMessage}` : RADAR_HEALTH_NOTE
}
