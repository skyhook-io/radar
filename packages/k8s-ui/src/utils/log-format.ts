/**
 * Log formatting and display utilities.
 * Shared between LogsViewer and WorkloadLogsViewer components.
 */

import type { LogLevel } from '../components/logs/useLogBuffer'

export type TimestampFormat =
  | 'time-local'
  | 'time-utc'
  | 'iso-local'
  | 'iso-utc'
  | 'relative'
  | 'epoch'

export const TIMESTAMP_FORMAT_LABELS: Record<TimestampFormat, string> = {
  'time-local': 'Time (local)',
  'time-utc': 'Time (UTC)',
  'iso-local': 'ISO (local)',
  'iso-utc': 'ISO (UTC)',
  'relative': 'Relative',
  'epoch': 'Epoch',
}

function pad2(n: number): string {
  return n < 10 ? '0' + n : String(n)
}

function formatIsoLocal(date: Date): string {
  const offset = -date.getTimezoneOffset()
  const sign = offset >= 0 ? '+' : '-'
  const abs = Math.abs(offset)
  return (
    date.getFullYear() +
    '-' + pad2(date.getMonth() + 1) +
    '-' + pad2(date.getDate()) +
    'T' + pad2(date.getHours()) +
    ':' + pad2(date.getMinutes()) +
    ':' + pad2(date.getSeconds()) +
    sign + pad2(Math.floor(abs / 60)) + ':' + pad2(abs % 60)
  )
}

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

/**
 * Format a K8s log timestamp for display.
 * Supports multiple display formats and UTC/local time zones.
 * `now` is accepted for testability; defaults to Date.now().
 */
export function formatLogTimestamp(
  ts: string,
  format: TimestampFormat = 'time-local',
  now?: number,
): string {
  const date = new Date(ts)
  if (isNaN(date.getTime())) {
    return ts.slice(11, 19) || ts
  }
  switch (format) {
    case 'time-utc':
      return date.toISOString().slice(11, 19)
    case 'iso-local':
      return formatIsoLocal(date)
    case 'iso-utc':
      return date.toISOString()
    case 'relative':
      return formatRelative(date, now ?? Date.now())
    case 'epoch':
      return String(Math.floor(date.getTime() / 1000))
    case 'time-local':
    default:
      return date.toLocaleTimeString('en-US', {
        hour12: false,
        hour: '2-digit',
        minute: '2-digit',
        second: '2-digit',
      })
  }
}

/** Map a detected LogLevel to a Tailwind color class. */
export function getLevelColor(level: LogLevel): string {
  switch (level) {
    case 'error': return 'text-red-400'
    case 'warn': return 'text-yellow-400'
    case 'debug': return 'text-theme-text-secondary'
    case 'info': return 'text-theme-text-primary'
    default: return 'text-theme-text-primary'
  }
}

/**
 * Highlight search query matches in text with a mark tag.
 * Returns HTML string safe for dangerouslySetInnerHTML.
 */
export function highlightSearchMatches(
  text: string, query: string, isRegex = false, isCaseSensitive = false,
): string {
  if (!query) return escapeHtml(text)
  const escaped = escapeHtml(text)
  const flags = isCaseSensitive ? 'g' : 'gi'
  let pattern: string
  if (isRegex) {
    try {
      // Validate the regex first — if invalid, fall back to literal
      new RegExp(query)
      pattern = query
    } catch {
      pattern = escapeRegExp(escapeHtml(query))
    }
  } else {
    pattern = escapeRegExp(escapeHtml(query))
  }
  const regex = new RegExp(`(${pattern})`, flags)
  return escaped.replace(regex, '<mark class="log-highlight">$1</mark>')
}

/**
 * Escape HTML special characters to prevent XSS.
 */
export function escapeHtml(text: string): string {
  return text
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
}

/**
 * Strip ANSI escape sequences from text.
 * Covers common CSI sequences (e.g. \x1b[32m, \x1b[0m) found in pod logs.
 * Used for search matching and log level detection on raw log content.
 */
export function stripAnsi(text: string): string {
  // eslint-disable-next-line no-control-regex
  return text.replace(/\x1b\[[0-9;]*[a-zA-Z]/g, '')
}

// Standard 256-color palette (indices 0-255)
// 0-7: standard colors, 8-15: bright colors, 16-231: 6x6x6 color cube, 232-255: grayscale
const ANSI_256_COLORS: string[] = (() => {
  const colors: string[] = [
    '#4c4c4c', '#cd3131', '#0dbc79', '#e5e510', '#2472c8', '#bc3fbc', '#11a8cd', '#e5e5e5',
    '#767676', '#f14c4c', '#23d18b', '#f5f543', '#3b8eea', '#d670d6', '#29b8db', '#e5e5e5',
  ]
  // 6x6x6 color cube (indices 16-231)
  const levels = [0, 95, 135, 175, 215, 255]
  for (let r = 0; r < 6; r++) {
    for (let g = 0; g < 6; g++) {
      for (let b = 0; b < 6; b++) {
        colors.push(`#${levels[r].toString(16).padStart(2, '0')}${levels[g].toString(16).padStart(2, '0')}${levels[b].toString(16).padStart(2, '0')}`)
      }
    }
  }
  // Grayscale (indices 232-255)
  for (let i = 0; i < 24; i++) {
    const v = (8 + i * 10).toString(16).padStart(2, '0')
    colors.push(`#${v}${v}${v}`)
  }
  return colors
})()

// ANSI SGR (Select Graphic Rendition) code → CSS style mapping.
// Uses CSS custom properties from variables.css for theme-aware light/dark colors.
const SGR_STYLES: Record<number, string> = {
  1: 'font-weight:bold',
  2: 'opacity:0.6',
  3: 'font-style:italic',
  4: 'text-decoration:underline',
  // Standard foreground colors (30-37)
  30: 'color:var(--ansi-black)',
  31: 'color:var(--ansi-red)',
  32: 'color:var(--ansi-green)',
  33: 'color:var(--ansi-yellow)',
  34: 'color:var(--ansi-blue)',
  35: 'color:var(--ansi-magenta)',
  36: 'color:var(--ansi-cyan)',
  37: 'color:var(--ansi-white)',
  // Standard background colors (40-47)
  40: 'background-color:var(--ansi-black)',
  41: 'background-color:var(--ansi-red)',
  42: 'background-color:var(--ansi-green)',
  43: 'background-color:var(--ansi-yellow)',
  44: 'background-color:var(--ansi-blue)',
  45: 'background-color:var(--ansi-magenta)',
  46: 'background-color:var(--ansi-cyan)',
  47: 'background-color:var(--ansi-white)',
  // Bright foreground colors (90-97)
  90: 'color:var(--ansi-bright-black)',
  91: 'color:var(--ansi-bright-red)',
  92: 'color:var(--ansi-bright-green)',
  93: 'color:var(--ansi-bright-yellow)',
  94: 'color:var(--ansi-bright-blue)',
  95: 'color:var(--ansi-bright-magenta)',
  96: 'color:var(--ansi-bright-cyan)',
  97: 'color:var(--ansi-bright-white)',
  // Bright background colors (100-107)
  100: 'background-color:var(--ansi-bright-black)',
  101: 'background-color:var(--ansi-bright-red)',
  102: 'background-color:var(--ansi-bright-green)',
  103: 'background-color:var(--ansi-bright-yellow)',
  104: 'background-color:var(--ansi-bright-blue)',
  105: 'background-color:var(--ansi-bright-magenta)',
  106: 'background-color:var(--ansi-bright-cyan)',
  107: 'background-color:var(--ansi-bright-white)',
}

// Maps 256-color palette indices 0-15 to CSS variable names for theme-aware rendering
const ANSI_16_NAMES = [
  'black', 'red', 'green', 'yellow', 'blue', 'magenta', 'cyan', 'white',
  'bright-black', 'bright-red', 'bright-green', 'bright-yellow', 'bright-blue', 'bright-magenta', 'bright-cyan', 'bright-white',
]

/**
 * Resolve an array of SGR codes to a CSS style string.
 * Handles 256-color (38;5;N / 48;5;N) sequences.
 */
function resolveSgrStyles(codes: number[]): string {
  const parts: string[] = []
  let i = 0
  while (i < codes.length) {
    // RGB foreground: 38;2;r;g;b
    if (codes[i] === 38 && codes[i + 1] === 2 && codes[i + 4] !== undefined) {
      const r = codes[i + 2], g = codes[i + 3], b = codes[i + 4]
      parts.push(`color:rgb(${r},${g},${b})`)
      i += 5
      continue
    }
    // RGB background: 48;2;r;g;b
    if (codes[i] === 48 && codes[i + 1] === 2 && codes[i + 4] !== undefined) {
      const r = codes[i + 2], g = codes[i + 3], b = codes[i + 4]
      parts.push(`background-color:rgb(${r},${g},${b})`)
      i += 5
      continue
    }
    // 256-color foreground: 38;5;N
    if (codes[i] === 38 && codes[i + 1] === 5 && codes[i + 2] !== undefined) {
      const colorIdx = codes[i + 2]
      if (colorIdx >= 0 && colorIdx < 16) {
        // Map 256-color indices 0-15 to theme-aware CSS variables
        parts.push(`color:var(--ansi-${ANSI_16_NAMES[colorIdx]})`)
      } else if (colorIdx < 256) {
        parts.push(`color:${ANSI_256_COLORS[colorIdx]}`)
      }
      i += 3
      continue
    }
    // 256-color background: 48;5;N
    if (codes[i] === 48 && codes[i + 1] === 5 && codes[i + 2] !== undefined) {
      const colorIdx = codes[i + 2]
      if (colorIdx >= 0 && colorIdx < 16) {
        parts.push(`background-color:var(--ansi-${ANSI_16_NAMES[colorIdx]})`)
      } else if (colorIdx < 256) {
        parts.push(`background-color:${ANSI_256_COLORS[colorIdx]}`)
      }
      i += 3
      continue
    }
    // Standard SGR code
    const style = SGR_STYLES[codes[i]]
    if (style) {
      parts.push(style)
    }
    i++
  }
  return parts.join(';')
}

/**
 * Convert ANSI SGR escape codes in a log line to HTML <span> elements.
 * HTML-escapes the text first so the output is safe for dangerouslySetInnerHTML.
 * Each call is independent — all opened spans are closed before returning.
 * Only SGR sequences (\x1b[...m) are handled; other ANSI sequences are stripped.
 */
export function ansiToHtml(text: string): string {
  // HTML-escape first: ANSI escape sequences contain no HTML special characters
  // (&, <, >) so escaping won't interfere with the ANSI pattern matching below.
  const escaped = escapeHtml(text)

  let result = ''
  let openSpans = 0
  // eslint-disable-next-line no-control-regex
  const ansiRe = /\x1b\[([0-9;]*)m/g
  let lastIndex = 0
  let match: RegExpExecArray | null

  while ((match = ansiRe.exec(escaped)) !== null) {
    result += escaped.slice(lastIndex, match.index)
    lastIndex = match.index + match[0].length

    const codes = match[1] === '' ? [0] : match[1].split(';').map(Number)

    const resetIdx = codes.indexOf(0)
    if (resetIdx !== -1) {
      // Close all open spans on reset
      result += '</span>'.repeat(openSpans)
      openSpans = 0
      // Apply any codes that follow the reset in the same sequence (e.g. \x1b[0;32m)
      const afterReset = codes.slice(resetIdx + 1)
      const styles = resolveSgrStyles(afterReset)
      if (styles) {
        result += `<span style="${styles}">`
        openSpans++
      }
    } else {
      const styles = resolveSgrStyles(codes)
      if (styles) {
        result += `<span style="${styles}">`
        openSpans++
      }
    }
  }

  result += escaped.slice(lastIndex)
  result += '</span>'.repeat(openSpans)
  return result
}

/**
 * Escape special regex characters in a string.
 */
export function escapeRegExp(text: string): string {
  return text.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')
}

/**
 * Parse a K8s log line to extract timestamp and content.
 * K8s timestamps are in RFC3339Nano format: 2024-01-20T10:30:00.123456789Z content
 */
export function parseLogLine(line: string): { timestamp: string; content: string } {
  if (line.length > 30 && line[4] === '-' && line[7] === '-' && line[10] === 'T') {
    const spaceIdx = line.indexOf(' ')
    if (spaceIdx > 20 && spaceIdx < 40) {
      return { timestamp: line.slice(0, spaceIdx), content: line.slice(spaceIdx + 1) }
    }
  }
  return { timestamp: '', content: line }
}

/**
 * Parse a log range string (e.g. "500" or "since:300") into tailLines/sinceSeconds params.
 */
export function parseLogRange(logRange: string): { tailLines?: number; sinceSeconds?: number } {
  if (logRange.startsWith('since:')) {
    return { sinceSeconds: Number(logRange.slice(6)) }
  }
  return { tailLines: Number(logRange) }
}

// Syntax highlight colors shared between JSON and logfmt rendering
export const SYNTAX_COLOR_KEY = '#7cacf8'
export const SYNTAX_COLOR_STRING = '#73c991'
export const SYNTAX_COLOR_NUMBER = '#e5c07b'
export const SYNTAX_COLOR_BOOLEAN = '#c678dd'
export const SYNTAX_COLOR_NULL = '#808080'

/**
 * Syntax-highlight a pretty-printed JSON string for HTML display.
 * Wraps keys, string values, numbers, booleans, and null in colored spans.
 * Applies HTML escaping internally — do not pre-escape the input.
 */
export function highlightJson(json: string): string {
  const escaped = escapeHtml(json)
  // Single-pass tokenizer: matches all JSON tokens at once so content inside strings
  // is consumed and can't be re-matched by number/boolean/null patterns.
  const tokenRe = /("(?:\\.|[^"\\])*")\s*:|("(?:\\.|[^"\\])*")|(-?\b\d+(?:\.\d+)?(?:[eE][+-]?\d+)?\b)|\b(true|false)\b|\b(null)\b/g
  return escaped.replace(tokenRe, (match, key, str, num, bool, nil) => {
    if (key) return `<span style="color:${SYNTAX_COLOR_KEY}">${key}</span>:`
    if (str) return `<span style="color:${SYNTAX_COLOR_STRING}">${str}</span>`
    if (num) return `<span style="color:${SYNTAX_COLOR_NUMBER}">${num}</span>`
    if (bool) return `<span style="color:${SYNTAX_COLOR_BOOLEAN}">${bool}</span>`
    if (nil) return `<span style="color:${SYNTAX_COLOR_NULL}">${nil}</span>`
    return match
  })
}

/**
 * Unescape common escape sequences within JSON string values.
 * Converts literal \n, \t, \r\n in pretty-printed JSON to real whitespace,
 * making stack traces and multi-line messages readable in the expanded view.
 */
export function unescapeJsonStrings(text: string): string {
  return text.replace(/"(?:[^"\\]|\\.)*"/g, (match) => {
    return match
      .replace(/\\r\\n/g, '\r\n')
      .replace(/\\n/g, '\n')
      .replace(/\\t/g, '\t')
  })
}

// logfmt detection and parsing

const LOGFMT_PAIR_RE = /(?:^|\s)([a-zA-Z_][\w.]*)=((?:"(?:[^"\\]|\\.)*")|(?:[^\s]*))/g

/**
 * Detect if a log line is in logfmt format (key=value pairs).
 * Requires at least 3 pairs AND that the matched pairs cover most of the line,
 * to avoid false positives on prose text that happens to contain a few key=value fragments.
 */
export function isLogfmt(content: string): boolean {
  const trimmed = content.trimStart()
  if (trimmed[0] === '{') return false
  const matches = trimmed.match(LOGFMT_PAIR_RE)
  if (!matches || matches.length < 3) return false
  // Check that key=value pairs cover at least 60% of the line length
  const matchedLength = matches.reduce((sum, m) => sum + m.trim().length, 0)
  return matchedLength / trimmed.length >= 0.6
}

/**
 * Parse a logfmt line into a key-value record.
 * Handles quoted values with escape sequences.
 */
export function parseLogfmt(content: string): Record<string, string> | null {
  const result: Record<string, string> = {}
  let count = 0
  let match: RegExpExecArray | null
  const re = new RegExp(LOGFMT_PAIR_RE.source, 'g')
  while ((match = re.exec(content)) !== null) {
    const key = match[1]
    let val = match[2]
    if (val.startsWith('"') && val.endsWith('"')) {
      val = val.slice(1, -1).replace(/\\"/g, '"').replace(/\\n/g, '\n').replace(/\\t/g, '\t')
    }
    result[key] = val
    count++
  }
  return count >= 2 ? result : null
}

/**
 * Handle SSE error events from log streams.
 * Parses server-sent error data and logs it, then calls onClose.
 */
export function handleSSEError(event: Event, prefix: string, onClose: () => void): void {
  const me = event as MessageEvent
  if (me.data) {
    try {
      const data = JSON.parse(me.data)
      console.error(`${prefix}:`, data.error || data.message || me.data)
    } catch {
      console.error(`${prefix}:`, me.data)
    }
  } else {
    console.error(`${prefix} connection error`)
  }
  onClose()
}
