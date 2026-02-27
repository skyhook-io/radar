/**
 * Log formatting and display utilities.
 * Shared between LogsViewer and WorkloadLogsViewer components.
 */

/**
 * Format a K8s log timestamp for display.
 * Extracts and formats the time portion (HH:MM:SS).
 */
export function formatLogTimestamp(ts: string): string {
  try {
    const date = new Date(ts)
    return date.toLocaleTimeString('en-US', {
      hour12: false,
      hour: '2-digit',
      minute: '2-digit',
      second: '2-digit',
    })
  } catch {
    // Fallback: extract HH:MM:SS from ISO timestamp
    return ts.slice(11, 19)
  }
}

/**
 * Determine the color class for a log line based on its content.
 * Detects common log level keywords.
 */
export function getLogLevelColor(content: string): string {
  const lower = content.toLowerCase()
  if (lower.includes('error') || lower.includes('fatal') || lower.includes('panic')) {
    return 'text-red-400'
  }
  if (lower.includes('warn')) {
    return 'text-yellow-400'
  }
  if (lower.includes('debug') || lower.includes('trace')) {
    return 'text-theme-text-secondary'
  }
  return 'text-theme-text-primary'
}

/**
 * Highlight search query matches in text with a mark tag.
 * Returns HTML string safe for dangerouslySetInnerHTML.
 */
export function highlightSearchMatches(text: string, query: string): string {
  if (!query) return escapeHtml(text)
  const escaped = escapeHtml(text)
  const escapedQuery = escapeHtml(query)
  const regex = new RegExp(`(${escapeRegExp(escapedQuery)})`, 'gi')
  return escaped.replace(regex, '<mark class="bg-yellow-500/30 text-yellow-200">$1</mark>')
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
  // Bright foreground colors (90-97)
  90: 'color:#767676',
  91: 'color:#f14c4c',
  92: 'color:#23d18b',
  93: 'color:#f5f543',
  94: 'color:#3b8eea',
  95: 'color:#d670d6',
  96: 'color:#29b8db',
  97: 'color:#e5e5e5',
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
      const styles = afterReset
        .map(c => SGR_STYLES[c])
        .filter(Boolean)
        .join(';')
      if (styles) {
        result += `<span style="${styles}">`
        openSpans++
      }
    } else {
      const styles = codes
        .map(c => SGR_STYLES[c])
        .filter(Boolean)
        .join(';')
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
