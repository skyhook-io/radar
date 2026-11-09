// Compact human-readable formatters for chart values and timestamps.
// Unit-tier breakpoints chosen so the rendered text stays short enough to
// fit inside chart axis labels and tooltips at typical font sizes.

export function formatMetricValue(value: number, unit: string): string {
  if (value === 0) return '0'
  if (value < 0) return `-${formatMetricValue(-value, unit)}`

  switch (unit) {
    case 'percent':
      return `${value.toFixed(value < 1 ? 2 : 1)}%`
    case 'requests/s':
      return `${value.toFixed(value < 1 ? 3 : 1)} req/s`
    case 'cores': {
      if (value < 0.0001) return '< 0.1m'
      if (value < 0.001) return `${(value * 1000).toFixed(1)}m`
      if (value < 1) return `${(value * 1000).toFixed(0)}m`
      return `${value.toFixed(2)}`
    }
    case 'bytes': {
      if (value < 1) return '< 1 B'
      if (value < 1024) return `${value.toFixed(0)} B`
      if (value < 1024 * 1024) return `${(value / 1024).toFixed(1)} KiB`
      if (value < 1024 * 1024 * 1024) return `${(value / (1024 * 1024)).toFixed(1)} MiB`
      return `${(value / (1024 * 1024 * 1024)).toFixed(2)} GiB`
    }
    case 'bytes/s': {
      if (value < 1) return '< 1 B/s'
      if (value < 1024) return `${value.toFixed(0)} B/s`
      if (value < 1024 * 1024) return `${(value / 1024).toFixed(1)} KiB/s`
      if (value < 1024 * 1024 * 1024) return `${(value / (1024 * 1024)).toFixed(1)} MiB/s`
      return `${(value / (1024 * 1024 * 1024)).toFixed(2)} GiB/s`
    }
    case 'count':
    case 'restarts': {
      if (value < 10000) return value.toFixed(0)
      if (value < 1e6) return `${(value / 1e3).toFixed(1)}k`
      return `${(value / 1e6).toFixed(1)}M`
    }
    case 'seconds': {
      if (value < 0.001) return `${(value * 1_000_000).toFixed(0)}µs`
      if (value < 1) return `${(value * 1000).toFixed(value < 0.01 ? 2 : 0)}ms`
      if (value < 60) return `${value.toFixed(value < 10 ? 2 : 1)}s`
      if (value < 3600) return `${(value / 60).toFixed(1)}m`
      return `${(value / 3600).toFixed(1)}h`
    }
    default:
      if (value < 0.01) return value.toExponential(1)
      if (value < 1) return value.toFixed(3)
      if (value < 100) return value.toFixed(2)
      if (value < 10000) return value.toFixed(0)
      if (value < 1e6) return `${(value / 1e3).toFixed(1)}k`
      if (value < 1e9) return `${(value / 1e6).toFixed(1)}M`
      return `${(value / 1e9).toFixed(2)}G`
  }
}

export function formatTimestamp(unix: number): string {
  const d = new Date(unix * 1000)
  return d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })
}

const DAY_SECONDS = 24 * 60 * 60

// Past this span two instants can share a month and day, so dates name the year.
const YEAR_SPAN_SECONDS = 300 * DAY_SECONDS

/** Whether dates on a chart spanning this many seconds should name the year. */
export function datesNeedYear(spanSeconds: number): boolean {
  return spanSeconds > YEAR_SPAN_SECONDS
}

function formatDay(unix: number, withYear = false): string {
  return new Date(unix * 1000).toLocaleDateString([], { ...(withYear && { year: 'numeric' }), month: 'short', day: 'numeric' })
}

/** Full local date and time of one instant, for tooltips; `withYear` for charts spanning most of a year or more. */
export function formatFullTimestamp(unix: number, withYear = false): string {
  return new Date(unix * 1000).toLocaleString([], {
    ...(withYear && { year: 'numeric' }),
    month: 'short',
    day: 'numeric',
    hour: '2-digit',
    minute: '2-digit',
    second: '2-digit',
  })
}

/**
 * Labels for evenly spaced time-axis ticks given in ascending unix seconds.
 * A span of a day or less shows the time only (`formatTimestamp`). A longer
 * span names the day (and the year past about ten months): a tick that starts a new local day
 * reads "Oct 21 02:00 PM" and the rest show the time; when that is wider than
 * `maxWidth` (in ems, see `labelWidthEm`), ticks that start a day show the day
 * alone and the rest stay blank, so neighbouring labels never collide.
 */
export function formatTimeAxisLabels(ticks: number[], maxWidth = Infinity): string[] {
  if (ticks.length === 0) return []
  const span = ticks[ticks.length - 1] - ticks[0]
  if (span <= DAY_SECONDS) return ticks.map(formatTimestamp)

  const withYear = datesNeedYear(span)
  const days = ticks.map((ts) => formatDay(ts, withYear))
  const startsDay = days.map((day, i) => i === 0 || day !== days[i - 1])
  const mixed = ticks.map((ts, i) => (startsDay[i] ? `${days[i]} ${formatTimestamp(ts)}` : formatTimestamp(ts)))
  if (Math.max(...mixed.map(labelWidthEm)) <= maxWidth) return mixed
  return days.map((day, i) => (startsDay[i] ? day : ''))
}

/**
 * Estimated rendered width of a monospace label in ems: about 0.6em per
 * character, a full em for wide (CJK, fullwidth) characters.
 */
export function labelWidthEm(label: string): number {
  let width = 0
  for (const ch of label) width += /[\u1100-\u115F\u2E80-\uA4CF\uAC00-\uD7A3\uF900-\uFAFF\uFE30-\uFE4F\uFF00-\uFF60\uFFE0-\uFFE6]/.test(ch) ? 1 : 0.6
  return width
}

export interface TimeAxisTick {
  ts: number
  label: string
}

const HOUR_SECONDS = 60 * 60
// Sub-day steps align to local clock hours; day steps to local midnights.
const ALIGNED_STEPS_HOURS = [6, 12, 24, 48, 72, 168, 336, 672]

/**
 * Calendar-aligned ticks for a window longer than a day: local midnights
 * labelled with the day ("Oct 21"), and, when the window is short enough for
 * 6- or 12-hour steps, the hours between labelled with the time. Uses the
 * smallest step that keeps the tick count within `maxTicks` and passes `fits`
 * (the caller's check that neighbouring labels don't collide). Returns null
 * for a window of a day or less, or when fewer than two ticks would fit, so
 * the caller keeps evenly spaced ticks.
 */
export function alignedTimeAxisTicks(
  minTs: number,
  maxTs: number,
  maxTicks: number,
  fits: (ticks: TimeAxisTick[]) => boolean = () => true,
): TimeAxisTick[] | null {
  if (maxTs - minTs <= DAY_SECONDS || maxTicks < 2) return null
  for (const stepHours of ALIGNED_STEPS_HOURS) {
    if ((maxTs - minTs) / HOUR_SECONDS / stepHours > maxTicks + 1) continue
    const ticks = alignedTicks(minTs, maxTs, stepHours)
    if (ticks.length > maxTicks) continue
    if (ticks.length < 2) return null
    const labelled = ticks.map(({ ts, startsDay }) => ({ ts, label: startsDay ? formatDay(ts) : formatTimestamp(ts) }))
    if (fits(labelled)) return labelled
  }
  return null
}

function alignedTicks(minTs: number, maxTs: number, stepHours: number): { ts: number; startsDay: boolean }[] {
  const out: { ts: number; startsDay: boolean }[] = []
  const start = new Date(minTs * 1000)
  const stepDays = stepHours / 24
  // Each day's start is built from its date: where a DST change skips
  // midnight the Date lands on 01:00, and stepping with setDate would carry
  // that hour into every later day.
  for (let offset = 0; ; offset++) {
    const cursor = new Date(start.getFullYear(), start.getMonth(), start.getDate() + offset)
    if (cursor.getTime() / 1000 > maxTs) break
    if (stepHours < 24) {
      for (let hour = 0; hour < 24; hour += stepHours) {
        const ts = new Date(cursor.getFullYear(), cursor.getMonth(), cursor.getDate(), hour).getTime() / 1000
        if (ts >= minTs && ts <= maxTs) out.push({ ts, startsDay: hour === 0 })
      }
    } else {
      // Index days by calendar date so the chosen midnights stay put as the window slides.
      const dayIndex = Math.round(Date.UTC(cursor.getFullYear(), cursor.getMonth(), cursor.getDate()) / (DAY_SECONDS * 1000))
      const ts = cursor.getTime() / 1000
      if (dayIndex % stepDays === 0 && ts >= minTs) out.push({ ts, startsDay: true })
    }
  }
  return out
}
