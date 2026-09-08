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
  /** Mirrors the viewer's pod attribution, which is set by context (workload views have it, single-pod views don't). */
  showPodName: boolean
}

export interface LogExportPayload {
  content: string
  mime: string
  extension: LogExportFormat
}

const MIME: Record<LogExportFormat, string> = {
  txt: 'text/plain',
  json: 'application/json',
  csv: 'text/csv',
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

function jsonEntry(entry: ExportableLogEntry, { showPodName }: LogExportOptions): Record<string, string> {
  return {
    timestamp: entry.timestamp,
    ...(showPodName && entry.pod ? { pod: entry.pod } : {}),
    ...(entry.container ? { container: entry.container } : {}),
    content: stripAnsi(entry.content),
  }
}

function csvColumns({ showPodName }: LogExportOptions): string[] {
  return showPodName ? ['timestamp', 'pod', 'container', 'content'] : ['timestamp', 'container', 'content']
}

function csvCell(value: string): string {
  return `"${value.replace(/"/g, '""')}"`
}

function csvRow(entry: ExportableLogEntry, opts: LogExportOptions): string {
  const cells = [
    entry.timestamp,
    ...(opts.showPodName ? [entry.pod ?? ''] : []),
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
      return { ...base, content: JSON.stringify(entries.map(e => jsonEntry(e, opts)), null, 2) }
    case 'csv':
      return { ...base, content: [csvColumns(opts).join(','), ...entries.map(e => csvRow(e, opts))].join('\n') }
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
      return [JSON.stringify(jsonEntry(entries[0], opts))]
    case 'csv':
      return [csvColumns(opts).join(','), csvRow(entries[0], opts)]
    default:
      return entries.slice(0, 2).map(e => textLine(e, opts))
  }
}
