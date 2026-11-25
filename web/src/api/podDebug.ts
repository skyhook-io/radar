import { useEffect, useMemo } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { getApiBase } from './config'
import { fetchJSON, useRadarFeature } from './client'
import { connectionHeaders, connectionHeaderValue, useCurrentConnection } from './connectionBinding'

export function createPodDebugContainer(namespace: string, podName: string, connection: string | undefined, request: {
  targetContainer: string
  uid: string
  image?: string
}) {
  return fetchJSON<{ containerName: string; image: string; status: 'running' | 'pending' }>(
    `/pods/${namespace}/${podName}/debug`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json', ...connectionHeaders(connection) },
      body: JSON.stringify({ ...request, returnImmediately: true }),
    },
  )
}

type PodDebugAccess = { exec?: boolean } & ({ state: 'allowed'; image: string } | { state: 'denied' | 'unknown' | 'unavailable' })

function refusesDebug(access?: PodDebugAccess) {
  return access?.state === 'denied' || access?.state === 'unavailable'
}

export function podDebugAccessKey(namespace: string, name: string, uid: string, base = getApiBase(), connection = connectionHeaderValue()) {
  return ['pod-debug-access', base, connection, namespace, name, uid]
}

export function usePodDebugAccess(namespace: string, name: string, uid: string, enabled: boolean, execOnly = false, namespaceGrant?: string) {
  const { support } = useRadarFeature('podDebugAccess')
  const client = useQueryClient()
  const base = getApiBase()
  const connection = useCurrentConnection()
  const key = useMemo(() => [...podDebugAccessKey(namespace, name, uid, base, connection), execOnly], [namespace, name, uid, base, connection, execOnly])
  const query = useQuery<PodDebugAccess>({
    queryKey: key,
    queryFn: () => fetchJSON<PodDebugAccess>(`/pods/${namespace}/${name}/debug-access${execOnly ? '?execOnly=true' : ''}`, {
      headers: connectionHeaders(connection),
    }),
    enabled: q => support === 'supported' && (enabled || (!!namespaceGrant && refusesDebug(q.state.data))),
    staleTime: 15_000,
    retry: false,
    refetchOnWindowFocus: true,
  })
  const recheck = query.refetch
  useEffect(() => {
    // A newly visible namespace grant can disagree with a previous named denial.
    // Ask about this Pod again rather than letting either cached answer outvote the other.
    const previous = client.getQueryData<PodDebugAccess>(key)
    if (namespaceGrant && support === 'supported' && refusesDebug(previous)) void recheck({ cancelRefetch: false })
  }, [namespaceGrant, support, client, key, recheck])
  return { query, support, recheckSupport: () => client.invalidateQueries({queryKey: ['capabilities'], exact: true}) }
}
