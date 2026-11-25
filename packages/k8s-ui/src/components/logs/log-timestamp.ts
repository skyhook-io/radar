import { formatLogTimestamp, type TimestampFormat } from '../../utils/log-format'

export function createLogTimestampFormatter(format: TimestampFormat, timeZone?: string) {
  if (format !== 'time-local' && format !== 'iso-local') {
    return (ts: string) => formatLogTimestamp(ts, format)
  }

  const clock: Intl.DateTimeFormatOptions = { timeZone, hour: '2-digit', minute: '2-digit', second: '2-digit' }
  const formatter = new Intl.DateTimeFormat('en-US', format === 'time-local'
    ? { ...clock, hour12: false }
    : { ...clock, hourCycle: 'h23', year: 'numeric', month: '2-digit', day: '2-digit', timeZoneName: 'longOffset' })

  return (ts: string) => {
    const date = new Date(ts)
    if (isNaN(date.getTime())) return ts.slice(11, 19) || ts
    if (format === 'time-local') return formatter.format(date)
    const parts = Object.fromEntries(formatter.formatToParts(date).map(({ type, value }) => [type, value]))
    const offset = parts.timeZoneName.slice(3, 9) || '+00:00'
    return `${parts.year}-${parts.month}-${parts.day}T${parts.hour}:${parts.minute}:${parts.second}${offset}`
  }
}
