import { fetchJSON } from './client'
import { connectionHeaders } from './connectionBinding'

export function createPodDebugContainer(namespace: string, podName: string, connection: string | undefined, request: {
  targetContainer: string
  uid?: string
  image?: string
}) {
  return fetchJSON<{ containerName: string; image: string; status: 'running' | 'pending' }>(
    `/pods/${namespace}/${podName}/debug`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json', ...connectionHeaders(connection) },
      body: JSON.stringify(request),
    },
  )
}
