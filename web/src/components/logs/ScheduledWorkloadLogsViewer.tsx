import { useEffect, useMemo, useState } from 'react'
import { Loader2, Terminal } from 'lucide-react'
import { clsx } from 'clsx'
import { useWorkloadRuns, type WorkloadRun } from '../../api/client'
import { WorkloadLogsViewer } from './WorkloadLogsViewer'
import { pickDefaultRun, workloadRunKey } from '../execution/BatchExecutionView'

interface ScheduledWorkloadLogsViewerProps {
  kind: string
  namespace: string
  name: string
  selectedRunKey?: string
  onSelectRun?: (runKey: string) => void
}

const EMPTY_RUNS: WorkloadRun[] = []

export function ScheduledWorkloadLogsViewer({ kind, namespace, name, selectedRunKey, onSelectRun }: ScheduledWorkloadLogsViewerProps) {
  const clusterScoped = kind === 'ClusterWorkflowTemplate' || kind === 'clusterworkflowtemplates'
  const runsQuery = useWorkloadRuns(kind, namespace, name, true, { clusterScoped, refetchActive: true })
  const runs = runsQuery.data?.runs ?? EMPTY_RUNS
  const defaultRun = useMemo(() => pickDefaultRun(runs), [runs])
  const [localRunKey, setLocalRunKey] = useState('')
  const effectiveRunKey = selectedRunKey ?? localRunKey
  const selectRun = onSelectRun ?? setLocalRunKey

  useEffect(() => {
    if (!runsQuery.data) return
    if (runs.length === 0) {
      if (effectiveRunKey) selectRun('')
      return
    }
    if (!runs.some(run => workloadRunKey(run) === effectiveRunKey)) {
      selectRun(workloadRunKey(defaultRun ?? runs[0]))
    }
  }, [runsQuery.data, runs, effectiveRunKey, defaultRun, selectRun])

  const selectedRun = runs.find(run => workloadRunKey(run) === effectiveRunKey) ?? defaultRun

  if (runsQuery.isLoading) {
    return (
      <div className="flex h-full items-center justify-center text-theme-text-tertiary">
        <div className="flex items-center gap-2">
          <Loader2 className="h-4 w-4 animate-spin" />
          <span>Loading runs...</span>
        </div>
      </div>
    )
  }

  if (runsQuery.error) {
    return (
      <div className="flex h-full flex-col items-center justify-center gap-2 text-theme-text-tertiary">
        <Terminal className="h-8 w-8" />
        <span>{runsQuery.error instanceof Error ? runsQuery.error.message : 'Failed to load runs'}</span>
      </div>
    )
  }

  if (!selectedRun) {
    return (
      <div className="flex h-full flex-col items-center justify-center gap-2 text-theme-text-tertiary">
        <Terminal className="h-8 w-8" />
        <span>No retained runs found</span>
      </div>
    )
  }

  return (
    <div className="flex h-full min-h-0 flex-col">
      <div className="shrink-0 border-b border-theme-border bg-theme-surface px-3 py-2">
        <div className="flex flex-wrap items-center gap-2">
          <span className="text-xs font-medium text-theme-text-secondary">Run</span>
          <select
            value={workloadRunKey(selectedRun)}
            onChange={(event) => selectRun(event.target.value)}
            className="min-w-0 max-w-full rounded-md border border-theme-border bg-theme-elevated px-2 py-1 text-sm text-theme-text-primary"
          >
            {runs.map(run => (
              <option key={workloadRunKey(run)} value={workloadRunKey(run)}>
                {formatRunOption(run, clusterScoped)}
              </option>
            ))}
          </select>
          <span className={clsx('badge-sm', phaseBadgeClass(selectedRun.phase))}>
            {selectedRun.phase}
          </span>
          <span className="text-xs text-theme-text-tertiary">
            {formatRunTime(selectedRun)}
          </span>
        </div>
      </div>
      <div className="min-h-0 flex-1">
        <WorkloadLogsViewer
          key={`${selectedRun.kind}/${selectedRun.namespace}/${selectedRun.name}`}
          kind={selectedRun.kind}
          namespace={selectedRun.namespace}
          name={selectedRun.name}
          autoStream={selectedRun.active}
        />
      </div>
    </div>
  )
}

function phaseBadgeClass(phase: string): string {
  switch (phase) {
    case 'Succeeded':
      return 'status-healthy'
    case 'Running':
      return 'status-neutral'
    case 'Failed':
    case 'Error':
      return 'status-unhealthy'
    case 'Pending':
      return 'status-degraded'
    default:
      return 'status-unknown'
  }
}

function formatRunTime(run: WorkloadRun): string {
  const raw = run.startedAt || run.scheduledAt || run.finishedAt
  if (!raw) return ''
  return new Date(raw).toLocaleString()
}

function formatRunOption(run: WorkloadRun, showNamespace: boolean): string {
  const bits = [showNamespace ? `${run.namespace}/${run.name}` : run.name, run.phase]
  if (run.progress) bits.push(run.progress)
  else if (run.desired) bits.push(`${run.succeeded ?? 0}/${run.desired}`)
  const work = run.podTotal ? `${run.podSucceeded ?? 0}/${run.podTotal} pods` : ''
  if (work) bits.push(work)
  return bits.join(' · ')
}
