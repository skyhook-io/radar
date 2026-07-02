// Pure URL <-> filter-state transforms. No React, no router, no window — just
// URLSearchParams in, URLSearchParams / typed values out. This is the behavioral
// contract every list view shares; keeping it pure makes it exhaustively
// testable and keeps the React hook a thin wrapper.

export interface FilterFieldDef {
  /** URL query-param name. Stable — this is a durable, shareable public API. */
  param: string
  /**
   * - 'set'     multi-select; comma-list. Empty ⇒ param omitted ⇒ "all" (no
   *             narrowing). Values must not contain commas (k8s identifiers and
   *             controlled enums never do). Written sorted, so the URL is
   *             canonical regardless of selection order. Value: Set<string>.
   * - 'text'    free-text search; written with history `replace` (no entry per
   *             keystroke). Omitted when empty / equal to `default`. Value: string.
   * - 'single'  single choice (enum / dropdown, incl. tri-state all|yes|no);
   *             pushes a history entry. Omitted when equal to `default`. Value:
   *             string.
   * - 'boolean' two-state flag; `param=1` when true, omitted when false. For
   *             three states use 'single' with an enum default. Value: boolean.
   */
  type: 'set' | 'text' | 'single' | 'boolean'
  /** Default for 'text'/'single' (omitted from the URL when the value equals it). */
  default?: string
}

export type FilterSchema = Record<string, FilterFieldDef>

type FieldValue<F extends FilterFieldDef> = F['type'] extends 'set'
  ? Set<string>
  : F['type'] extends 'boolean'
    ? boolean
    : string

export type FilterValues<S extends FilterSchema> = { [K in keyof S]: FieldValue<S[K]> }

/** Keys of a schema whose field is one of the given kinds — for type-safe setters. */
export type FieldKeysOfType<S extends FilterSchema, T extends FilterFieldDef['type']> = {
  [K in keyof S]: S[K]['type'] extends T ? K : never
}[keyof S]

/**
 * Identity helper that preserves literal types (so `values.x` is precisely typed)
 * and validates the schema — duplicate URL params would silently clobber each
 * other, so we fail loudly at module load.
 */
export function defineFilterSchema<const S extends FilterSchema>(schema: S): S {
  const seen = new Set<string>()
  for (const key in schema) {
    const { param } = schema[key]
    if (seen.has(param)) throw new Error(`defineFilterSchema: duplicate URL param "${param}"`)
    seen.add(param)
  }
  return schema
}

export function decodeFilters<S extends FilterSchema>(schema: S, params: URLSearchParams): FilterValues<S> {
  const out = {} as FilterValues<S>
  for (const key in schema) {
    const def = schema[key]
    const raw = params.get(def.param)
    if (def.type === 'set') {
      ;(out[key] as Set<string>) = new Set((raw ?? '').split(',').filter(Boolean))
    } else if (def.type === 'boolean') {
      ;(out[key] as boolean) = raw === '1'
    } else {
      // text / single: an empty param (?f=) reads as the default, same as omission.
      ;(out[key] as string) = raw ? raw : (def.default ?? '')
    }
  }
  return out
}

/** Write one field into `params` in place. Read (decode) and write agree exactly. */
function writeField(params: URLSearchParams, def: FilterFieldDef, value: Set<string> | string | boolean): void {
  if (def.type === 'set') {
    const set = value as Set<string>
    if (set.size === 0) params.delete(def.param)
    else params.set(def.param, [...set].sort().join(','))
  } else if (def.type === 'boolean') {
    if (value) params.set(def.param, '1')
    else params.delete(def.param)
  } else {
    const str = value as string
    if (!str || str === (def.default ?? '')) params.delete(def.param)
    else params.set(def.param, str)
  }
}

/** Return new params with one field set to `value`. */
export function withField<S extends FilterSchema>(
  schema: S,
  params: URLSearchParams,
  key: keyof S,
  value: Set<string> | string | boolean,
): URLSearchParams {
  const next = new URLSearchParams(params)
  writeField(next, schema[key], value)
  return next
}

/** Return new params with one member added to / removed from a 'set' field. */
export function withToggle<S extends FilterSchema>(
  schema: S,
  params: URLSearchParams,
  key: keyof S,
  value: string,
): URLSearchParams {
  const next = new URLSearchParams(params)
  const cur = new Set((next.get(schema[key].param) ?? '').split(',').filter(Boolean))
  if (cur.has(value)) cur.delete(value)
  else cur.add(value)
  writeField(next, schema[key], cur)
  return next
}

/** Default (empty) value for a field, used to reset it. */
export function emptyValue(def: FilterFieldDef): Set<string> | string | boolean {
  return def.type === 'set' ? new Set<string>() : def.type === 'boolean' ? false : (def.default ?? '')
}

/** Return new params with the given schema fields cleared (all of them if omitted). */
export function withCleared<S extends FilterSchema>(schema: S, params: URLSearchParams, keys?: (keyof S)[]): URLSearchParams {
  const next = new URLSearchParams(params)
  for (const key of keys ?? (Object.keys(schema) as (keyof S)[])) next.delete(schema[key].param)
  return next
}

export function isFilterActive<S extends FilterSchema>(schema: S, values: FilterValues<S>): boolean {
  for (const key in schema) {
    const def = schema[key]
    const v = values[key]
    if (def.type === 'set' && (v as Set<string>).size > 0) return true
    if (def.type === 'boolean' && (v as boolean)) return true
    if ((def.type === 'text' || def.type === 'single') && (v as string) !== (def.default ?? '')) return true
  }
  return false
}
