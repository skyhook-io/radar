import { createContext, useCallback, useContext, useEffect, useMemo, useRef, useState, type ReactNode } from 'react'
import {
  decodeFilters,
  isFilterActive,
  withCleared,
  withField,
  withToggle,
  emptyValue,
  type FieldKeysOfType,
  type FilterSchema,
  type FilterValues,
} from './filter-state-core'

// Shared filter-state contract for Radar's list views (OSS + Hub).
//
// The consolidation center is BEHAVIOR, not components: one place owns how a
// view's filters serialize to the URL, what "cleared" means, and how defaults
// encode — so every list view is shareable/bookmarkable and behaves the same,
// whether it renders as a facet sidebar or a compact bar.
//
// URL is the single source of truth (no localStorage of filter values — that
// goes stale across clusters/orgs). Router-agnostic + app-agnostic: this never
// imports react-router or reads window; the host injects a FilterLocation
// adapter over its own router. All transforms live in ./filter-state-core (pure,
// exhaustively tested); this file is only the React glue.

/** A reactive view of the URL query string, injected by the host app. */
export interface FilterLocation {
  /** Current query params — reactive (sourced from the app's router hook). */
  searchParams: URLSearchParams
  /**
   * Apply an update. The updater receives the LATEST params (not a captured
   * snapshot), so rapid successive toggles compose instead of clobbering.
   * `replace` avoids a history entry per keystroke (used for text fields).
   */
  update: (updater: (prev: URLSearchParams) => URLSearchParams, opts?: { replace?: boolean }) => void
}

const FilterLocationContext = createContext<FilterLocation | null>(null)

/**
 * Wrap the app subtree once with the host's router adapter, e.g.
 *   const [searchParams, setSearchParams] = useSearchParams()
 *   <FilterLocationProvider value={{ searchParams, update: setSearchParams }}>
 */
export function FilterLocationProvider({ value, children }: { value: FilterLocation; children: ReactNode }) {
  return <FilterLocationContext.Provider value={value}>{children}</FilterLocationContext.Provider>
}

export function useFilterLocation(): FilterLocation {
  const ctx = useContext(FilterLocationContext)
  if (!ctx) throw new Error('useFilterState requires a <FilterLocationProvider> ancestor')
  return ctx
}

export interface FilterState<S extends FilterSchema> {
  values: FilterValues<S>
  /** Any field diverges from its default (⇒ show a "Clear all" affordance). */
  isActive: boolean
  /** Add/remove one value in a 'set' field. */
  toggle: (key: FieldKeysOfType<S, 'set'>, value: string) => void
  /** Replace a 'set' field wholesale. */
  setSet: (key: FieldKeysOfType<S, 'set'>, values: string[]) => void
  /** Set a 'text' or 'single' field. 'text' replaces history; 'single' pushes it. */
  setString: (key: FieldKeysOfType<S, 'text' | 'single'>, value: string) => void
  setBoolean: (key: FieldKeysOfType<S, 'boolean'>, value: boolean) => void
  /** Reset one field to its default. */
  clear: (key: keyof S) => void
  /** Reset every field in the schema (leaves unrelated params untouched). */
  clearAll: () => void
}

export function useFilterState<S extends FilterSchema>(schema: S): FilterState<S> {
  const ctx = useContext(FilterLocationContext)

  // Graceful fallback: without a provider, keep filter state locally (still
  // works, just not URL-synced). This lets a shared view adopt the contract
  // before every host has wired a FilterLocationBridge — a host that hasn't
  // (e.g. Radar Hub, until it picks up a published build) keeps its prior local
  // behavior instead of crashing. A one-time warning flags the missing wiring.
  const [localParams, setLocalParams] = useState(() => new URLSearchParams())
  const warned = useRef(false)
  useEffect(() => {
    if (!ctx && !warned.current) {
      warned.current = true
      console.warn('[k8s-ui] useFilterState: no <FilterLocationProvider> ancestor — filters are local, not URL-synced.')
    }
  }, [ctx])
  const fallback = useMemo<FilterLocation>(
    () => ({ searchParams: localParams, update: (u) => setLocalParams((prev) => u(new URLSearchParams(prev))) }),
    [localParams],
  )
  const { searchParams, update } = ctx ?? fallback

  // Depend on the serialized string, not the URLSearchParams object identity, so
  // a router adapter that returns a mutated/unstable instance can't stale the memo.
  const paramsKey = searchParams.toString()
  const values = useMemo(() => decodeFilters(schema, new URLSearchParams(paramsKey)), [schema, paramsKey])
  const isActive = useMemo(() => isFilterActive(schema, values), [schema, values])

  const toggle = useCallback(
    (key: FieldKeysOfType<S, 'set'>, value: string) => update((prev) => withToggle(schema, prev, key, value)),
    [schema, update],
  )
  const setSet = useCallback(
    (key: FieldKeysOfType<S, 'set'>, vals: string[]) => update((prev) => withField(schema, prev, key, new Set(vals))),
    [schema, update],
  )
  const setString = useCallback(
    (key: FieldKeysOfType<S, 'text' | 'single'>, v: string) =>
      // Typing into a search box shouldn't spam history; picking an enum should be
      // a real back-step. 'text' replaces, 'single' pushes.
      update((prev) => withField(schema, prev, key, v), { replace: schema[key].type === 'text' }),
    [schema, update],
  )
  const setBoolean = useCallback(
    (key: FieldKeysOfType<S, 'boolean'>, v: boolean) => update((prev) => withField(schema, prev, key, v)),
    [schema, update],
  )
  const clear = useCallback(
    (key: keyof S) => update((prev) => withField(schema, prev, key, emptyValue(schema[key]))),
    [schema, update],
  )
  const clearAll = useCallback(() => update((prev) => withCleared(schema, prev)), [schema, update])

  return { values, isActive, toggle, setSet, setString, setBoolean, clear, clearAll }
}
