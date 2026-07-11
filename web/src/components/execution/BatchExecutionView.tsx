import { useEffect, useMemo, useState, type ComponentType, type ReactNode } from 'react'
import { Activity, ChevronRight, GitBranch } from 'lucide-react'
import { clsx } from 'clsx'
import { EmptyState, FetchResult, StatusDot, mapHealthToTone } from '@skyhook-io/k8s-ui'
import { buildWorkflowExecutionModel, type WorkflowExecutionActivity, type WorkflowExecutionModel, type WorkflowExecutionNode, type WorkflowTemplateReference } from '@skyhook-io/k8s-ui/utils/workflow-execution'
import { midTruncate } from '@skyhook-io/k8s-ui/utils/format'
import { useResource, useWorkloadRuns, type WorkloadRun } from '../../api/client'
import { getScaledJobStatus } from '../resources/resource-utils-keda'

const EMPTY_RUNS: WorkloadRun[] = []
const SCHEDULED_KINDS = new Set(['CronJob', 'CronWorkflow', 'WorkflowTemplate', 'ClusterWorkflowTemplate', 'ScaledJob'])
const RUN_KIND_LABEL: Record<string, string> = {
  jobs: 'Job',
  workflows: 'Workflow',
}

export function workloadRunKey(run: Pick<WorkloadRun, 'kind' | 'namespace' | 'name'>): string {
  return `${run.kind}/${run.namespace}/${run.name}`
}

function isTemplateKind(kind: string): boolean {
  return kind === 'WorkflowTemplate' || kind === 'ClusterWorkflowTemplate'
}

function configurationTitle(kind: string): string {
  if (kind === 'CronJob' || kind === 'CronWorkflow') return 'Schedule'
  if (kind === 'ScaledJob') return 'Trigger configuration'
  if (isTemplateKind(kind)) return 'Definition'
  if (kind === 'Job') return 'Job configuration'
  return 'Workflow configuration'
}

interface BatchExecutionProps {
  kind: string
  apiKind: string
  namespace: string
  name: string
  resource: any
  selectedRunKey?: string
  onSelectRun?: (runKey: string) => void
  onNavigateToResource?: (resource: { kind: string; namespace: string; name: string; group?: string }) => void
}

export function BatchExecutionFullscreen({ kind, apiKind, namespace, name, resource, selectedRunKey = '', onSelectRun, onNavigateToResource }: BatchExecutionProps) {
  const scheduled = SCHEDULED_KINDS.has(kind)
  const clusterScoped = kind === 'ClusterWorkflowTemplate'
  const runsQuery = useWorkloadRuns(apiKind, namespace, name, true, { refetchActive: true, clusterScoped })
  const runs = runsQuery.data?.runs ?? EMPTY_RUNS
  const defaultRun = useMemo(() => pickDefaultRun(runs), [runs])
  const [runFilter, setRunFilter] = useState<'all' | 'active' | 'failed'>('all')
  const [runSearch, setRunSearch] = useState('')

  useEffect(() => {
    if (!runsQuery.data) return
    if (runs.length === 0) {
      if (selectedRunKey) onSelectRun?.('')
      return
    }
    if (!runs.some((run) => workloadRunKey(run) === selectedRunKey)) {
      onSelectRun?.(workloadRunKey(defaultRun ?? runs[0]))
    }
  }, [runsQuery.data, runs, selectedRunKey, defaultRun, onSelectRun])

  const selectedRun = runs.find((run) => workloadRunKey(run) === selectedRunKey) ?? defaultRun
  const visibleRuns = useMemo(() => runs.filter((run) => {
    if (runFilter === 'active' && !run.active) return false
    if (runFilter === 'failed' && run.phase !== 'Failed' && run.phase !== 'Error') return false
    return !runSearch || run.name.toLowerCase().includes(runSearch.toLowerCase())
  }), [runs, runFilter, runSearch])
  const source = sourceFacts(kind, resource, runs)
  const phaseCounts = countPhases(runs)
  const fetchTarget = selectedRun && scheduled ? resourceTargetForRun(selectedRun) : null
  const selectedResourceQuery = useResource<any>(
    fetchTarget?.kind ?? '',
    fetchTarget?.namespace ?? '',
    fetchTarget?.name ?? '',
    fetchTarget?.group,
    { enabled: Boolean(fetchTarget), refetchInterval: selectedRun?.active ? 5000 : false },
  )
  const selectedResource = scheduled ? selectedResourceQuery.data : resource
  const workflowExecution = useMemo(
    () => selectedResource && selectedRun?.kind === 'workflows' ? buildWorkflowExecutionModel(selectedResource) : null,
    [selectedResource, selectedRun?.kind],
  )

  if (runsQuery.isLoading) {
    return <FetchResult loading className="h-full" />
  }

  if (runsQuery.error) {
    return (
      <div className="p-4">
        <EmptyState tone="neutral" variant="card" headline="Run history unavailable" body={runsQuery.error instanceof Error ? runsQuery.error.message : 'Radar could not load retained runs.'} />
      </div>
    )
  }

  return (
    <div className="flex h-full min-h-0 bg-theme-base">
      {scheduled && (
        <aside className="flex w-72 shrink-0 flex-col border-r border-theme-border bg-theme-surface">
          <div className="border-b border-theme-border px-3 py-3">
            <div className="flex items-center justify-between gap-2">
              <div>
                <div className="text-xs font-medium uppercase tracking-wide text-theme-text-tertiary">{isTemplateKind(kind) ? 'Workflows using this definition' : 'Run history'}</div>
                <div className="mt-1 text-sm font-semibold text-theme-text-primary">{pluralizeRuns(runs.length)}</div>
              </div>
            </div>
            <div className="mt-2 flex flex-wrap gap-1 text-[10px] text-theme-text-tertiary">
              {phaseCounts.running > 0 && <span className="rounded bg-theme-hover px-1.5 py-0.5">{phaseCounts.running} running</span>}
              {phaseCounts.failed > 0 && <span className="rounded bg-theme-hover px-1.5 py-0.5">{phaseCounts.failed} failed</span>}
              {phaseCounts.succeeded > 0 && <span className="rounded bg-theme-hover px-1.5 py-0.5">{phaseCounts.succeeded} succeeded</span>}
            </div>
            <p className="mt-2 text-[10px] leading-4 text-theme-text-tertiary">Retained Kubernetes objects, not all-time history.</p>
            {(runs.length > 8 || phaseCounts.failed > 0) && (
              <div className="mt-3 space-y-2">
                <div className="flex gap-1">
                  {(['all', 'active', 'failed'] as const).map((filter) => (
                    <button key={filter} type="button" onClick={() => setRunFilter(filter)} className={clsx('rounded px-2 py-1 text-[10px] font-medium capitalize', runFilter === filter ? 'selection' : 'text-theme-text-tertiary hover:bg-theme-hover')}>{filter}</button>
                  ))}
                </div>
                {runs.length > 20 && <input value={runSearch} onChange={(event) => setRunSearch(event.target.value)} placeholder="Filter run names" className="w-full rounded-md border border-theme-border bg-theme-elevated px-2 py-1.5 text-xs text-theme-text-primary placeholder:text-theme-text-tertiary" />}
              </div>
            )}
          </div>

          <div className="min-h-0 flex-1 overflow-y-auto p-2">
            {runs.length === 0 ? (
              <div className="p-2">
                <EmptyState
                  tone="neutral"
                  variant="card"
                  headline={emptyRunsCopy(kind, resource).headline}
                  body={emptyRunsCopy(kind, resource).body}
                />
              </div>
            ) : visibleRuns.length === 0 ? (
              <EmptyState tone="filtered" variant="card" headline="No runs match these filters" body="Change the status filter or run-name search." />
            ) : (
              <div className="space-y-1">
                {visibleRuns.map((run) => (
                  <RunRailButton
                    key={`${run.kind}/${run.namespace}/${run.name}`}
                    run={run}
                    showNamespace={clusterScoped}
                    selected={workloadRunKey(selectedRun ?? run) === workloadRunKey(run)}
                    onClick={() => onSelectRun?.(workloadRunKey(run))}
                  />
                ))}
              </div>
            )}
          </div>
        </aside>
      )}

      <main className="min-w-0 flex-1 overflow-auto">
        <div className="space-y-4 p-4">
          {scheduled && selectedResourceQuery.error && (
            <EmptyState tone="neutral" variant="card" headline="Selected run unavailable" body={selectedResourceQuery.error instanceof Error ? selectedResourceQuery.error.message : 'Radar could not load this retained run.'} />
          )}
          <div className="grid gap-3 lg:grid-cols-[minmax(0,1.7fr)_minmax(320px,0.9fr)]">
            <section className="space-y-3">
              {selectedRun ? (
                <section className="rounded-lg border border-theme-border bg-theme-surface">
                  <div className="flex items-start justify-between gap-3 border-b border-theme-border px-4 py-3">
                    <div className="min-w-0">
                      <div className="flex items-center gap-2">
                        <StatusDot tone={mapHealthToTone(phaseHealth(selectedRun.phase))} />
                        <div className="min-w-0">
                          <div className="text-[10px] font-medium uppercase tracking-wide text-theme-text-tertiary">Selected run</div>
                          {onNavigateToResource ? (
                            <button type="button" className="block max-w-full truncate text-base font-semibold text-accent-text hover:underline" onClick={() => onNavigateToResource(resourceTargetForRun(selectedRun))}>{selectedRun.name}</button>
                          ) : <h3 className="truncate text-base font-semibold text-theme-text-primary">{selectedRun.name}</h3>}
                        </div>
                      </div>
                      <div className="mt-1 flex flex-wrap gap-x-3 gap-y-1 text-xs text-theme-text-tertiary">
                        <span>{RUN_KIND_LABEL[selectedRun.kind] ?? selectedRun.kind}</span>
                        {selectedRun.startedAt && <span>started {formatAge(selectedRun.startedAt)}</span>}
                        {selectedRun.trigger === 'manual' && <span>manual trigger</span>}
                        {selectedRun.trigger === 'event' && <span>event triggered</span>}
                        {selectedRun.scheduledAt && <span>scheduled {formatAge(selectedRun.scheduledAt)}</span>}
                        {selectedResourceQuery.isFetching && <span>refreshing</span>}
                      </div>
                    </div>
                    <span className={clsx('badge', phaseBadgeClass(selectedRun.phase))}>{selectedRun.phase}</span>
                  </div>
                  <div className="grid gap-0 xl:grid-cols-[minmax(0,1fr)_minmax(280px,0.7fr)]">
                    <RunDetailList run={selectedRun} resource={selectedResource} workflowExecution={workflowExecution} scheduledParent={scheduled} />
                    <div className="border-t border-theme-border p-4 xl:border-l xl:border-t-0">
                      <RunContext run={selectedRun} resource={selectedResource} workflowExecution={workflowExecution} onNavigateToResource={onNavigateToResource} />
                    </div>
                  </div>
                  {selectedRun.message && <RunMessageDetails run={selectedRun} />}
                </section>
              ) : (
                <EmptyState
                  tone="neutral"
                  variant="card"
                  headline="No selected run"
                  body={`There are no retained ${runKindPluralForSchedule(kind)} to inspect.`}
                />
              )}
            </section>

            <section className="self-start rounded-lg border border-theme-border bg-theme-surface">
              <div className="border-b border-theme-border px-4 py-3">
                <h3 className="text-sm font-semibold text-theme-text-primary">{configurationTitle(kind)}</h3>
              </div>
              <div className="space-y-3 p-4">
                <SourceFacts source={source} />
              </div>
            </section>
          </div>

          {selectedRun && selectedRun.kind === 'workflows' ? (
            <div className="grid items-start gap-4 lg:grid-cols-[minmax(0,1.25fr)_minmax(360px,0.75fr)]">
              <RunExecutionPanel run={selectedRun} workflowExecution={workflowExecution} loading={selectedResourceQuery.isLoading} onNavigateToResource={onNavigateToResource} />
              <RunActivityPanel run={selectedRun} resource={selectedResource} workflowExecution={workflowExecution} />
            </div>
          ) : selectedRun ? (
            <RunActivityPanel run={selectedRun} resource={selectedResource} workflowExecution={workflowExecution} />
          ) : null}

        </div>
      </main>
    </div>
  )
}

export function pickDefaultRun(runs: WorkloadRun[]): WorkloadRun | undefined {
  return [...runs].sort(compareRuns)[0]
}

function pickLatestRun(runs: WorkloadRun[]): WorkloadRun | undefined {
  return [...runs].sort((a, b) => {
    const timeDiff = runTime(b) - runTime(a)
    if (timeDiff !== 0) return timeDiff
    return compareRuns(a, b)
  })[0]
}

function compareRuns(a: WorkloadRun, b: WorkloadRun): number {
  if (a.active !== b.active) return a.active ? -1 : 1
  const timeDiff = runTime(b) - runTime(a)
  if (timeDiff !== 0) return timeDiff
  const phaseDiff = phaseRank(a.phase) - phaseRank(b.phase)
  if (phaseDiff !== 0) return phaseDiff
  return a.name.localeCompare(b.name)
}

function phaseRank(phase: string): number {
  switch (phase) {
    case 'Failed':
    case 'Error':
      return 0
    case 'Running':
    case 'Pending':
      return 1
    case 'Succeeded':
      return 2
    default:
      return 3
  }
}

function runTime(run: WorkloadRun): number {
  let out = 0
  for (const value of [run.startedAt, run.scheduledAt, run.finishedAt]) {
    if (!value) continue
    const t = Date.parse(value)
    if (!Number.isNaN(t) && t > out) out = t
  }
  return out
}

function jobPhase(job: any): string {
  if (!job) return 'Pending'
  const conditions = job.status?.conditions ?? []
  if (job.spec?.suspend === true || conditions.some((c: any) => c.type === 'Suspended' && c.status === 'True')) return 'Suspended'
  if ((job.status?.active ?? 0) > 0) return 'Running'
  if (conditions.some((c: any) => c.type === 'Complete' && c.status === 'True')) return 'Succeeded'
  if (conditions.some((c: any) => c.type === 'Failed' && c.status === 'True')) return 'Failed'
  return 'Pending'
}

function scaledJobState(resource: any): string {
  return getScaledJobStatus(resource).text
}

function scaledJobTone(resource: any): 'info' | 'warning' | 'success' | 'error' {
  switch (getScaledJobStatus(resource).level) {
    case 'healthy':
      return 'success'
    case 'unhealthy':
    case 'alert':
      return 'error'
    case 'degraded':
      return 'warning'
    default:
      return 'info'
  }
}

function sourceFacts(kind: string, resource: any, runs: WorkloadRun[]) {
  const spec = resource?.spec ?? {}
  const status = resource?.status ?? {}
  const latest = pickLatestRun(runs)
  if (kind === 'CronJob') {
    return {
      state: spec.suspend ? 'Suspended' : (status.active?.length ?? 0) > 0 ? 'Active' : 'Scheduled',
      stateTone: spec.suspend ? 'warning' : 'info',
      schedule: spec.schedule,
      concurrency: spec.concurrencyPolicy || 'Allow',
      progress: latest?.progress,
      duration: latest ? formatRunDuration(latest) : '',
      work: `${status.active?.length ?? 0} active`,
      facts: [
        ['Last schedule', status.lastScheduleTime ? formatAge(status.lastScheduleTime) : 'Never'],
        ['Last success', status.lastSuccessfulTime ? formatAge(status.lastSuccessfulTime) : 'Never'],
        ['Starting deadline', spec.startingDeadlineSeconds ? `${spec.startingDeadlineSeconds}s` : 'None'],
        ['Retention limits', `${spec.successfulJobsHistoryLimit ?? 3} successful / ${spec.failedJobsHistoryLimit ?? 1} failed`],
      ],
    }
  }
  if (kind === 'CronWorkflow') {
    const schedules = Array.isArray(spec.schedules) ? spec.schedules.join(', ') : spec.schedule
    const template = spec.workflowSpec?.workflowTemplateRef?.name || spec.workflowSpec?.entrypoint
    return {
      state: spec.suspend ? 'Suspended' : (status.active?.length ?? 0) > 0 ? 'Active' : 'Scheduled',
      stateTone: spec.suspend ? 'warning' : 'info',
      schedule: schedules,
      concurrency: spec.concurrencyPolicy || 'Allow',
      progress: latest?.progress,
      duration: latest ? formatRunDuration(latest) : '',
      work: `${runs.filter((run) => run.active).length} active`,
      facts: [
        ['Timezone', spec.timezone || 'Cluster default'],
        ['Template', template || '-'],
        ['Starting deadline', spec.startingDeadlineSeconds ? `${spec.startingDeadlineSeconds}s` : 'None'],
        ['Retention limits', `${spec.successfulJobsHistoryLimit ?? 3} successful / ${spec.failedJobsHistoryLimit ?? 1} failed`],
      ],
    }
  }
  if (kind === 'WorkflowTemplate') {
    return {
      state: 'Definition',
      stateTone: 'info',
      progress: latest?.progress,
      duration: latest ? formatRunDuration(latest) : '',
      work: `${runs.filter((run) => run.active).length} active`,
      facts: [
        ['Entrypoint', spec.entrypoint || '-'],
        ['Templates', Array.isArray(spec.templates) ? String(spec.templates.length) : '-'],
        ['Service account', spec.serviceAccountName || '-'],
      ],
    }
  }
  if (kind === 'ClusterWorkflowTemplate') {
    return {
      state: 'Definition',
      stateTone: 'info',
      progress: latest?.progress,
      duration: latest ? formatRunDuration(latest) : '',
      work: `${runs.filter((run) => run.active).length} active`,
      facts: [
        ['Entrypoint', spec.entrypoint || '-'],
        ['Templates', Array.isArray(spec.templates) ? String(spec.templates.length) : '-'],
        ['Scope', 'Cluster'],
      ],
    }
  }
  if (kind === 'ScaledJob') {
    const active = runs.filter((run) => run.active).length
    const triggers = Array.isArray(spec.triggers) ? spec.triggers : []
    const image = spec.jobTargetRef?.template?.spec?.containers?.[0]?.image || 'Job template'
    return {
      state: scaledJobState(resource),
      stateTone: scaledJobTone(resource),
      progress: latest?.progress,
      duration: latest ? formatRunDuration(latest) : '',
      work: `${active} active`,
      facts: [
        ['Job image', String(image)],
        ['Triggers', triggers.length ? triggers.map((trigger: any) => trigger.type || 'trigger').join(', ') : '-'],
        ['Polling interval', spec.pollingInterval != null ? `${spec.pollingInterval}s` : 'Default'],
        ['History limits', `${spec.successfulJobsHistoryLimit ?? '-'} successful / ${spec.failedJobsHistoryLimit ?? '-'} failed`],
        ['Replica range', `${spec.minReplicaCount ?? 0} min / ${spec.maxReplicaCount ?? '-'} max`],
      ],
    }
  }
  if (kind === 'Job') {
    return {
      state: jobPhase(resource),
      stateTone: phaseTone(jobPhase(resource)),
      progress: latest?.progress,
      duration: latest ? formatRunDuration(latest) : '',
      work: latest ? workCount(latest) : '',
      facts: [
        ['Parallelism', String(spec.parallelism ?? 1)],
        ['Completions', String(spec.completions ?? 1)],
        ['Completion mode', spec.completionMode || 'NonIndexed'],
        ['Backoff limit', String(spec.backoffLimit ?? 6)],
        ['TTL after finish', spec.ttlSecondsAfterFinished != null ? `${spec.ttlSecondsAfterFinished}s` : 'None'],
      ],
    }
  }
  return {
    state: status.phase || 'Pending',
    stateTone: phaseTone(status.phase || 'Pending'),
    progress: status.progress,
    duration: latest ? formatRunDuration(latest) : '',
    work: latest ? workCount(latest) : '',
    facts: [
      ['Entrypoint', spec.entrypoint || '-'],
      ['Template', spec.workflowTemplateRef?.name || '-'],
      ['Priority', spec.priority != null ? String(spec.priority) : '-'],
      ['Service account', spec.serviceAccountName || '-'],
    ],
  }
}

function SourceFacts({ source }: { source: ReturnType<typeof sourceFacts> }) {
  return (
    <>
      <div className="grid grid-cols-2 gap-2">
        {source.state !== 'Definition' && <FactTile label="State" value={source.state} tone={source.stateTone} />}
        {source.schedule && <FactTile label="Schedule" value={source.schedule} mono />}
        {source.concurrency && <FactTile label="Concurrency" value={source.concurrency} />}
      </div>
      <div className="space-y-2">
        {source.facts.map(([label, value]) => (
          <div key={label} className="flex items-start justify-between gap-3 text-sm">
            <span className="text-theme-text-tertiary">{label}</span>
            <span className="min-w-0 truncate text-right text-theme-text-primary">{value}</span>
          </div>
        ))}
      </div>
    </>
  )
}

function RunDetailList({ run, resource, workflowExecution, scheduledParent }: { run: WorkloadRun; resource: any; workflowExecution: WorkflowExecutionModel | null; scheduledParent: boolean }) {
  const isWorkflowRun = run.kind === 'workflows' || !!workflowExecution
  const rows: Array<[string, string]> = [
    ['Started', run.startedAt ? formatAge(run.startedAt) : '-'],
    ['Finished', run.finishedAt ? formatAge(run.finishedAt) : run.active ? 'Running' : '-'],
    ['Duration', formatRunDuration(run) || '-'],
    ...(run.progress ? [['Progress', run.progress]] as Array<[string, string]> : []),
    ...(scheduledParent || run.trigger || run.scheduledAt ? [
      ['Trigger', run.trigger === 'manual' ? 'Manual' : run.trigger === 'event' ? 'Event' : run.scheduledAt ? 'Cron schedule' : '-'],
    ] as Array<[string, string]> : []),
    ...(run.scheduledAt ? [
      ['Cron scheduled', formatAge(run.scheduledAt)],
      ['Start delay', formatScheduleDelay(run) || '-'],
    ] as Array<[string, string]> : []),
    ['Parallelism', run.parallelism ? String(run.parallelism) : '-'],
    ['Pods', podBreakdown(run, workflowExecution) || '-'],
    ...(isWorkflowRun ? [
      ['Execution nodes', executionNodeBreakdown(workflowExecution) || '-'],
      ...(workflowExecution?.resourcesDuration ? [['Resource duration', formatResourceDuration(workflowExecution.resourcesDuration)]] as Array<[string, string]> : []),
    ] as Array<[string, string]> : [
      ['Retry limit', jobRetryLimitValue(resource)],
    ] as Array<[string, string]>),
  ]
  return (
    <div className="space-y-2 p-4">
      {rows.map(([label, value]) => (
        <div key={label} className="flex items-start justify-between gap-3 text-sm">
          <span className="text-theme-text-tertiary">{label}</span>
          <span className="min-w-0 break-words text-right text-theme-text-primary">{value}</span>
        </div>
      ))}
    </div>
  )
}

function RunMessageDetails({ run }: { run: WorkloadRun }) {
  const failed = run.phase === 'Failed' || run.phase === 'Error'
  return (
    <details className="group border-t border-theme-border">
      <summary className="flex cursor-pointer list-none items-center gap-2 px-4 py-3 hover:bg-theme-hover/50 [&::-webkit-details-marker]:hidden">
        <ChevronRight className="h-4 w-4 shrink-0 text-theme-text-tertiary transition-transform group-open:rotate-90" />
        <span className={clsx('shrink-0 text-sm font-medium', failed ? 'text-red-700 dark:text-red-300' : 'text-theme-text-primary')}>
          {failed ? 'Failure details' : 'Run message'}
        </span>
        <span className="min-w-0 truncate text-xs text-theme-text-tertiary">{run.message}</span>
      </summary>
      <div className="px-4 pb-4">
        <div className={clsx('break-words rounded-md border px-3 py-2 text-sm', failed ? 'border-red-500/30 bg-red-500/10 text-red-700 dark:text-red-300' : 'border-theme-border bg-theme-elevated/40 text-theme-text-secondary')}>
          {run.message}
        </div>
      </div>
    </details>
  )
}

function RunExecutionPanel({ run, workflowExecution, loading, onNavigateToResource }: { run: WorkloadRun; workflowExecution: WorkflowExecutionModel | null; loading: boolean; onNavigateToResource?: BatchExecutionProps['onNavigateToResource'] }) {
  const [showAll, setShowAll] = useState(false)
  if (run.kind === 'workflows') {
    if (loading && !workflowExecution) {
      return <Panel title="Execution" icon={GitBranch}><FetchResult loading /></Panel>
    }
    if (!workflowExecution || workflowExecution.nodes.length === 0) {
      return (
        <Panel title="Execution" icon={GitBranch}>
          <EmptyState tone="neutral" variant="card" headline="Execution detail unavailable" body={run.active ? 'This Workflow has not reported execution nodes yet.' : 'This retained Workflow no longer has execution-node detail.'} />
        </Panel>
      )
    }
    const rows = flattenExecutionRows(workflowExecution)
    const visibleRows = showAll || !workflowExecution.isLarge ? rows : executionPreviewRows(rows, workflowExecution)
    const messageOwners = new Map<string, { id: string; depth: number; leaf: boolean }>()
    for (const row of visibleRows) {
      const message = row.node.message?.trim()
      if (!message) continue
      const candidate = { id: row.node.id, depth: row.depth, leaf: row.node.childIds.length === 0 }
      const current = messageOwners.get(message)
      if (!current || (candidate.leaf && !current.leaf) || candidate.leaf === current.leaf && candidate.depth > current.depth) messageOwners.set(message, candidate)
    }
    const runMessage = run.message?.trim()
    const renderedRows = visibleRows.map((row) => {
      const nodeMessage = row.node.message?.trim()
      const repeatedRunMessage = Boolean(nodeMessage && runMessage && (runMessage.includes(nodeMessage) || nodeMessage.includes(runMessage)))
      return { ...row, showMessage: Boolean(nodeMessage) && !repeatedRunMessage && messageOwners.get(nodeMessage!)?.id === row.node.id }
    })
    return (
      <Panel title="Execution" icon={GitBranch} detail={`${workflowExecution.nodes.length} ${workflowExecution.nodes.length === 1 ? 'node' : 'nodes'}`}>
        <div className="divide-y divide-theme-border rounded-md border border-theme-border">
          {renderedRows.map(({ node, depth, showMessage }) => <ExecutionNodeRow key={node.id} node={node} depth={depth} showMessage={showMessage} namespace={run.namespace} onNavigateToResource={onNavigateToResource} />)}
        </div>
        {visibleRows.length < rows.length && <button type="button" className="mt-3 text-sm font-medium text-accent-text hover:underline" onClick={() => setShowAll(true)}>Show all {rows.length} nodes</button>}
      </Panel>
    )
  }

  return null
}

function executionPreviewRows(rows: ExecutionRow[], workflowExecution: WorkflowExecutionModel): ExecutionRow[] {
  const preview = rows.slice(0, 40)
  const included = new Set(preview.map((row) => row.node.id))
  const rowByID = new Map(rows.map((row) => [row.node.id, row]))
  for (const path of workflowExecution.focusPaths) {
    for (const node of path.nodes) {
      const row = rowByID.get(node.id)
      if (!row || included.has(node.id)) continue
      preview.push(row)
      included.add(node.id)
    }
  }
  return preview
}

function RunActivityPanel({ run, resource, workflowExecution }: { run: WorkloadRun; resource: any; workflowExecution: WorkflowExecutionModel | null }) {
  const [showAll, setShowAll] = useState(false)
  const activity = run.kind === 'workflows' ? workflowExecution?.activity ?? [] : jobActivity(run, resource)
  const significant = activity.filter((item) => item.tone === 'danger' || item.tone === 'warning' || item.id.startsWith('workflow-') || item.id === 'scheduled' || item.id === 'started')
  const defaultItems = activity.length <= 8 ? activity : significant.length > 0 ? significant : activity.slice(-8)
  const visible = showAll ? activity : defaultItems
  return (
    <Panel title="Activity" icon={Activity} detail={activity.length ? `${activity.length} events` : undefined}>
      {visible.length === 0 ? (
        <EmptyState tone="neutral" variant="card" headline="No activity yet" body="This run has not reported timing details yet." />
      ) : (
        <div className="divide-y divide-theme-border border-y border-theme-border">
          {visible.map((item) => (
            <div key={item.id} className="flex gap-3 px-2 py-2">
              <ActivityDot tone={item.tone} />
              <div className="min-w-0 flex-1">
                <div className="flex items-start justify-between gap-3">
                  <div className="truncate text-sm font-medium text-theme-text-primary">{item.label}</div>
                  <div className="shrink-0 text-xs text-theme-text-tertiary">{formatAge(item.at)}</div>
                </div>
                {item.detail && item.detail !== run.message && item.tone !== 'danger' && <div className="mt-0.5 break-words text-xs text-theme-text-secondary">{item.detail}</div>}
              </div>
            </div>
          ))}
        </div>
      )}
      {!showAll && visible.length < activity.length && <button type="button" className="mt-3 text-sm font-medium text-accent-text hover:underline" onClick={() => setShowAll(true)}>Show all {activity.length} events</button>}
    </Panel>
  )
}

function RunContext({ run, resource, workflowExecution, onNavigateToResource }: { run: WorkloadRun; resource: any; workflowExecution: WorkflowExecutionModel | null; onNavigateToResource?: BatchExecutionProps['onNavigateToResource'] }) {
  const definition = workflowExecution?.templateRefs.find((ref) => ref.source === 'workflow')
  const uses = dedupeResourceRefs(workflowExecution?.templateRefs.filter((ref) => ref.source === 'task') ?? [])
  const entrypoint = resource?.spec?.entrypoint || resource?.status?.storedWorkflowTemplateSpec?.entrypoint
  return (
    <div>
      <h4 className="mb-3 text-xs font-medium uppercase tracking-wide text-theme-text-tertiary">Run context</h4>
      <div className="space-y-3">
        <ContextRow label={run.launcher?.kind === 'ScaledJob' ? 'Triggered by' : run.launcher ? 'Scheduled by' : 'Created'}>
          {run.launcher ? <GenericResourceButton refInfo={run.launcher} onNavigateToResource={onNavigateToResource} /> : <span>Directly</span>}
        </ContextRow>
        <ContextRow label="Definition">
          {definition ? <ResourceButton refInfo={definition} onNavigateToResource={onNavigateToResource} /> : <span>Inline definition</span>}
        </ContextRow>
        {entrypoint && <ContextRow label="Entrypoint"><span className="font-mono text-xs">{entrypoint}</span></ContextRow>}
        {uses.length > 0 && (
          <ContextRow label="Uses">
            <span className="flex flex-wrap justify-end gap-2">
              {uses.map((ref) => <ResourceButton key={`${ref.resourceKind}/${ref.namespace}/${ref.name}`} refInfo={ref} onNavigateToResource={onNavigateToResource} />)}
            </span>
          </ContextRow>
        )}
      </div>
    </div>
  )
}

function ContextRow({ label, children }: { label: string; children: ReactNode }) {
  return <div className="flex items-start justify-between gap-3 text-sm"><span className="text-theme-text-tertiary">{label}</span><span className="min-w-0 text-right text-theme-text-primary">{children}</span></div>
}

function GenericResourceButton({ refInfo, onNavigateToResource }: { refInfo: NonNullable<WorkloadRun['launcher']>; onNavigateToResource?: BatchExecutionProps['onNavigateToResource'] }) {
  const label = `${refInfo.kind} · ${refInfo.namespace ? `${refInfo.namespace}/` : ''}${refInfo.name}`
  if (!onNavigateToResource) return <span>{label}</span>
  return <button type="button" className="text-accent-text hover:underline" onClick={() => onNavigateToResource({ kind: pluralKind(refInfo.kind), namespace: refInfo.namespace ?? '', name: refInfo.name, group: refInfo.group })}>{label}</button>
}

function dedupeResourceRefs(refs: WorkflowTemplateReference[]): WorkflowTemplateReference[] {
  const seen = new Set<string>()
  return refs.filter((ref) => {
    const key = `${ref.resourceKind}/${ref.namespace}/${ref.name}`
    if (seen.has(key)) return false
    seen.add(key)
    return true
  })
}

function Panel({ title, icon: Icon, detail, children }: { title: string; icon: ComponentType<{ className?: string }>; detail?: string; children: ReactNode }) {
  return (
    <section className="rounded-lg border border-theme-border bg-theme-surface">
      <div className="flex items-center justify-between gap-3 border-b border-theme-border px-4 py-3">
        <div className="flex items-center gap-2 text-sm font-semibold text-theme-text-primary">
          <Icon className="h-4 w-4 text-theme-text-secondary" />
          {title}
        </div>
        {detail && <span className="text-xs text-theme-text-tertiary">{detail}</span>}
      </div>
      <div className="p-4">{children}</div>
    </section>
  )
}

function ExecutionNodeRow({ node, depth, showMessage, namespace, onNavigateToResource }: { node: WorkflowExecutionNode; depth: number; showMessage: boolean; namespace: string; onNavigateToResource?: BatchExecutionProps['onNavigateToResource'] }) {
  return (
    <div className="flex items-start gap-3 px-3 py-2" style={{ paddingLeft: `${12 + Math.min(depth, 8) * 18}px` }}>
      <StatusDot tone={mapHealthToTone(phaseHealth(node.phase))} className="mt-1" />
      <div className="min-w-0 flex-1">
        <div className="flex flex-wrap items-center gap-2">
          <span className="truncate text-sm font-medium text-theme-text-primary">{node.displayName}</span>
          <span className="text-[10px] uppercase tracking-wide text-theme-text-tertiary">{node.type}</span>
        </div>
        {showMessage && (node.phase === 'Failed' || node.phase === 'Error') && <div className="mt-0.5 line-clamp-2 text-xs text-red-600 dark:text-red-400">{node.message}</div>}
        <div className="mt-1 flex flex-wrap gap-2 text-xs">
          {node.podName && <GenericResourceButton refInfo={{ kind: 'Pod', namespace, name: node.podName }} onNavigateToResource={onNavigateToResource} />}
          {node.templateRef && <ResourceButton refInfo={node.templateRef} onNavigateToResource={onNavigateToResource} />}
          {!node.templateRef && node.templateName && <span className="font-mono text-theme-text-tertiary">{node.templateName}</span>}
        </div>
      </div>
      <span className={clsx('badge-sm shrink-0', phaseBadgeClass(node.phase))}>{node.phase}</span>
    </div>
  )
}

type ExecutionRow = { node: WorkflowExecutionNode; depth: number }

function flattenExecutionRows(model: WorkflowExecutionModel): ExecutionRow[] {
  const byID = new Map(model.nodes.map((node) => [node.id, node]))
  const seen = new Set<string>()
  const rows: Array<{ node: WorkflowExecutionNode; depth: number }> = []
  const visit = (node: WorkflowExecutionNode, depth: number) => {
    if (seen.has(node.id)) return
    seen.add(node.id)
    rows.push({ node, depth })
    for (const childID of node.childIds) {
      const child = byID.get(childID)
      if (child) visit(child, depth + 1)
    }
  }
  for (const root of model.roots) visit(root, 0)
  for (const node of model.nodes) visit(node, 0)
  return rows
}

function ActivityDot({ tone }: { tone: string }) {
  return <span className={clsx('mt-1 h-2.5 w-2.5 shrink-0 rounded-full', toneDotClass(tone))} />
}

function ResourceButton({ refInfo, onNavigateToResource }: { refInfo: WorkflowTemplateReference; onNavigateToResource?: BatchExecutionProps['onNavigateToResource'] }) {
  const label = refInfo.clusterScope ? `${refInfo.name} (cluster)` : refInfo.name
  if (!onNavigateToResource) return <span className="text-sm font-medium text-theme-text-primary">{label}</span>
  return (
    <button
      type="button"
      className="truncate text-sm font-medium text-accent-text hover:underline"
      onClick={() => onNavigateToResource({ kind: refInfo.resourceKind, namespace: refInfo.namespace, name: refInfo.name, group: 'argoproj.io' })}
    >
      {label}
    </button>
  )
}

function RunRailButton({ run, selected, showNamespace, onClick }: { run: WorkloadRun; selected: boolean; showNamespace: boolean; onClick: () => void }) {
  return (
    <button
      type="button"
      onClick={onClick}
      className={clsx(
        'flex w-full items-start gap-2 rounded-md px-2 py-2 text-left transition-colors',
        selected ? 'selection selection-ring' : 'hover:bg-theme-hover',
      )}
    >
      <span className="min-w-0 flex-1">
          <span className="block truncate text-xs font-medium text-theme-text-primary" title={run.name}>{showNamespace ? `${run.namespace}/` : ''}{midTruncate(run.name, 34)}</span>
          <span className="mt-0.5 block truncate text-[10px] text-theme-text-tertiary">{formatRunTime(run) || 'time unknown'}{formatRunDuration(run) ? ` · ${formatRunDuration(run)}` : ''} · {workCount(run)}</span>
      </span>
      <span className={clsx('badge-sm shrink-0', phaseBadgeClass(run.phase))}>{shortPhase(run.phase)}</span>
    </button>
  )
}

function FactTile({ label, value, tone, mono }: { label: string; value: string | number; tone?: string; mono?: boolean }) {
  return (
    <div className="rounded-md border border-theme-border bg-theme-surface px-3 py-2">
      <div className="text-[10px] font-medium uppercase tracking-wide text-theme-text-tertiary">{label}</div>
      <div className={clsx('mt-1 truncate text-sm font-semibold text-theme-text-primary', mono && 'font-mono', tone && phaseTextClass(tone))}>{value}</div>
    </div>
  )
}

function phaseBadgeClass(phase: string): string {
  switch (phase) {
    case 'Succeeded':
    case 'Complete':
      return 'status-healthy'
    case 'Running':
      return 'status-neutral'
    case 'Failed':
    case 'Error':
      return 'status-unhealthy'
    case 'Pending':
    case 'Suspended':
      return 'status-degraded'
    default:
      return 'status-unknown'
  }
}

function phaseTone(phase: string): string {
  switch (phase) {
    case 'Succeeded':
    case 'Complete':
      return 'success'
    case 'Running':
    case 'Idle':
      return 'info'
    case 'Failed':
    case 'Error':
      return 'error'
    case 'Pending':
    case 'Suspended':
      return 'warning'
    default:
      return 'neutral'
  }
}

function phaseTextClass(tone: string): string {
  switch (tone) {
    case 'success':
      return 'text-emerald-600 dark:text-emerald-400'
    case 'warning':
      return 'text-amber-600 dark:text-amber-400'
    case 'error':
      return 'text-red-600 dark:text-red-400'
    case 'info':
      return 'text-sky-600 dark:text-sky-400'
    default:
      return ''
  }
}

function phaseHealth(phase: string): 'healthy' | 'degraded' | 'unhealthy' | 'neutral' | 'unknown' {
  switch (phase) {
    case 'Succeeded':
    case 'Complete':
      return 'healthy'
    case 'Running':
      return 'neutral'
    case 'Failed':
    case 'Error':
      return 'unhealthy'
    case 'Pending':
    case 'Suspended':
      return 'degraded'
    default:
      return 'unknown'
  }
}

function shortPhase(phase: string): string {
  if (phase === 'Succeeded') return 'OK'
  if (phase === 'Running') return 'Run'
  if (phase === 'Pending') return 'Pend'
  return phase
}

function countPhases(runs: WorkloadRun[]) {
  return {
    running: runs.filter((run) => run.active).length,
    failed: runs.filter((run) => run.phase === 'Failed' || run.phase === 'Error').length,
    succeeded: runs.filter((run) => run.phase === 'Succeeded').length,
  }
}

function resourceTargetForRun(run: WorkloadRun) {
  return {
    kind: run.kind,
    namespace: run.namespace,
    name: run.name,
    group: run.kind === 'workflows' ? 'argoproj.io' : undefined,
  }
}

function pluralKind(kind: string): string {
  switch (kind) {
    case 'Pod': return 'pods'
    case 'Job': return 'jobs'
    case 'CronJob': return 'cronjobs'
    case 'Workflow': return 'workflows'
    case 'CronWorkflow': return 'cronworkflows'
    case 'WorkflowTemplate': return 'workflowtemplates'
    case 'ClusterWorkflowTemplate': return 'clusterworkflowtemplates'
    case 'ScaledJob': return 'scaledjobs'
    default: return kind.toLowerCase()
  }
}

function workCount(run: WorkloadRun): string {
  if (run.podTotal) {
    if (run.podRunning) return `${run.podRunning} running ${pluralize('pod', run.podRunning)}`
    if (run.podPending && !run.podSucceeded && !run.podFailed) return `${run.podPending} pending ${pluralize('pod', run.podPending)}`
    if (run.podFailed && run.phase !== 'Succeeded') return `${run.podFailed}/${run.podTotal} pods failed`
    return `${run.podSucceeded ?? 0}/${run.podTotal} pods`
  }
  if (run.desired) return `${run.succeeded ?? 0}/${run.desired} completions`
  return 'work unknown'
}

function jobRetryLimitValue(resource: any): string {
  const limit = resource?.spec?.backoffLimit
  const n = Number(limit ?? 6)
  return `${n} ${n === 1 ? 'retry' : 'retries'}`
}

function podBreakdown(run: WorkloadRun, workflowExecution?: WorkflowExecutionModel | null): string | null {
  const counts = workflowExecution?.counts
  const podTotal = counts?.podTotal ?? run.podTotal
  if (!podTotal) return null
  const parts = [
    (counts?.podRunning ?? run.podRunning) ? `${counts?.podRunning ?? run.podRunning} running` : '',
    (counts?.podSucceeded ?? run.podSucceeded) ? `${counts?.podSucceeded ?? run.podSucceeded} succeeded` : '',
    (counts?.podFailed ?? run.podFailed) ? `${counts?.podFailed ?? run.podFailed} failed` : '',
    (counts?.podPending ?? run.podPending) ? `${counts?.podPending ?? run.podPending} pending` : '',
  ].filter(Boolean)
  return parts.join(' · ') || `${podTotal} total`
}

function executionNodeBreakdown(workflowExecution?: WorkflowExecutionModel | null): string | null {
  const counts = workflowExecution?.counts
  if (!counts?.nodeTotal) return null
  const parts = [
    counts.nodeRunning ? `${counts.nodeRunning} running` : '',
    counts.nodeSucceeded ? `${counts.nodeSucceeded} succeeded` : '',
    counts.nodeFailed ? `${counts.nodeFailed} failed` : '',
    counts.nodeSkipped ? `${counts.nodeSkipped} skipped` : '',
  ].filter(Boolean)
  return parts.join(' · ') || `${counts.nodeTotal} total`
}

function formatScheduleDelay(run: WorkloadRun): string {
  if (!run.scheduledAt || !run.startedAt) return ''
  const scheduled = Date.parse(run.scheduledAt)
  const started = Date.parse(run.startedAt)
  if (Number.isNaN(scheduled) || Number.isNaN(started) || started < scheduled) return ''
  return formatDuration(started - scheduled)
}

function formatResourceDuration(resources: Record<string, number>): string {
  return Object.entries(resources)
    .map(([resource, seconds]) => `${resource}: ${formatDuration(seconds * 1000)}`)
    .join(' · ')
}

function jobActivity(run: WorkloadRun, resource: any): WorkflowExecutionActivity[] {
  const items: WorkflowExecutionActivity[] = []
  if (run.scheduledAt) items.push({ id: 'scheduled', at: run.scheduledAt, label: 'Cron scheduled', tone: 'info' })
  if (run.startedAt) items.push({ id: 'started', at: run.startedAt, label: 'Job started', tone: 'info' })
  const conditions = Array.isArray(resource?.status?.conditions) ? resource.status.conditions : []
  const hasComplete = hasJobCondition(conditions, 'Complete')
  const hasFailed = hasJobCondition(conditions, 'Failed')
  for (const condition of conditions) {
    const at = condition.lastTransitionTime || condition.lastProbeTime
    if (!at || condition.status !== 'True') continue
    if (condition.type === 'SuccessCriteriaMet' && hasComplete) continue
    if (condition.type === 'FailureTarget' && hasFailed) continue
    const mapped = jobConditionActivity(condition.type)
    items.push({
      id: `condition-${condition.type}`,
      at,
      label: mapped.label,
      detail: condition.message || condition.reason,
      tone: mapped.tone,
    })
  }
  if (items.length === 0 && run.finishedAt) {
    items.push({ id: 'finished', at: run.finishedAt, label: `Job ${run.phase.toLowerCase()}`, detail: run.message, tone: run.phase === 'Failed' ? 'danger' : 'success' })
  }
  return items.sort((a, b) => Date.parse(a.at) - Date.parse(b.at))
}

function hasJobCondition(conditions: any[], type: string): boolean {
  return conditions.some((condition) => condition?.type === type && condition?.status === 'True')
}

function jobConditionActivity(type: string): { label: string; tone: WorkflowExecutionActivity['tone'] } {
  switch (type) {
    case 'Complete':
      return { label: 'Job completed', tone: 'success' }
    case 'Failed':
      return { label: 'Job failed', tone: 'danger' }
    case 'FailureTarget':
      return { label: 'Job is failing', tone: 'danger' }
    case 'SuccessCriteriaMet':
      return { label: 'Completion target reached', tone: 'success' }
    case 'Suspended':
      return { label: 'Job suspended', tone: 'warning' }
    default:
      return { label: splitConditionName(type), tone: 'info' }
  }
}

function splitConditionName(type: string): string {
  return type
    .replace(/([a-z0-9])([A-Z])/g, '$1 $2')
    .replace(/_/g, ' ')
}

function toneDotClass(tone: string): string {
  switch (tone) {
    case 'success':
      return 'bg-emerald-500'
    case 'danger':
      return 'bg-red-500'
    case 'warning':
      return 'bg-amber-500'
    case 'info':
      return 'bg-accent'
    default:
      return 'bg-theme-border'
  }
}

function formatRunDuration(run: WorkloadRun): string {
  if (!run.startedAt) return ''
  const start = Date.parse(run.startedAt)
  const end = run.finishedAt ? Date.parse(run.finishedAt) : Date.now()
  if (Number.isNaN(start) || Number.isNaN(end) || end < start) return ''
  return formatDuration(end - start)
}

function formatRunTime(run: WorkloadRun): string {
  const raw = run.startedAt || run.scheduledAt || run.finishedAt
  return raw ? formatAge(raw) : ''
}

function formatAge(value: string): string {
  const t = Date.parse(value)
  if (Number.isNaN(t)) return value
  const diff = Date.now() - t
  if (diff < 60_000) return 'just now'
  return `${formatDuration(diff)} ago`
}

function formatDuration(ms: number): string {
  const seconds = Math.max(0, Math.floor(ms / 1000))
  if (seconds < 60) return `${seconds}s`
  const minutes = Math.floor(seconds / 60)
  if (minutes < 60) return `${minutes}m ${seconds % 60}s`
  const hours = Math.floor(minutes / 60)
  if (hours < 48) return `${hours}h ${minutes % 60}m`
  const days = Math.floor(hours / 24)
  return `${days}d ${hours % 24}h`
}

function pluralizeRuns(count: number): string {
  return count === 1 ? '1 retained run' : `${count} retained runs`
}

function runKindPluralForSchedule(kind: string): string {
  if (kind === 'CronJob' || kind === 'ScaledJob') return 'Jobs'
  if (kind === 'CronWorkflow' || kind === 'WorkflowTemplate' || kind === 'ClusterWorkflowTemplate') return 'Workflows'
  return 'runs'
}

function emptyRunsCopy(kind: string, resource: any): { headline: string; body: string } {
  if (isTemplateKind(kind)) {
    return { headline: 'No retained Workflows use this definition', body: 'No readable Workflow objects currently reference this definition.' }
  }
  const lastScheduled = resource?.status?.lastScheduleTime || resource?.status?.lastScheduledTime
  if (lastScheduled) {
    return { headline: 'No retained runs', body: `This resource last scheduled work ${formatAge(lastScheduled)}, but its ${runKindPluralForSchedule(kind)} are no longer retained in Kubernetes.` }
  }
  return { headline: 'No runs yet', body: `Kubernetes does not currently have retained ${runKindPluralForSchedule(kind)} for this resource.` }
}

function pluralize(word: string, count: number): string {
  return count === 1 ? word : `${word}s`
}
