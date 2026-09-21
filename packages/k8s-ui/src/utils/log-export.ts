/**
 * Serialization for the log viewer's export popover.
 *
 * Copy and download share this module so the two can never disagree about what
 * a line looks like — the popover's preview is rendered from the same functions
 * that produce the payload.
 */

import { stripAnsi } from './log-format'

export type LogExportFormat = 'txt' | 'json' | 'csv'

export interface ExportableLogEntry {
  timestamp: string
  content: string
  container?: string
  pod?: string
}

export interface LogExportOptions {
  format: LogExportFormat
  /**
   * Mirrors the viewer's timestamp toggle, and applies to `txt` only. JSON and
   * CSV always carry the timestamp: there it is a named field a reader can
   * ignore, not the visual noise the toggle exists to suppress.
   */
  showTimestamps: boolean
  /**
   * Mirrors the viewer's pod attribution, and — like `showTimestamps` — applies
   * to `txt` only. JSON and CSV carry `pod` whenever the entry has one: a
   * display preference must not silently strip the column that makes a
   * multi-pod export attributable.
   */
  showPodName: boolean
}

export interface LogExportPayload {
  content: string
  mime: string
  extension: LogExportFormat
}

// Charset is explicit: log content is arbitrary UTF-8, and Excel on Windows
// falls back to a legacy code page without it.
const MIME: Record<LogExportFormat, string> = {
  txt: 'text/plain;charset=utf-8',
  json: 'application/json;charset=utf-8',
  csv: 'text/csv;charset=utf-8',
}

/** Display order for the format picker. */
export const LOG_EXPORT_FORMATS: readonly LogExportFormat[] = ['txt', 'json', 'csv']

export const LOG_EXPORT_FORMAT_LABELS: Record<LogExportFormat, string> = {
  txt: 'Text',
  json: 'JSON',
  csv: 'CSV',
}

/**
 * One entry as the viewer would read it back: timestamps raw rather than in the
 * display format, since an exported line is correlated against other systems
 * long after the screen that produced it is gone.
 */
function textLine(entry: ExportableLogEntry, { showTimestamps, showPodName }: LogExportOptions): string {
  const parts: string[] = []
  if (showTimestamps && entry.timestamp) parts.push(entry.timestamp)
  if (showPodName && entry.pod) parts.push(entry.container ? `[${entry.pod}/${entry.container}]` : `[${entry.pod}]`)
  parts.push(stripAnsi(entry.content))
  return parts.join(' ')
}

function jsonEntry(entry: ExportableLogEntry): Record<string, string> {
  return {
    timestamp: entry.timestamp,
    ...(entry.pod ? { pod: entry.pod } : {}),
    ...(entry.container ? { container: entry.container } : {}),
    content: stripAnsi(entry.content),
  }
}

/** Columns follow the data, not a display toggle, so a row can never carry a field the header doesn't name. */
function csvColumns(entries: readonly ExportableLogEntry[]): string[] {
  return entries.some(e => e.pod)
    ? ['timestamp', 'pod', 'container', 'content']
    : ['timestamp', 'container', 'content']
}

// A cell opening with one of these is a formula to Excel and Sheets, and quoting
// does not stop it — the quotes are stripped before evaluation. Log content is
// whatever a pod chose to print, so the leading apostrophe (which spreadsheets
// consume on import) is the difference between a CSV and an execution vector.
const SPREADSHEET_FORMULA_START = /^[=+\-@\t\r]/

function csvCell(value: string): string {
  const escaped = (SPREADSHEET_FORMULA_START.test(value) ? `'${value}` : value).replace(/"/g, '""')
  return `"${escaped}"`
}

function csvRow(entry: ExportableLogEntry, columns: readonly string[]): string {
  const cells = [
    entry.timestamp,
    ...(columns.includes('pod') ? [entry.pod ?? ''] : []),
    entry.container ?? '',
    stripAnsi(entry.content),
  ]
  return cells.map(csvCell).join(',')
}

export function serializeLogEntries(
  entries: readonly ExportableLogEntry[],
  opts: LogExportOptions,
): LogExportPayload {
  const base = { mime: MIME[opts.format], extension: opts.format }
  switch (opts.format) {
    case 'json':
      return { ...base, content: JSON.stringify(entries.map(jsonEntry), null, 2) }
    case 'csv': {
      const columns = csvColumns(entries)
      return { ...base, content: [columns.join(','), ...entries.map(e => csvRow(e, columns))].join('\n') }
    }
    default:
      return { ...base, content: entries.map(e => textLine(e, opts)).join('\n') }
  }
}

/**
 * Up to two representative lines for the popover's preview box. This is the only
 * thing telling the user which fields their export carries, so it renders the
 * shape of the real output rather than a description of it: for CSV that means
 * the header alongside a row, and for JSON a single compact entry rather than
 * the pretty-printed array the payload actually uses.
 */
export function previewLogExport(
  entries: readonly ExportableLogEntry[],
  opts: LogExportOptions,
): string[] {
  if (entries.length === 0) return []
  switch (opts.format) {
    case 'json':
      return [JSON.stringify(jsonEntry(entries[0]))]
    case 'csv': {
      const columns = csvColumns(entries)
      return [columns.join(','), csvRow(entries[0], columns)]
    }
    default:
      return entries.slice(0, 2).map(e => textLine(e, opts))
  }
}
