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
}

export function formatCNPGRunTime(iso: string): { utc: string; local: string } {
  const d = new Date(iso)
  const pad = (n: number) => String(n).padStart(2, '0')
  const utc = `${d.getUTCFullYear()}-${pad(d.getUTCMonth() + 1)}-${pad(d.getUTCDate())} ${pad(d.getUTCHours())}:${pad(d.getUTCMinutes())}:${pad(d.getUTCSeconds())} UTC`
  return { utc, local: d.toLocaleString() }
}

/** One line for the runs list: why the first run is when it is. */
export function cnpgScheduleBasisNote(p: CNPGSchedulePreview): string {
  if (p.suspended) return 'Suspended: these are the times the schedule names; nothing runs until it is resumed.'
  if (p.runsImmediately) return "The operator runs one backup as soon as it sees this schedule: a scheduled time has passed since its last check. Missed runs are not replayed beyond that one."
  return p.basis === 'lastCheckTime'
    ? "Counted from the operator's last check (status.lastCheckTime), as the operator counts them, on its clock (UTC unless its Pod sets TZ)."
    : "Counted from now; the operator starts counting at its first check (UTC unless its Pod sets TZ)."
}
