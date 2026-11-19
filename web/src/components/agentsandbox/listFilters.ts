import { useCallback, useMemo } from 'react'
import { useSearchParams } from 'react-router-dom'
import { columnFiltersParam, getCellFilterValue, kindFiltersFromSearch } from '@skyhook-io/k8s-ui/components/resources/resource-utils'
import { getCellFilterKind, getColumnsForKind } from '@skyhook-io/k8s-ui/components/resources/ResourcesView'
import type { HostKind } from '../../integrations/resourceHost'
import type { KindRead } from './data'

export interface ColumnFilter { key: string; label: string; values: string[]; exclude: boolean }

/** A Resources-page filter in the URL that these lists don't apply, and the params that clear it. */
export interface IgnoredFilter { label: string; params: string[]; column?: string }

/**
 * The Resources page's own list filters in a query string, as they bear on
 * one of these lists: the `filters` columns the kind's standard table has
 * (applied below, matched against the text that table filters on), and the
 * rest, which these lists name rather than apply.
 */
export function readListFilters(search: string, kind: HostKind): { columns: ColumnFilter[]; ignored: IgnoredFilter[] } {
  const params = new URLSearchParams(search)
  const { columnFilters, columnFilterExcludes, problemFilters } = kindFiltersFromSearch(search)
  const labels = new Map(getColumnsForKind(kind.name, kind.group).map((c) => [c.key, c.label]))
  const columns: ColumnFilter[] = []
  const ignored: IgnoredFilter[] = []
  for (const [key, values] of Object.entries(columnFilters)) {
    const label = labels.get(key)
    if (label) columns.push({ key, label, values, exclude: !!columnFilterExcludes[key] })
    else ignored.push({ label: `${key}: ${values.join(', ')}`, params: ['filters'], column: key })
  }
  const labelSelector = params.get('labels')
  if (labelSelector) ignored.push({ label: `Labels: ${labelSelector}`, params: ['labels'] })
  const ownerKind = params.get('ownerKind')
  const ownerName = params.get('ownerName')
  if (ownerKind || ownerName) ignored.push({ label: `Owner: ${[ownerKind, ownerName].filter(Boolean).join('/')}`, params: ['ownerKind', 'ownerName'] })
  if (problemFilters.length > 0) ignored.push({ label: `Problems: ${problemFilters.join(', ')}`, params: ['problems'] })
  if (params.get('regex') === 'true') ignored.push({ label: 'Regex search', params: ['regex'] })
  return { columns, ignored }
}

/** Keeps the objects every column filter admits: any of its values, or none of them when it excludes. */
export function applyColumnFilters(read: KindRead, columns: ColumnFilter[], kind: HostKind): KindRead {
  if (read.state !== 'read' || columns.length === 0) return read
  const cellKind = getCellFilterKind(kind.name, kind.group)
  const items = read.items.filter((o) =>
    columns.every((c) => {
      const match = c.values.includes(getCellFilterValue(o, c.key, cellKind))
      return c.exclude ? !match : match
    }),
  )
  return { ...read, items }
}

/** The query string without one column of the `filters` param. */
export function withoutColumn(search: string, key: string): URLSearchParams {
  const params = new URLSearchParams(search)
  const { columnFilters, columnFilterExcludes } = kindFiltersFromSearch(search)
  delete columnFilters[key]
  const next = columnFiltersParam(columnFilters, columnFilterExcludes)
  if (next) params.set('filters', next)
  else params.delete('filters')
  return params
}

/**
 * The URL's list filters for this kind: a function narrowing a read by its
 * column filters, and a chip for each, with the filters it doesn't apply
 * named as such so a link's narrowing is never silently dropped.
 */
export function useListFilters(kind: HostKind): { apply: (read: KindRead) => KindRead; narrows: boolean; chips: { label: string; onClear: () => void }[] } {
  const [params, setParams] = useSearchParams()
  const search = params.toString()
  const { columns, ignored } = useMemo(() => readListFilters(search, kind), [search, kind])
  const apply = useCallback((read: KindRead) => applyColumnFilters(read, columns, kind), [columns, kind])
  const replace = (next: URLSearchParams) => setParams(next, { replace: true })
  const drop = (keys: string[]) => {
    const next = new URLSearchParams(search)
    for (const k of keys) next.delete(k)
    replace(next)
  }
  const chips = [
    ...columns.map((c) => ({ label: `${c.label}${c.exclude ? ' excludes' : ''}: ${c.values.join(', ')}`, onClear: () => replace(withoutColumn(search, c.key)) })),
    ...ignored.map((f) => ({ label: `${f.label} (not applied here)`, onClear: () => (f.column ? replace(withoutColumn(search, f.column)) : drop(f.params)) })),
  ]
  return { apply, narrows: columns.length > 0, chips }
}
