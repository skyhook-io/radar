import type { ReadGap, ReadNameSet, ReadNarrowing } from './read-coverage'

export interface ResourceReadScope {
  type: 'cluster' | 'all_namespaces' | 'namespaces'
  namespaces?: ReadNameSet
}

/**
 * A single-kind read's coverage. `status` is relative to `scope`: the
 * namespaces the request named, or what remained of every namespace after
 * `narrowedBy` (the caller's access, Radar's pin, what Radar collects) narrowed it.
 */
export interface ResourceReadCoverage {
  status: 'complete' | 'partial'
  scope: ResourceReadScope
  narrowedBy?: ReadNarrowing[]
  /** What Radar read; on every successful answer, never on a failed read's coverage. */
  read?: ResourceReadScope
  gaps: ReadGap[]
}

export type ResourceCoverage = ResourceReadCoverage | { status: 'unknown' }

export interface ResourceListResponse<T> {
  items: T[]
  coverage: ResourceReadCoverage
  /** The lowercased plural from the request path, not a Kubernetes Kind. */
  kind?: string
  /** Omitted for the core group. */
  group?: string
  columns?: import('../components/resources/printer-columns').PrinterColumnDef[] | null
  cells?: Record<string, unknown[]> | null
}

/** A failed resource read's body, on REST and in MCP results alike. */
export interface ResourceReadErrorBody {
  error: string
  error_code: string
  coverage?: ResourceReadCoverage
}
