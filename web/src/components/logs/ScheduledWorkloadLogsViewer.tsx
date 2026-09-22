import { useEffect, useMemo, useState } from 'react'
import { Loader2, Terminal } from 'lucide-react'
import { clsx } from 'clsx'
import { useWorkloadRuns, useResource, type WorkloadRun } from '../../api/client'
import { WorkloadLogsViewer } from './WorkloadLogsViewer'
import { pickDefaultRun, selectedRunMissing, workloadRunKey } from '../execution/BatchExecutionView'

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
  const jobSet = kind.toLowerCase() === 'jobset' || kind.toLowerCase() === 'jobsets'
  const [localRunKey, setLocalRunKey] = useState('')
  const effectiveRunKey = selectedRunKey ?? localRunKey
  const selectRun = onSelectRun ?? setLocalRunKey
  const [scope, setScope] = useState('selected')
  const [role, setRole] = useState('')
  const rootQuery = useResource<any>('jobsets', namespace, name, 'jobset.x-k8s.io', { enabled: jobSet })
  const runsQuery = useWorkloadRuns(kind, namespace, name, true, { clusterScoped, refetchActive: true, ...(jobSet ? { selected: effectiveRunKey } : {}) })
  const memberCollection = runsQuery.data?.collection === 'members'
  const runs = runsQuery.data?.runs ?? EMPTY_RUNS
  const resolvedRuns = useMemo(() => runsQuery.data?.selected && !runs.some(run => workloadRunKey(run) === workloadRunKey(runsQuery.data!.selected!)) ? [...runs, runsQuery.data.selected] : runs, [runs, runsQuery.data?.selected])
  const defaultRun = useMemo(() => memberCollection ? runs[0] : pickDefaultRun(runs), [memberCollection, runs])
  const roles: string[] = [...new Set<string>([...(rootQuery.data?.spec?.replicatedJobs?.map((r: { name: string }) => r.name) ?? []), ...(role ? [role] : [])])]
  const effectiveRole = role || roles[0] || ''
  const aggregate = memberCollection && scope !== 'selected'
  const scopeControls = memberCollection && <div className="flex flex-wrap items-center gap-2 border-b border-theme-border bg-theme-surface px-3 py-2 text-xs">
    <label htmlFor="jobset-log-scope" className="text-theme-text-secondary">Log scope</label>
    <select id="jobset-log-scope" value={scope} onChange={event => { if (event.target.value === 'role') setRole(effectiveRole); setScope(event.target.value) }} className="rounded border border-theme-border bg-theme-elevated px-2 py-1 text-theme-text-primary"><option value="selected">Selected Job</option><option value="role" disabled={roles.length === 0}>Role</option><option value="all">All current members</option></select>
    {scope === 'role' && <select aria-label="Log role" value={effectiveRole} onChange={event => setRole(event.target.value)} className="rounded border border-theme-border bg-theme-elevated px-2 py-1 text-theme-text-primary">{roles.map(r => <option key={r} value={r}>{r}</option>)}</select>}
    {aggregate && <span className="text-theme-text-secondary">Snapshot · Refresh for new logs · up to 40 container sources, 1,000 lines and 64 KiB per source · retained Pods only</span>}
  </div>

  const selectionMissing = memberCollection && selectedRunMissing(resolvedRuns, effectiveRunKey)

  useEffect(() => {
    if (!runsQuery.data || runsQuery.isPlaceholderData || selectionMissing) return
    if (resolvedRuns.length === 0) {
      if (effectiveRunKey) selectRun('')
      return
    }
    if (!resolvedRuns.some(run => workloadRunKey(run) === effectiveRunKey)) {
      selectRun(workloadRunKey(defaultRun ?? runs[0]))
    }
  }, [runsQuery.data, runsQuery.isPlaceholderData, runs, resolvedRuns, effectiveRunKey, defaultRun, selectRun, selectionMissing])

  const selectedRun = selectionMissing ? undefined : resolvedRuns.find(run => workloadRunKey(run) === effectiveRunKey) ?? defaultRun

  if (runsQuery.isLoading) {
    return (
      <div className="flex h-full items-center justify-center text-theme-text-tertiary">
        <div className="flex items-center gap-2">
          <Loader2 className="h-4 w-4 animate-spin" />
          <span>{kind === 'JobSet' || kind === 'jobsets' ? 'Loading member Jobs...' : 'Loading runs...'}</span>
        </div>
      </div>
    )
  }

  if (runsQuery.error) {
    return (
      <div className="flex h-full flex-col items-center justify-center gap-2 text-theme-text-tertiary">
        <Terminal className="h-8 w-8" />
        <span>{runsQuery.error instanceof Error ? runsQuery.error.message : kind === 'JobSet' || kind === 'jobsets' ? 'Failed to load member Jobs' : 'Failed to load runs'}</span>
      </div>
    )
  }

  if (aggregate) return <div className="flex h-full min-h-0 flex-col">{scopeControls}<div className="min-h-0 flex-1"><WorkloadLogsViewer key={JSON.stringify([rootQuery.data?.metadata?.uid, scope, effectiveRole])} kind="jobsets" namespace={namespace} name={name} snapshotOnly role={scope === 'role' ? effectiveRole : undefined} autoStream={false} /></div></div>

  if (selectionMissing) {
    return (
      <div className="space-y-3 p-4">{scopeControls}
        <p className="text-sm text-theme-text-secondary">Selected Job is currently unavailable. It may have been removed or be waiting for recreation. Your selection is preserved if it reappears; choose another shown Job to switch.</p>
        {runs.length > 0 && <select aria-label="Select a shown member Job" value={effectiveRunKey} onChange={(event) => selectRun(event.target.value)} className="max-w-full rounded-md border border-theme-border bg-theme-elevated px-2 py-1 text-sm text-theme-text-primary">
          <option value={effectiveRunKey} disabled>Choose a shown Job</option>
          {runs.map((run) => <option key={workloadRunKey(run)} value={workloadRunKey(run)}>{formatRunOption(run, false, true)}</option>)}
        </select>}
      </div>
    )
  }

  if (!selectedRun) {
    return (
      <div className="flex h-full flex-col items-center justify-center gap-2 text-theme-text-tertiary">
        <Terminal className="h-8 w-8" />
        <span>{memberCollection ? 'No child Jobs found' : 'No retained runs found'}</span>
      </div>
    )
  }

  return (
    <div className="flex h-full min-h-0 flex-col">
      {scopeControls}
      <div className="shrink-0 border-b border-theme-border bg-theme-surface px-3 py-2">
        <div className="flex flex-wrap items-center gap-2">
          <span className="text-xs font-medium text-theme-text-secondary">{memberCollection ? 'Job' : 'Run'}</span>
          <select
            value={workloadRunKey(selectedRun)}
            onChange={(event) => selectRun(event.target.value)}
            className="min-w-0 max-w-full rounded-md border border-theme-border bg-theme-elevated px-2 py-1 text-sm text-theme-text-primary"
          >
            {resolvedRuns.map(run => (
              <option key={workloadRunKey(run)} value={workloadRunKey(run)}>
                {formatRunOption(run, clusterScoped, memberCollection)}
              </option>
            ))}
          </select>
          <span className={clsx('badge-sm', phaseBadgeClass(selectedRun.phase))}>
            {selectedRun.phase}{selectedRun.deleting ? ' · deleting' : ''}
          </span>
          <span className="text-xs text-theme-text-tertiary">
            {formatRunTime(selectedRun)}
          </span>
          {memberCollection && runsQuery.data?.truncated && (
            <span className="text-xs text-theme-text-tertiary">Showing {runs.length} of {runsQuery.data.total} Jobs</span>
          )}
        </div>
      </div>
      <div className="min-h-0 flex-1">
        <WorkloadLogsViewer
          key={workloadRunLogsKey(selectedRun)}
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

function formatRunOption(run: WorkloadRun, showNamespace: boolean, memberCollection: boolean): string {
  const bits = [showNamespace ? `${run.namespace}/${run.name}` : run.name, `${run.phase}${run.deleting ? ' · deleting' : ''}`]
  if (memberCollection && run.jobset?.replicatedJob) {
    bits.push(`${run.jobset?.replicatedJob}${run.jobset?.jobIndex ? ` #${run.jobset?.jobIndex}` : ''}`)
  }
  if (run.progress) bits.push(run.progress)
  else if (run.desired) bits.push(`${run.succeeded ?? 0}/${run.desired}`)
  const work = run.podTotal ? `${run.podSucceeded ?? 0}/${run.podTotal} completed Pods` : ''
  if (work) bits.push(work)
  return bits.join(' · ')
}

// A JobSet retry can replace a child Job at the same resource address.
export function workloadRunLogsKey(run: WorkloadRun): string {
  return JSON.stringify([run.group, workloadRunKey(run), run.jobset?.restartAttempt, run.jobset?.jobRestartAttempt])
}
