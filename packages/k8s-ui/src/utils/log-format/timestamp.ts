export type TimestampFormat =
  | 'time-local'
  | 'time-utc'
  | 'iso-local'
  | 'iso-utc'
  | 'relative'
  | 'epoch'

function formatRelative(date: Date, now: number): string {
  const diffSec = Math.round((now - date.getTime()) / 1000)
  const abs = Math.abs(diffSec)
  const suffix = diffSec >= 0 ? ' ago' : ' from now'
  if (abs < 2) return 'just now'
  if (abs < 60) return `${abs}s${suffix}`
  if (abs < 3600) return `${Math.floor(abs / 60)}m${suffix}`
  if (abs < 86400) return `${Math.floor(abs / 3600)}h${suffix}`
  return `${Math.floor(abs / 86400)}d${suffix}`
}

export function createLogTimestampFormatter(format: TimestampFormat, timeZone?: string) {
  const clock: Intl.DateTimeFormatOptions = { timeZone, hour: '2-digit', minute: '2-digit', second: '2-digit' }
  let formatDate: (date: Date, now?: number) => string
  switch (format) {
    case 'time-local':
      formatDate = new Intl.DateTimeFormat('en-US', { ...clock, hour12: false }).format
      break
    case 'iso-local': {
      const formatter = new Intl.DateTimeFormat('en-US', {
        ...clock, hourCycle: 'h23', year: 'numeric', month: '2-digit', day: '2-digit', timeZoneName: 'longOffset',
      })
      formatDate = date => {
        const parts = Object.fromEntries(formatter.formatToParts(date).map(({ type, value }) => [type, value]))
        const offset = parts.timeZoneName.slice(3, 9) || '+00:00'
        return `${parts.year}-${parts.month}-${parts.day}T${parts.hour}:${parts.minute}:${parts.second}${offset}`
      }
      break
    }
    case 'time-utc':
      formatDate = date => date.toISOString().slice(11, 19)
      break
    case 'iso-utc':
      formatDate = date => date.toISOString()
      break
    case 'epoch':
      formatDate = date => String(Math.floor(date.getTime() / 1000))
      break
    case 'relative':
      formatDate = (date, now) => formatRelative(date, now ?? Date.now())
      break
  }
  return (ts: string, now?: number) => {
    const date = new Date(ts)
    return isNaN(date.getTime()) ? ts.slice(11, 19) || ts : formatDate(date, now)
  }
}
