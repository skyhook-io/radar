/**
 * The server's reading of a ScheduledBackup schedule (see
 * /api/cnpg/scheduledbackups/{ns}/{name}/schedule-preview): parsed as
 * CloudNativePG parses it, with the next runs counted as the operator counts
 * them. Times are RFC 3339 UTC.
 */
export interface CNPGSchedulePreview {
  schedule: string
  valid: boolean
  error?: string
  description?: string
  nextRuns?: string[]
  /** The first run is due already: the operator creates a backup as soon as it reconciles. */
  runsImmediately?: boolean
  basis: 'lastCheckTime' | 'now'
  lastCheckTime?: string
  suspended?: boolean
  clock?: { zone: string; declared: boolean; source: string }
}

export function formatCNPGRunTime(iso: string): { utc: string; local: string } {
  const d = new Date(iso)
  const pad = (n: number) => String(n).padStart(2, '0')
  const utc = `${d.getUTCFullYear()}-${pad(d.getUTCMonth() + 1)}-${pad(d.getUTCDate())} ${pad(d.getUTCHours())}:${pad(d.getUTCMinutes())}:${pad(d.getUTCSeconds())} UTC`
  return { utc, local: d.toLocaleString() }
}

/** One line for the runs list: why the first run is when it is. */
export function cnpgScheduleBasisNote(p: CNPGSchedulePreview): string {
  const clock = p.clock?.source ?? 'Operator clock is not established; upcoming times assume UTC.'
  if (p.suspended) return `Suspended: nothing runs until it is resumed. ${clock}`
  const basis = p.basis === 'lastCheckTime' ? "Counted from the operator's last check (status.lastCheckTime)." : 'Counted from now; the operator starts counting at its first check.'
  const due = p.runsImmediately ? ` ${p.clock?.declared ? 'A run is due on the declared clock.' : 'A run is due in this UTC estimate.'} The operator takes at most one catch-up backup.` : ''
  return `${basis} ${clock}${due}`
}
