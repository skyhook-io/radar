import type { HealthLevel } from './resource-utils'
import { KNOWN_NEGATIVE_CONDITIONS, KNOWN_POSITIVE_CONDITIONS } from './generic-status'

// Vendor-declared table columns from a CRD's additionalPrinterColumns,
// evaluated server-side (see internal/server/printer_columns.go) and shipped
// with the list response. The pure parts live here so they're unit-tested;
// the JSX render is materialized in ResourcesView.

/** One column definition, as the CRD declared it. */
export interface PrinterColumnDef {
  name: string
  type?: string
  format?: string
  /** The vendor's own text. Absent for most CRDs, which declare none. */
  description?: string
  /** kubectl's `-o wide` tier. Declared, but not shown by default. */
  priority?: number
}

export interface PrinterTable {
  /** The kind these columns describe, echoed by the server. */
  kind?: string
  group?: string
  columns: PrinterColumnDef[]
  /** metadata.uid -> one value per entry in `columns`, same order. */
  cells: Record<string, unknown[]>
}

/**
 * Stable identity for a column set. Printer columns are fixed per CRD version,
 * so this string only changes when the kind changes or an operator is upgraded
 * mid-session — which is exactly when the visibility defaults should be
 * recomputed, and never on an ordinary refetch.
 */
export function printerTableKey(table: PrinterTable | null | undefined): string {
  if (!table?.columns?.length) return ''
  // Priority is part of the identity, not just the name: default visibility is
  // derived from it, so an operator upgrade that promotes a column out of the
  // `-o wide` tier has to re-run the visibility defaults. Serialized rather
  // than concatenated because column names are vendor text that can contain
  // the separators — real ones already carry spaces ("VERIFY IMAGES").
  return JSON.stringify(table.columns.map(c => [c.name, c.priority ?? 0]))
}

/**
 * Column key. Prefixed so it can never collide with a built-in column key or
 * with a user-defined `label:`/`annotation:` custom column. Build-only — never
 * parsed back into a name.
 */
export const PRINTER_COLUMN_PREFIX = 'printer:'

export function printerColumnKey(def: PrinterColumnDef): string {
  return `${PRINTER_COLUMN_PREFIX}${def.name}`
}

/**
 * Drops anything malformed. The server owns this shape, but it crosses the
 * wire, and a bad entry would otherwise produce a dead column or crash a map.
 */
export function sanitizePrinterTable(raw: any): PrinterTable | null {
  if (!raw || !Array.isArray(raw.columns) || raw.columns.length === 0) return null
  const seen = new Set<string>()
  const columns: PrinterColumnDef[] = []
  const keptIdx: number[] = []
  raw.columns.forEach((c: any, i: number) => {
    if (!c || typeof c.name !== 'string') return
    const name = c.name.trim()
    if (!name || seen.has(name)) return
    seen.add(name)
    columns.push({
      name,
      type: typeof c.type === 'string' ? c.type : undefined,
      format: typeof c.format === 'string' ? c.format : undefined,
      description: typeof c.description === 'string' ? c.description : undefined,
      priority: typeof c.priority === 'number' ? c.priority : 0,
    })
    keptIdx.push(i)
  })
  if (!columns.length) return null

  const cells: Record<string, unknown[]> = {}
  const rawCells = raw.cells && typeof raw.cells === 'object' ? raw.cells : {}
  for (const uid of Object.keys(rawCells)) {
    const row = rawCells[uid]
    if (!Array.isArray(row)) continue
    // Realign to the surviving columns, or a dropped duplicate would shift
    // every later value into the wrong column.
    cells[uid] = keptIdx.map(i => row[i])
  }
  return {
    kind: typeof raw.kind === 'string' ? raw.kind : undefined,
    group: typeof raw.group === 'string' ? raw.group : undefined,
    columns,
    cells,
  }
}

/**
 * Whether a table describes the kind currently selected. The selected kind
 * changes before the new list resolves, so an unchecked table can briefly be
 * the PREVIOUS kind's — which the column-visibility effect would then persist
 * under the new kind's storage key.
 */
export function printerTableMatchesKind(
  table: PrinterTable | null | undefined,
  kind: string,
  group: string | undefined,
): boolean {
  if (!table) return false
  // A server that sent no identity cannot be paired safely.
  if (!table.kind) return false
  // The server echoes the lowercased request path segment, so compare
  // case-insensitively rather than relying on the caller's plural already
  // being lowercase — a deep link that discovery could not resolve isn't.
  return table.kind.toLowerCase() === kind.toLowerCase() && (table.group ?? '') === (group ?? '')
}

export function readPrinterCell(table: PrinterTable, uid: string | undefined, index: number): unknown {
  if (!uid) return undefined
  const row = table.cells[uid]
  return Array.isArray(row) ? row[index] : undefined
}

/**
 * Display text for one cell. Every value is a scalar by the time it gets here:
 * the table converter humanizes `date` cells to "5m", coerces the numeric
 * types, and JSON-encodes a `string` column whose path lands on an object or
 * array — so a badly-aimed JSONPath arrives as a blob like `{"foo":"bar"}`
 * rather than as structure. Non-scalars can therefore only come from a
 * malformed response, and those read as absent.
 */
export function formatPrinterCell(value: unknown): string {
  if (value === null || value === undefined) return ''
  if (typeof value === 'boolean') return value ? 'True' : 'False'
  if (typeof value === 'number') return String(value)
  if (typeof value === 'string') return value
  return ''
}

/**
 * Seconds represented by a Kubernetes human-readable duration ("5m", "2d3h",
 * "1y64d"), or null if it isn't one. The table converter emits these for `date`
 * columns, and sorting them as text puts "10m" before "2m".
 */
const DURATION_UNIT_SECONDS: Record<string, number> = { s: 1, m: 60, h: 3600, d: 86400, y: 31536000 }

export function parseHumanDuration(text: string): number | null {
  const trimmed = text.trim()
  if (!trimmed || !/^(\d+[smhdy])+$/.test(trimmed)) return null
  let total = 0
  for (const [, amount, unit] of trimmed.matchAll(/(\d+)([smhdy])/g)) {
    total += Number(amount) * DURATION_UNIT_SECONDS[unit]
  }
  return total
}

/** Sort key. Numbers and booleans sort by value, not by their rendered text. */
export function printerCellSortValue(value: unknown, def?: PrinterColumnDef): string | number {
  if (typeof value === 'number') return value
  if (typeof value === 'boolean') return value ? 1 : 0
  if (typeof value === 'string') {
    if (def?.type === 'date') {
      const seconds = parseHumanDuration(value)
      // A date cell holds an age, so a bigger number is an OLDER object —
      // the reverse of the Age column's timestamp. Negating restores the
      // shared meaning: ascending puts the oldest first in both.
      if (seconds !== null) return -seconds
    }
    return value
  }
  return ''
}

/**
 * Width class for a column. Printer columns carry no width hint, so this is
 * derived from the declared type: scalars are narrow, strings need room.
 */
export function printerColumnWidth(def: PrinterColumnDef): string {
  switch (def.type) {
    case 'integer':
    case 'number':
    case 'boolean':
      return 'w-24'
    case 'date':
      return 'w-28'
    default:
      return 'w-40'
  }
}

/**
 * Column names whose polarity is a convention rather than a guess. Shared with
 * the condition sets in generic-status.ts so the two can't drift, plus the
 * names CRDs actually use for the same idea in a printer column.
 *
 * This is the one place Radar interprets vendor data rather than passing it
 * through. It is deliberately narrow: the vendor NAMED the column, which is a
 * much stronger signal than inferring health from a value. Anything not on
 * these lists renders as plain text with no health claim.
 */
const POSITIVE_COLUMN_NAMES = new Set(
  [...KNOWN_POSITIVE_CONDITIONS, 'ReadyToUse', 'Established', 'Installed', 'Provided']
    .map(n => n.toLowerCase()),
)
// Split by severity, not just polarity. generic-status.ts tones a true
// KNOWN_NEGATIVE_CONDITION as `degraded` (amber); collapsing these to red here
// would make the same `Degraded=True` amber in the generic Status column and
// red in a printer column.
const DEGRADED_COLUMN_NAMES = new Set(KNOWN_NEGATIVE_CONDITIONS.map(n => n.toLowerCase()))
const TERMINAL_COLUMN_NAMES = new Set(['denied', 'failed'])

/** The three states a boolean-ish printer cell can hold, however it is typed. */
function truthiness(value: unknown): 'true' | 'false' | 'unknown' | null {
  if (typeof value === 'boolean') return value ? 'true' : 'false'
  if (typeof value !== 'string') return null
  switch (value.trim().toLowerCase()) {
    case 'true': return 'true'
    case 'false': return 'false'
    case 'unknown': return 'unknown'
    default: return null
  }
}

/**
 * Health tone for a printer cell, or null to render it as plain text.
 *
 * Only fires when the column's NAME carries a known polarity and its VALUE is
 * True/False/Unknown — the shape a condition-derived printer column has. A
 * column named `Healthy` holding `True` is the vendor telling us this row is
 * fine; rendering that as grey text throws away the only scannable signal on
 * the kinds that have no curated Status column.
 */
export function printerCellTone(def: PrinterColumnDef, value: unknown): HealthLevel | null {
  const state = truthiness(value)
  if (!state) return null
  // The NAME decides whether we say anything at all — including for Unknown.
  // Checking the value first would badge `Mode: Unknown` on a column whose
  // polarity we have no claim to.
  const name = def.name.trim().toLowerCase()
  const positive = POSITIVE_COLUMN_NAMES.has(name)
  const degraded = DEGRADED_COLUMN_NAMES.has(name)
  const terminal = TERMINAL_COLUMN_NAMES.has(name)
  if (!positive && !degraded && !terminal) return null
  // Unknown is not a failure — the controller could not evaluate it.
  if (state === 'unknown') return 'unknown'
  if (positive) return state === 'true' ? 'healthy' : 'unhealthy'
  if (degraded) return state === 'true' ? 'degraded' : 'healthy'
  return state === 'true' ? 'unhealthy' : 'healthy'
}
