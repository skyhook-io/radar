/**
 * Log formatting and display utilities.
 * Shared between LogsViewer and WorkloadLogsViewer components.
 */

import type { LogLevel } from '../components/logs/useLogBuffer'

/**
 * Format a K8s log timestamp for display.
 * Extracts and formats the time portion (HH:MM:SS).
 */
export function formatLogTimestamp(ts: string): string {
  const date = new Date(ts)
  if (isNaN(date.getTime())) {
    // Fallback: extract HH:MM:SS from ISO timestamp
    return ts.slice(11, 19) || ts
  }
  return date.toLocaleTimeString('en-US', {
    hour12: false,
    hour: '2-digit',
    minute: '2-digit',
    second: '2-digit',
  })
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
  return escaped.replace(regex, '<mark style="background:rgba(234,179,8,0.4);color:inherit;border-radius:2px;padding:0 1px">$1</mark>')
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
// Colors are chosen to be legible on a dark background (terminal-standard palette).
const SGR_STYLES: Record<number, string> = {
  1: 'font-weight:bold',
  2: 'opacity:0.6',
  3: 'font-style:italic',
  4: 'text-decoration:underline',
  // Standard foreground colors (30-37)
  30: 'color:#4c4c4c',
  31: 'color:#cd3131',
  32: 'color:#0dbc79',
  33: 'color:#e5e510',
  34: 'color:#2472c8',
  35: 'color:#bc3fbc',
  36: 'color:#11a8cd',
  37: 'color:#e5e5e5',
  // Standard background colors (40-47)
  40: 'background-color:#4c4c4c',
  41: 'background-color:#cd3131',
  42: 'background-color:#0dbc79',
  43: 'background-color:#e5e510',
  44: 'background-color:#2472c8',
  45: 'background-color:#bc3fbc',
  46: 'background-color:#11a8cd',
  47: 'background-color:#e5e5e5',
  // Bright foreground colors (90-97)
  90: 'color:#767676',
  91: 'color:#f14c4c',
  92: 'color:#23d18b',
  93: 'color:#f5f543',
  94: 'color:#3b8eea',
  95: 'color:#d670d6',
  96: 'color:#29b8db',
  97: 'color:#e5e5e5',
  // Bright background colors (100-107)
  100: 'background-color:#767676',
  101: 'background-color:#f14c4c',
  102: 'background-color:#23d18b',
  103: 'background-color:#f5f543',
  104: 'background-color:#3b8eea',
  105: 'background-color:#d670d6',
  106: 'background-color:#29b8db',
  107: 'background-color:#e5e5e5',
}

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
      if (colorIdx >= 0 && colorIdx < 256) {
        parts.push(`color:${ANSI_256_COLORS[colorIdx]}`)
      }
      i += 3
      continue
    }
    // 256-color background: 48;5;N
    if (codes[i] === 48 && codes[i + 1] === 5 && codes[i + 2] !== undefined) {
      const colorIdx = codes[i + 2]
      if (colorIdx >= 0 && colorIdx < 256) {
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
