import type { SearchCoverage } from '@skyhook-io/k8s-ui/types/read-coverage'
import type { SearchCoverageEntry } from './searchCoverageNote'

/** The wire coverage that flattens back to entries, for tests: each gap lists
 *  one axis and gives the other as an extent, as Radar sends it. */
export function coverageOf(entries: SearchCoverageEntry[]): SearchCoverage {
  return {
    status: entries.length ? 'partial' : 'complete',
    gaps: entries.map((e) => {
      if (e.kind !== '*') return { code: e.reason, kinds: { count: 1, names: [{ kind: e.kind, group: e.group || undefined }] }, namespaces: { extent: 'all' } }
      if (e.namespaces) return { code: e.reason, kinds: { extent: 'all' }, namespaces: { count: e.namespaces.length, names: e.namespaces } }
      if (e.count) return { code: e.reason, kinds: { count: e.count }, namespaces: { extent: 'all' } }
      return { code: e.reason, kinds: { extent: 'unknown' }, namespaces: { extent: 'all' } }
    }),
  }
}
