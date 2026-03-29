import { useEffect, useRef, useState, useCallback } from 'react'
import { Loader2, AlertCircle, RefreshCw } from 'lucide-react'
import { TerminalTab } from './TerminalTab'

export interface NodeTerminalTabProps {
  nodeName: string
  isActive?: boolean
  /** Create a debug pod on the node, returns pod coordinates for exec */
  createNodeDebugPod: (nodeName: string) => Promise<{
    podName: string
    namespace: string
    containerName: string
  }>
  /** Clean up debug pod(s) for this node */
  cleanupNodeDebugPod: (nodeName: string) => Promise<void>
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
  const [debugPod, setDebugPod] = useState<{
    podName: string
    namespace: string
    containerName: string
  } | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [isCreating, setIsCreating] = useState(true)
  const cleanupDoneRef = useRef(false)
  // Stable refs for callbacks
  const createNodeDebugPodRef = useRef(createNodeDebugPod)
  const cleanupNodeDebugPodRef = useRef(cleanupNodeDebugPod)
  const createSessionRef = useRef(createSession)
  useEffect(() => { createNodeDebugPodRef.current = createNodeDebugPod }, [createNodeDebugPod])
  useEffect(() => { cleanupNodeDebugPodRef.current = cleanupNodeDebugPod }, [cleanupNodeDebugPod])
  useEffect(() => { createSessionRef.current = createSession }, [createSession])

  const createPod = useCallback(async () => {
    cleanupDoneRef.current = false
    setIsCreating(true)
    setError(null)
    try {
      const result = await createNodeDebugPodRef.current(nodeName)
      setDebugPod(result)
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to create debug pod')
    } finally {
      setIsCreating(false)
    }
  }, [nodeName])

  useEffect(() => {
    createPod()
    return () => {
      if (!cleanupDoneRef.current) {
        cleanupDoneRef.current = true
        cleanupNodeDebugPodRef.current(nodeName).catch((err) => {
          console.warn('[NodeTerminal] Cleanup on unmount failed:', err)
        })
      }
    }
  }, [nodeName, createPod])

  // Best-effort cleanup on page unload — uses keepalive so the browser
  // does not cancel the request when the page navigates away.
  useEffect(() => {
    const handleUnload = () => {
      if (!cleanupDoneRef.current) {
        cleanupDoneRef.current = true
        cleanupNodeDebugPodRef.current(nodeName).catch(() => {})
      }
    }
    window.addEventListener('beforeunload', handleUnload)
    return () => window.removeEventListener('beforeunload', handleUnload)
  }, [nodeName])

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
