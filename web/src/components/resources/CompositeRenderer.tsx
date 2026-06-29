import { useMemo } from 'react'
import { useQueries } from '@tanstack/react-query'
import {
  CompositeRenderer as BaseCompositeRenderer,
  type ComposedRefStatus,
} from '@skyhook-io/k8s-ui/components/resources/renderers/CompositeRenderer'
import {
  getCrossplaneResourceRefs,
  type CrossplaneResourceRef,
} from '@skyhook-io/k8s-ui/components/resources/resource-utils-crossplane'
import { getResourceStatus } from '@skyhook-io/k8s-ui'
import { kindToPlural } from '@skyhook-io/k8s-ui/utils/navigation'
import type { ResourceRef as NavRef } from '../../types'
import { fetchJSON, ApiError } from '../../api/client'

interface CompositeRendererProps {
  data: any
  onNavigate?: (ref: NavRef) => void
}

function makeRefKey(ref: CrossplaneResourceRef): string {
  return `${ref.apiVersion}/${ref.kind}/${ref.namespace ?? ''}/${ref.name}`
}

// Crossplane refs carry full apiVersion (e.g. "s3.aws.upbound.io/v1beta1")
// because plural collisions across provider groups are real — two providers
// can ship CRDs with the same kind. Drop the version, keep the group.
function groupFromApiVersion(apiVersion: string | undefined): string {
  if (!apiVersion) return ''
  const slash = apiVersion.indexOf('/')
  return slash < 0 ? '' : apiVersion.slice(0, slash)
}

/**
 * Host wrapper for the package's CompositeRenderer: fans out a React Query
 * lookup per composed resource ref so each row in the composed-resources
 * list can show a live status badge.
 *
 * Errors are folded into the per-ref status entry rather than thrown — a
 * missing composed resource (e.g. 404 because reconciliation hasn't created
 * it yet) is a normal state for a freshly-applied Composite, not a failure.
 */
export function CompositeRenderer({ data, onNavigate }: CompositeRendererProps) {
  const refs = useMemo<CrossplaneResourceRef[]>(() => getCrossplaneResourceRefs(data), [data])

  const queries = useQueries({
    queries: refs.map(ref => {
      const group = groupFromApiVersion(ref.apiVersion)
      return {
        queryKey: ['composed-ref', group, ref.kind, ref.namespace ?? '', ref.name],
        queryFn: async () => {
          const ns = ref.namespace || '_'
          const plural = kindToPlural(ref.kind)
          const query = group ? `?group=${encodeURIComponent(group)}` : ''
          return fetchJSON<{ resource: any }>(`/resources/${plural}/${ns}/${ref.name}${query}`)
        },
        staleTime: 30000,
        retry: false,
        enabled: Boolean(ref.kind && ref.name),
      }
    }),
  })

  const composedRefStatuses = useMemo<Map<string, ComposedRefStatus>>(() => {
    const map = new Map<string, ComposedRefStatus>()
    refs.forEach((ref, i) => {
      const q = queries[i]
      const key = makeRefKey(ref)
      if (q.isLoading) {
        map.set(key, { ref, loading: true })
        return
      }
      if (q.isError) {
        // 404 = the composed resource genuinely doesn't exist yet (normal
        // during reconciliation). Any other failure (401/403/500/network)
        // means we can't tell the operator the resource's status — render
        // it distinctly so they don't read "missing" and think the resource
        // is absent when they've actually just lost permission to read it.
        if (q.error instanceof ApiError && q.error.status === 404) {
          map.set(key, { ref, missing: true })
        } else {
          const message = q.error instanceof Error ? q.error.message : 'Failed to fetch composed resource'
          map.set(key, { ref, error: true, errorMessage: message })
        }
        return
      }
      if (!q.data) {
        map.set(key, { ref, missing: true })
        return
      }
      const status = getResourceStatus(kindToPlural(ref.kind), q.data.resource) ?? undefined
      map.set(key, {
        ref,
        status: status ? { ...status, level: (status as any).level ?? 'unknown' } : undefined,
      })
    })
    return map
  }, [refs, queries])

  return <BaseCompositeRenderer data={data} onNavigate={onNavigate} composedRefStatuses={composedRefStatuses} />
}
