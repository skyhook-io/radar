import { useQuery } from '@tanstack/react-query'
import { getApiBase } from './config'
import { fetchJSON, useRadarFeature } from './client'
import { connectionHeaders, connectionHeaderValue } from './connectionBinding'

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

export function usePodDebugAccess(namespace: string, name: string, uid: string, enabled: boolean) {
  const { support } = useRadarFeature('podDebugAccess')
  const base = getApiBase()
  const connection = connectionHeaderValue()
  const query = useQuery({
    queryKey: ['pod-debug-access', base, connection, namespace, name, uid],
    queryFn: () => fetchJSON<PodDebugAccess>(`/pods/${namespace}/${name}/debug-access`, {
      headers: connectionHeaders(connection),
    }),
    enabled: enabled && support === 'supported',
    staleTime: 15_000,
    retry: false,
    refetchOnWindowFocus: false,
  })
  return { query, support }
}
