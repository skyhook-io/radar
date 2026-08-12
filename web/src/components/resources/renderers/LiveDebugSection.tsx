import { useEffect, useMemo, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { getDefaultRunningContainerName } from '@skyhook-io/k8s-ui'
import {
  LiveDebugSection as BaseLiveDebugSection,
  type LiveDebugAspect,
  type LiveDebugRun,
} from '@skyhook-io/k8s-ui/components/resources/renderers/LiveDebugSection'
import { fetchIGRun, fetchIGStatus, startIGCapture, stopIGRun } from '../../../api/igdebug'

interface LiveDebugSectionProps {
  data: any
  onNavigate?: (ref: { kind: string; namespace: string; name: string }) => void
}

export function LiveDebugSection({ data, onNavigate }: LiveDebugSectionProps) {
  const namespace = data.metadata?.namespace || ''
  const pod = data.metadata?.name || ''
  const containers = useMemo(() => {
    const running = new Set(
      (data.status?.containerStatuses || [])
        .filter((status: any) => status.state?.running != null)
        .map((status: any) => status.name),
    )
    return (data.spec?.containers || [])
      .map((container: any) => container.name as string)
      .filter((name: string) => running.has(name))
  }, [data.spec?.containers, data.status?.containerStatuses])
  const defaultContainer = getDefaultRunningContainerName(data) || ''
  const [selectedContainer, setSelectedContainer] = useState(defaultContainer)
  const [run, setRun] = useState<LiveDebugRun>()
  const [error, setError] = useState<string>()
  const runID = run?.id
  const runState = run?.state
  const statusQuery = useQuery({
    queryKey: ['ig', 'status'],
    queryFn: fetchIGStatus,
    staleTime: 15_000,
    retry: false,
  })

  useEffect(() => {
    setSelectedContainer(defaultContainer)
    setRun(undefined)
    setError(undefined)
  }, [namespace, pod, defaultContainer])

  useEffect(() => {
    if (!runID || !runState || ['complete', 'stopped', 'failed', 'partial'].includes(runState)) return
    let cancelled = false
    const poll = async () => {
      try {
        const next = await fetchIGRun(runID)
        if (!cancelled) setRun(next)
      } catch (pollError) {
        if (!cancelled) setError(pollError instanceof Error ? pollError.message : String(pollError))
      }
    }
    const timer = window.setInterval(poll, 1_000)
    void poll()
    return () => {
      cancelled = true
      window.clearInterval(timer)
    }
  }, [runID, runState])

  if (statusQuery.isLoading || statusQuery.data?.state === 'not-installed') return null

  const capture = async (aspect: LiveDebugAspect, durationSeconds: number) => {
    setError(undefined)
    setRun(undefined)
    try {
      setRun(await startIGCapture({ namespace, pod, container: selectedContainer, aspect, durationSeconds }))
    } catch (captureError) {
      setError(captureError instanceof Error ? captureError.message : String(captureError))
    }
  }

  const stop = async () => {
    if (!run) return
    try {
      setRun(await stopIGRun(run.id))
    } catch (stopError) {
      setError(stopError instanceof Error ? stopError.message : String(stopError))
    }
  }

  const changeContainer = (container: string) => {
    setSelectedContainer(container)
    setRun(undefined)
    setError(undefined)
  }

  const status = statusQuery.data?.state === 'installed' ? 'installed' : 'unknown'
  return (
    <BaseLiveDebugSection
      status={status}
      statusMessage={statusQuery.data?.message || (statusQuery.error as Error | undefined)?.message}
      containers={containers}
      selectedContainer={selectedContainer}
      onContainerChange={changeContainer}
      run={run}
      error={error}
      onCapture={capture}
      onStop={stop}
      onNavigate={onNavigate}
    />
  )
}
