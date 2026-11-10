import { sanitizePrinterTable, type PrinterTable } from '@skyhook-io/k8s-ui/components/resources/printer-columns'
import type { ResourceCoverage, ResourceListResponse } from '@skyhook-io/k8s-ui/types/resource-coverage'
import { isLowerBound } from '@skyhook-io/k8s-ui/utils/resource-coverage'

export interface ResourceListResult<T> {
  items: T[]
  coverage: ResourceCoverage
  printerTable: PrinterTable | null
}

/**
 * Every list body a supported Radar sends: the coverage envelope, or from a
 * Radar that predates coverage a bare array, or for `?table=1` the printer
 * envelope without `coverage`.
 */
export type ResourceListBody<T> =
  | ResourceListResponse<T>
  | (Omit<ResourceListResponse<T>, 'coverage'> & { coverage?: undefined })
  | T[]

/**
 * Reads a list body by its shape rather than by the cluster's advertised
 * features: a host can point one query client at several clusters, so a cached
 * capabilities answer may describe a different Radar than the one that
 * answered. A response without coverage can't establish absence, so it reads
 * as `unknown`.
 */
export function normalizeResourceList<T>(body: ResourceListBody<T>): ResourceListResult<T> {
  if (Array.isArray(body)) return { items: body, coverage: { status: 'unknown' }, printerTable: null }
  return { items: body.items, coverage: body.coverage ?? { status: 'unknown' }, printerTable: sanitizePrinterTable(body) }
}

/** Back off once per loading response, resetting for a new query or settled coverage. */
export function createListRefetchInterval(normalInterval?: number | false) {
  type ListQuery = { state: { data?: { coverage: ResourceCoverage }; dataUpdateCount: number } }
  let previousQuery: ListQuery | undefined
  let previousUpdate = -1
  let delay = 0
  return (query: ListQuery): number | false | undefined => {
    if (query !== previousQuery || !listSyncPending(query.state.data?.coverage)) {
      delay = 0
      previousUpdate = -1
      previousQuery = query
    }
    if (!listSyncPending(query.state.data?.coverage)) return normalInterval
    if (query.state.dataUpdateCount !== previousUpdate) {
      delay = delay === 0 ? 5000 : Math.min(delay * 2, 30000)
      previousUpdate = query.state.dataUpdateCount
    }
    return delay
  }
}

export function listSyncPending(coverage: ResourceCoverage | undefined): boolean {
  return coverage?.status === 'partial' && coverage.gaps.some(gap => gap.code === 'kind_sync_pending')
}

/**
 * One status for a view assembled from several list reads. A failed read makes
 * it partial, and so does one Radar's collection narrowed: the view shows
 * totals for the cluster, which a read of only the collected namespaces can't
 * establish. `nothingToRead` says discovery found no kind to read, which is
 * itself a complete answer.
 */
export function combinedListStatus(coverage: ResourceCoverage[], failed: boolean, nothingToRead: boolean): 'complete' | 'partial' | 'unknown' {
  if (failed || coverage.some(isLowerBound)) return 'partial'
  if (coverage.length === 0 ? nothingToRead : coverage.every(c => c.status === 'complete')) return 'complete'
  return 'unknown'
}

export { isLowerBound as lowerBound } from '@skyhook-io/k8s-ui/utils/resource-coverage'
