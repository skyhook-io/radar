import { useState, type ReactNode } from 'react'
import { useQuery } from '@tanstack/react-query'
import { LoaderCircle, RefreshCw } from 'lucide-react'
import type { DockTab } from '@skyhook-io/k8s-ui'
import { ApiError, fetchResourceWithRelationships } from '../../api/client'
import { getApiBase } from '../../api/config'
import { connectionHeaderValue, useCurrentConnection } from '../../api/connectionBinding'
import type { PodDebugTarget } from '../resources/PodDebugAction'

type Session = NonNullable<DockTab['debugSession']>

interface Startup {
  state: 'finished' | 'running' | 'waiting' | 'blocked'
  title?: string
  hint?: string
  reason?: string
  message?: string
}

function startup(pod: PodDebugTarget, uid: string, container: string): Startup {
  if (pod.metadata.uid !== uid) return { state: 'finished', title: 'The original Pod was replaced' }
  const status = pod.status.ephemeralContainerStatuses?.find(item => item.name === container)?.state
  if (status?.terminated) {
    const { exitCode, reason, message } = status.terminated
    return { state: 'finished', title: `Debug container exited with code ${exitCode}`, reason, message }
  }
  if (pod.metadata.deletionTimestamp || ['Failed', 'Succeeded'].includes(pod.status.phase ?? '')) {
    return { state: 'finished', title: 'The Pod is terminating or has finished' }
  }
  if (status?.running) return { state: 'running' }
  const waiting = status?.waiting
  switch (waiting?.reason) {
    case 'InvalidImageName':
      return { state: 'finished', title: 'The debug image name is invalid', hint: 'Start a new debug session with a valid image reference.', ...waiting }
    case 'ErrImagePull':
    case 'ImagePullBackOff':
      return { state: 'blocked', title: 'Kubernetes could not pull the debug image', hint: 'Check the image and registry access. The terminal opens if Kubernetes succeeds on a retry.', ...waiting }
    case 'ErrImageNeverPull':
      return { state: 'blocked', title: 'The debug image is missing on the node and pulling is disabled', hint: 'Load the image on the node or start a new session with an available image.', ...waiting }
    case 'CreateContainerConfigError':
    case 'CreateContainerError':
    case 'RunContainerError':
      return { state: 'blocked', title: 'Kubernetes could not start the debug container', hint: 'Inspect the kubelet message. Start a new session if the image or its configuration must change.', ...waiting }
    default:
      return { state: 'waiting', title: 'Waiting for the debug container', ...waiting }
  }
}

export function PodDebugSession({ namespace, podName, containerName, session, isActive = true, children }: {
  namespace: string; podName: string; containerName: string; session: Session; isActive?: boolean; children: ReactNode
}) {
  const [connected, setConnected] = useState(false)
  const currentConnection = useCurrentConnection()
  const current = () => getApiBase() === session.apiBase && connectionHeaderValue() === session.connectionId
  const query = useQuery({
    queryKey: ['pod-debug-startup', session.apiBase, session.connectionId, session.podUID, containerName],
    queryFn: async () => {
      if (!current()) throw new ApiError('The cluster connection changed.', 409)
      const { resource } = await fetchResourceWithRelationships<PodDebugTarget>('pods', namespace, podName)
      if (!current()) throw new ApiError('The cluster connection changed.', 409)
      return startup(resource, session.podUID, containerName)
    },
    enabled: !connected && isActive,
    retry: false,
    refetchInterval: query => query.state.data?.state === 'finished' || query.state.data?.state === 'running' ||
      (query.state.error instanceof ApiError && [403, 404, 409].includes(query.state.error.status)) ? false : query.state.data?.state === 'blocked' ? 5000 : 2000,
    refetchOnWindowFocus: false,
  })

  if (!connected && !query.error && query.data?.state === 'running' && current()) setConnected(true)
  if (currentConnection !== session.connectionId || getApiBase() !== session.apiBase) return <div role="status" className="p-5 text-sm text-theme-text-secondary">The cluster connection changed. Reopen the Pod to inspect this debug session.</div>
  if (connected) return <>{children}</>

  const status = query.data
  const error = query.error
  const code = error instanceof ApiError ? error.status : undefined
  const title = code === 403 ? 'Access denied while reading the debug container'
    : code === 404 ? 'The original Pod is gone'
    : code === 409 ? 'The cluster connection changed'
    : error ? 'Could not read the debug container status' : status?.title ?? 'Waiting for the debug container'
  return <div className="flex h-full min-h-32 flex-col justify-center gap-2 overflow-auto bg-theme-base px-5 py-4">
    <h3 className="flex items-center gap-2 font-medium text-theme-text-primary">
      {!error && (!status || status.state === 'waiting') && <LoaderCircle aria-hidden className="h-4 w-4 animate-spin text-theme-text-tertiary" />}
      {title}
    </h3>
    <p className="break-all font-mono text-xs text-theme-text-secondary">{namespace}/{podName} · {containerName} · {session.image}</p>
    {(error || status?.reason || status?.message) && <p className="break-words text-sm text-theme-text-primary">
      {error?.message || [status?.reason, status?.message].filter(Boolean).join(' · ')}
    </p>}
    {!error && status?.hint && <p className="text-xs text-theme-text-secondary">{status.hint}</p>}
    {!error && (!status || status.state === 'waiting') && <p className="text-xs text-theme-text-secondary">The terminal opens when Kubernetes reports this container is running.</p>}
    {error && code !== 404 && code !== 409 && <button type="button" className="btn-secondary flex w-fit items-center gap-1.5 px-3 py-1.5 text-xs"
      disabled={query.isFetching} onClick={() => { void query.refetch() }}><RefreshCw className="h-3.5 w-3.5" />Check status</button>}
  </div>
}
