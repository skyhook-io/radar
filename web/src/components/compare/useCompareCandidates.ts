import { useMemo } from 'react'
import type { CompareResourceRef } from '@skyhook-io/k8s-ui'
import { useResources } from '../../api/client'

/**
 * Fetch candidates for the compare picker — same kind as the source.
 * Pass `enabled=false` when the picker is closed to avoid hitting the API.
 */
export function useCompareCandidates(kind: string, group: string | undefined, enabled: boolean) {
  const query = useResources<{ metadata?: { name?: string; namespace?: string } }>(
    enabled ? kind : '',
    undefined,
    group,
  )
  const candidates: CompareResourceRef[] = useMemo(() => {
    if (!query.data) return []
    return query.data
      .filter(r => r?.metadata?.name)
      .map(r => ({
        kind,
        namespace: r.metadata?.namespace ?? '',
        name: r.metadata!.name!,
        group,
      }))
  }, [query.data, kind, group])
  return { candidates, isPending: query.isPending, error: query.error }
}
