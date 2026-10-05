import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import type { DatumWorkspace } from '@skyhook-io/k8s-ui/components/datum/workspace'
import { apiFetch, fetchJSON, useRadarFeature } from './client'
import { getApiBase } from './config'
import { shouldRetryRadarQuery } from './radarFeatures'

export function useDatumWorkspace(namespaces: string[], enabled = true) {
  const { guard, gatedKey } = useRadarFeature('datumWorkspace')
  const ns = [...namespaces].sort().join(',')
  return useQuery<DatumWorkspace>({
    queryKey: ['datum', 'workspace', ns, ...gatedKey],
    queryFn: ({ signal }) =>
      guard(() =>
        fetchJSON<DatumWorkspace>(
          `/datum/workspace${ns ? `?namespaces=${encodeURIComponent(ns)}` : ''}`,
          signal,
        ),
      ),
    enabled,
    staleTime: 10_000,
    refetchInterval: 30_000,
    retry: shouldRetryRadarQuery,
  })
}
export function useOpenDatumProject(onConnected: () => void) {
  const { guard } = useRadarFeature('datumWorkspace')
  const cache = useQueryClient()
  return useMutation({
    mutationFn: ({
      name,
      uid,
      reviewedContext,
    }: {
      name: string
      uid: string
      reviewedContext: string
    }) =>
      guard(async () => {
        const res = await apiFetch(
          `${getApiBase()}/datum/projects/${encodeURIComponent(name)}/connect`,
          {
            method: 'POST',
            body: JSON.stringify({ uid, reviewedContext }),
            headers: { 'Content-Type': 'application/json' },
          },
        )
        if (!res.ok) {
          const body = await res.json()
          throw new Error(body.error)
        }
        return res.json() as Promise<{ context: string; status: string }>
      }),
    onSuccess: () => {
      onConnected()
      cache.removeQueries()
      cache.invalidateQueries()
    },
    onError: () => {
      cache.invalidateQueries()
    },
    meta: {
      errorMessage: 'Could not open project control plane',
      successMessage: 'Project control plane connected',
    },
  })
}
