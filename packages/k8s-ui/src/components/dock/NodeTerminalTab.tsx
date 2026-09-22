import { useEffect, useRef, useState, useCallback } from 'react'
import { Loader2, AlertCircle, RefreshCw } from 'lucide-react'
import { TerminalTab } from './TerminalTab'

export interface NodeDebugPod {
  podName: string
  namespace: string
  uid: string
  containerName: string
}

export interface NodeTerminalTabProps {
  nodeName: string
  isActive?: boolean
  /** Create a debug pod and return its identity and exec coordinates. */
  createNodeDebugPod: (nodeName: string) => Promise<NodeDebugPod>
  /** Clean up only this creation result, using its UID as a precondition. */
  cleanupNodeDebugPod: (nodeName: string, pod: NodeDebugPod) => Promise<void>
  /** Return WebSocket URL for exec into a pod container */
  createSession: (namespace: string, podName: string, containerName: string) => Promise<{ wsUrl: string }>
}

export function NodeTerminalTab({
  nodeName,
  isActive,
  createNodeDebugPod,
  cleanupNodeDebugPod,
  createSession,
}: NodeTerminalTabProps) {
  const [debugPod, setDebugPod] = useState<NodeDebugPod | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [isCreating, setIsCreating] = useState(true)
  const [attempt, setAttempt] = useState(0)
  // Stable refs avoid restarting creation when the host renders new callbacks.
  const createNodeDebugPodRef = useRef(createNodeDebugPod)
  const cleanupNodeDebugPodRef = useRef(cleanupNodeDebugPod)
  const createSessionRef = useRef(createSession)
  useEffect(() => { createNodeDebugPodRef.current = createNodeDebugPod }, [createNodeDebugPod])
  useEffect(() => { cleanupNodeDebugPodRef.current = cleanupNodeDebugPod }, [cleanupNodeDebugPod])
  useEffect(() => { createSessionRef.current = createSession }, [createSession])

  const createPod = useCallback(() => setAttempt(value => value + 1), [])

  useEffect(() => {
    // Each attempt owns its result, including retries and Strict Mode remounts.
    let disposed = false
    let pod: NodeDebugPod | null = null
    let cleanupDone = false
    const cleanup = cleanupNodeDebugPodRef.current
    const dispose = () => {
      disposed = true
      if (!pod || cleanupDone) return
      cleanupDone = true
      cleanup(nodeName, pod).catch((err) => {
        console.warn('[NodeTerminal] Cleanup failed:', err)
      })
    }

    setDebugPod(null)
    setIsCreating(true)
    setError(null)
    const create = async () => {
      try {
        pod = await createNodeDebugPodRef.current(nodeName)
        if (disposed) {
          dispose()
        } else {
          setDebugPod(pod)
        }
      } catch (err) {
        if (!disposed) setError(err instanceof Error ? err.message : 'Failed to create debug pod')
      } finally {
        if (!disposed) setIsCreating(false)
      }
    }
    void create()

    // The host uses keepalive for best-effort delivery during page unload.
    window.addEventListener('beforeunload', dispose)
    return () => {
      window.removeEventListener('beforeunload', dispose)
      dispose()
    }
  }, [nodeName, attempt])

  if (isCreating) {
    return (
      <div className="h-full w-full flex flex-col items-center justify-center gap-3 bg-theme-base">
        <Loader2 className="w-6 h-6 text-blue-400 animate-spin" />
        <div className="text-sm text-theme-text-secondary">
          Creating debug pod on <span className="text-theme-text-primary font-medium">{nodeName}</span>...
        </div>
        <div className="text-xs text-theme-text-tertiary">
          This may take a moment while the pod starts
        </div>
      </div>
    )
  }

  if (error) {
    return (
      <div className="h-full w-full flex flex-col items-center justify-center gap-3 bg-theme-base p-4">
        <AlertCircle className="w-6 h-6 text-red-400" />
        <div className="text-sm text-red-400">Failed to create debug shell</div>
        <div className="text-xs text-theme-text-tertiary text-center max-w-md break-all">{error}</div>
        <button
          onClick={createPod}
          className="flex items-center gap-2 px-3 py-1.5 btn-brand text-xs rounded"
        >
          <RefreshCw className="w-3 h-3" />
          Retry
        </button>
      </div>
    )
  }

  if (!debugPod) return null

  // Wrap createSession to bind the debug pod's namespace/podName
  const boundCreateSession = (containerName: string) =>
    createSessionRef.current(debugPod.namespace, debugPod.podName, containerName)

  return (
    <TerminalTab
      namespace={debugPod.namespace}
      podName={debugPod.podName}
      containerName={debugPod.containerName}
      containers={[debugPod.containerName]}
      isActive={isActive}
      createSession={boundCreateSession}
    />
  )
}
