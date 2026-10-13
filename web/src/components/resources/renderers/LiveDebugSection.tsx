import { useEffect, useLayoutEffect, useMemo, useRef, useState, type RefObject } from 'react'
import { useQuery } from '@tanstack/react-query'
import { RadarUpgradeNote, getDefaultContainerName, getDefaultRunningContainerName } from '@skyhook-io/k8s-ui'
import {
  LiveDebugSection as BaseLiveDebugSection,
  type LiveDebugAspect,
  type LiveDebugRun,
} from '@skyhook-io/k8s-ui/components/resources/renderers/LiveDebugSection'
import { ApiError, useRadarFeature } from '../../../api/client'
import { RadarFeatureUnsupportedError } from '../../../api/radarFeatures'
import { getApiBase } from '../../../api/config'
import { useCapabilitiesContext } from '../../../contexts/CapabilitiesContext'
import { useConnection } from '../../../context/ConnectionContext'
import { fetchIGRun, fetchIGStatus, startIGCapture, stopIGRun } from '../../../api/igdebug'
import { Section } from '@skyhook-io/k8s-ui/components/ui/drawer-components'
import { Activity } from 'lucide-react'

interface LiveDebugSectionProps {
  data: any
  onNavigate?: (ref: { kind: string; namespace: string; name: string }) => void
}

export function LiveDebugSection({ data, onNavigate }: LiveDebugSectionProps) {
  const apiBase = getApiBase()
  const { connection } = useConnection()
  const currentConnection = useRef({ context: connection.context })
  useLayoutEffect(() => {
    currentConnection.current.context = connection.context
  }, [connection.context])
  const viewKey = JSON.stringify([apiBase, connection.context, data.metadata?.namespace, data.metadata?.name])
  return <LiveDebugView key={viewKey} data={data} onNavigate={onNavigate} apiBase={apiBase} context={connection.context} currentConnection={currentConnection} />
}

interface LiveDebugViewProps extends LiveDebugSectionProps {
  apiBase: string
  context: string
  currentConnection: RefObject<{ context: string }>
}

function LiveDebugView(props: LiveDebugViewProps) {
  const { data } = props
  const containers = useMemo(() => (data.spec?.containers || []).map((container: any) => container.name as string), [data.spec?.containers])
  const defaultContainer = getDefaultRunningContainerName(data) || getDefaultContainerName(data) || ''
  const [requestedContainer, setSelectedContainer] = useState(defaultContainer)
  const selectedContainer = containers.includes(requestedContainer) ? requestedContainer : defaultContainer
  if (requestedContainer !== selectedContainer) setSelectedContainer(selectedContainer)
  return <LiveDebugCapture {...props} containers={containers} selectedContainer={selectedContainer} onContainerChange={setSelectedContainer} />
}

function LiveDebugCapture({ data, onNavigate, apiBase, context, currentConnection, containers, selectedContainer, onContainerChange }: LiveDebugViewProps & {
  containers: string[]
  selectedContainer: string
  onContainerChange: (container: string) => void
}) {
  const cloud = useCapabilitiesContext().deployment?.mode === 'cloud'
  const feature = useRadarFeature('liveDebug')
  const namespace = data.metadata?.namespace || ''
  const pod = data.metadata?.name || ''
  const [run, setRun] = useState<LiveDebugRun>()
  const [error, setError] = useState<string>()
  const [pendingTarget, setPendingTarget] = useState<string>()
  const [stateContainer, setStateContainer] = useState(selectedContainer)
  if (stateContainer !== selectedContainer) {
    setStateContainer(selectedContainer)
    setRun(undefined)
    setError(undefined)
    setPendingTarget(undefined)
  }
  const lifecycle = useRef({ generation: 0, busy: false, run: undefined as LiveDebugRun | undefined })
  const containerStatus = data.status?.containerStatuses?.find((status: any) => status.name === selectedContainer)
  const containerID = containerStatus?.containerID
  const currentContainerID = useRef(containerID)
  const running = containerStatus?.state?.running != null
  const normalizedContainerID = containerID?.replace(/^.*?:\/\//, '')
  const targetChanged = !!run && (run.target.podUid !== data.metadata?.uid || (!!run.target.containerId && !!normalizedContainerID && run.target.containerId !== normalizedContainerID))
  const targetKey = `${apiBase}:${context}:${data.metadata?.uid}:${selectedContainer}`
  const currentTarget = useRef(targetKey)
  const pending = pendingTarget === targetKey
  const runID = run?.id
  const runState = run?.state
  const statusQuery = useQuery({
    queryKey: ['ig', 'status', apiBase, context, ...feature.gatedKey],
    enabled: !cloud,
    queryFn: () => feature.guard(fetchIGStatus),
    staleTime: 15_000,
    retry: false,
  })

  useLayoutEffect(() => {
    lifecycle.current = { generation: 0, busy: false, run: undefined }
  }, [selectedContainer])

  useLayoutEffect(() => {
    lifecycle.current.run = run
    currentTarget.current = targetKey
    currentContainerID.current = containerID
  }, [run, targetKey, containerID])

  useEffect(() => {
    const capture = lifecycle.current
    const connection = currentConnection.current
    return () => {
      // A container change installs a new session before this cleanup runs.
      capture.generation++
      const current = capture.run
      if (current && !['complete', 'stopped', 'failed', 'partial'].includes(current.state)
          && getApiBase() === apiBase && connection.context === context) {
        void stopIGRun(current.id).catch(() => {})
      }
    }
  }, [apiBase, context, currentConnection, selectedContainer])

  useEffect(() => {
    lifecycle.current.generation++
    lifecycle.current.busy = false
  }, [targetKey])

  useEffect(() => {
    if (!runID || !runState || ['complete', 'stopped', 'failed', 'partial'].includes(runState)) return
    let cancelled = false
    const target = targetKey
    const abort = new AbortController()
    let timer: number | undefined
    const poll = async () => {
      try {
        const next = await fetchIGRun(runID, abort.signal)
        if (!cancelled && currentTarget.current === target) setRun(next)
      } catch (pollError) {
        if (!cancelled && currentTarget.current === target) {
          const message = pollError instanceof Error ? pollError.message : String(pollError)
          setError(message)
          if (pollError instanceof ApiError && (pollError.status === 403 || pollError.status === 404)) {
            setRun(previous => previous ? { ...previous, state: 'failed', error: message } : previous)
            return
          }
        }
      }
      if (!cancelled) timer = window.setTimeout(poll, 1_000)
    }
    void poll()
    return () => {
      cancelled = true
      abort.abort()
      window.clearTimeout(timer)
    }
  }, [runID, runState, targetKey])

  if (cloud) return null

  if (statusQuery.error instanceof RadarFeatureUnsupportedError) {
    return <Section title="Live Debug" icon={Activity} defaultExpanded={false}><div className="mb-2 text-xs text-theme-text-secondary">Requires an existing Inspektor Gadget deployment and a direct Radar connection.</div><RadarUpgradeNote requirement={statusQuery.error.radarUpgrade} /></Section>
  }

  if (statusQuery.isLoading || statusQuery.data?.state === 'not-installed') return null

  const capture = async (aspect: LiveDebugAspect, durationSeconds: number) => {
    const active = lifecycle.current
    if (!running || active.busy || (run && !['complete', 'stopped', 'failed', 'partial'].includes(run.state))) return
    active.busy = true
    setPendingTarget(targetKey)
    const token = ++active.generation
    const target = targetKey
    const runtimeID = containerID
    setError(undefined)
    setRun(undefined)
    try {
      const next = await startIGCapture({ namespace, pod, podUid: data.metadata.uid, containerId: containerID, container: selectedContainer, aspect, durationSeconds })
      if (active.generation === token && currentTarget.current === target && (runtimeID == null || currentContainerID.current === runtimeID)) setRun(next)
      else {
        if (active.generation === token && currentTarget.current === target) {
          setError('The container restarted while the capture request was pending. Its response was discarded. Capture again.')
        }
        if (getApiBase() === apiBase && currentConnection.current.context === context) {
          void stopIGRun(next.id).catch(() => {})
        }
      }
    } catch (captureError) {
      if (active.generation === token && currentTarget.current === target) setError(captureError instanceof Error ? captureError.message : String(captureError))
    } finally {
      if (active.generation === token && currentTarget.current === target) {
        active.busy = false
        setPendingTarget(undefined)
      }
    }
  }

  const stop = async () => {
    if (!run) return
    const active = lifecycle.current
    const token = active.generation
    const target = targetKey
    try {
      const next = await stopIGRun(run.id)
      if (active.generation === token && currentTarget.current === target) setRun(next)
    } catch (stopError) {
      if (active.generation === token && currentTarget.current === target) setError(stopError instanceof Error ? stopError.message : String(stopError))
    }
  }

  const status = statusQuery.data?.state === 'installed' ? 'installed' : 'unknown'
  return (
    <BaseLiveDebugSection
      status={status}
      pending={pending}
      targetChanged={targetChanged}
      captureUnavailableReason={!running ? 'Capture needs a running container. This container has no reported running state.' : undefined}
      statusMessage={statusQuery.data?.message || (statusQuery.error as Error | undefined)?.message}
      containers={containers}
      selectedContainer={selectedContainer}
      onContainerChange={onContainerChange}
      run={run}
      error={error}
      onCapture={capture}
      onStop={stop}
      onNavigate={onNavigate}
    />
  )
}
