// "14:02" for an outage that started today, "Oct 29, 14:02" otherwise.
// Returns null when the server didn't say (older Radar) or sent garbage.
export function outageSinceLabel(disconnectedAt: string | undefined, now: Date = new Date()): string | null {
  if (!disconnectedAt) return null
  const started = new Date(disconnectedAt)
  if (Number.isNaN(started.getTime())) return null
  const time = started.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })
  if (started.toDateString() === now.toDateString()) return time
  return `${started.toLocaleDateString([], { month: 'short', day: 'numeric' })}, ${time}`
}
