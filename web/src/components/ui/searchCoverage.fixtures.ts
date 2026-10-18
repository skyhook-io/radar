import type { SearchCoverage } from '@skyhook-io/k8s-ui/types/read-coverage'
import type { SearchCoverageEntry } from './searchCoverageNote'

/** The wire coverage that flattens back to entries, for tests. */
export function coverageOf(entries: SearchCoverageEntry[]): SearchCoverage {
  return {
    status: entries.length ? 'partial' : 'complete',
    gaps: entries.map((e) => {
      if (e.kind !== '*') return { code: e.reason, kinds: { count: 1, names: [{ kind: e.kind, group: e.group || undefined }] } }
      if (e.namespaces) return { code: e.reason, namespaces: { count: e.namespaces.length, names: e.namespaces } }
      if (e.count) return { code: e.reason, kinds: { count: e.count } }
      return { code: e.reason }
    }),
  }
}
