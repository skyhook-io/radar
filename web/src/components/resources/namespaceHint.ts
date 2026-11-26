import type { ResourceCoverage } from '@skyhook-io/k8s-ui'

export type EmptyNamespaceHint =
  | { type: 'elsewhere'; count: number; atLeast: boolean }
  | { type: 'none-elsewhere' }

/**
 * What an empty, namespace-filtered list says about the rest of the cluster,
 * from the kind's count across every namespace the caller can read.
 *
 * A count Radar couldn't fully read, or one narrowed by the caller's access,
 * is a lower bound, so it can't back "none in other namespaces".
 */
export function emptyNamespaceHint(total: number | undefined, coverage: ResourceCoverage | undefined): EmptyNamespaceHint | null {
  if (total === undefined) return null
  const complete = coverage?.status === 'complete' && !coverage.narrowedBy?.length
  if (total > 0) return { type: 'elsewhere', count: total, atLeast: !complete }
  return complete ? { type: 'none-elsewhere' } : null
}
