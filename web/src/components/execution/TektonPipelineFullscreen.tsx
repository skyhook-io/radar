import { useCallback, useMemo, useRef } from 'react'
import { useQueries } from '@tanstack/react-query'
import { PipelineDagView } from '@skyhook-io/k8s-ui/components/resources/renderers/PipelineDagView'
import {
  aggregateMatrixStatuses,
  applyTaskRunStatuses,
  buildChildTaskRunRefs,
  buildPipelineTaskGraph,
  buildSkippedTaskReasons,
  tektonNodeStatusFromConditions,
  tektonRefName,
  type TektonTaskNode,
  type TektonTaskNodeStatus,
} from '@skyhook-io/k8s-ui/components/resources/resource-utils-tekton'
import { fetchJSON } from '../../api/client'

interface TektonPipelineFullscreenProps {
  kind: string
  namespace: string
  name: string
  resource: any
  onNavigateToResource?: (resource: { kind: string; namespace: string; name: string; group?: string }) => void
}

// Fullscreen "full view" overview for Pipeline/PipelineRun — the DAG's real
// home (see PipelineDagView's header comment for why it's not in the compact
// drawer). Pipeline gets the static declared graph; PipelineRun fans out one
// fetch per status.childReferences entry for live per-task coloring, the
// same pattern CompositeRenderer uses for composed-resource status.
export function TektonPipelineFullscreen({ kind, namespace, name, resource, onNavigateToResource }: TektonPipelineFullscreenProps) {
  const isRun = kind === 'pipelineruns'
  const status = resource?.status ?? {}
  const pipelineSpec = isRun ? (status.pipelineSpec ?? {}) : (resource?.spec ?? {})
  const declaredTasks = useMemo(() => buildPipelineTaskGraph(pipelineSpec), [pipelineSpec])

  const childRefs = useMemo(
    () => (isRun ? buildChildTaskRunRefs(status) : new Map<string, { taskRunName: string }[]>()),
    [isRun, status],
  )
  // Flattened one entry per actual TaskRun — a matrix task contributes
  // several entries sharing the same pipelineTaskName, each needing its own
  // fetch, not just the first/last one.
  const flatChildren = useMemo(
    () => [...childRefs.entries()].flatMap(([pipelineTaskName, refs]) => refs.map((ref) => [pipelineTaskName, ref] as const)),
    [childRefs],
  )

  const queries = useQueries({
    queries: flatChildren.map(([, ref]) => ({
      queryKey: ['resource', 'taskruns', namespace, ref.taskRunName, 'tekton.dev'],
      queryFn: async () => fetchJSON<{ resource: any }>(`/resources/taskruns/${namespace || '_'}/${ref.taskRunName}?group=tekton.dev`),
      staleTime: 5000,
      refetchInterval: 5000,
      retry: false,
      enabled: Boolean(ref.taskRunName && namespace),
    })),
  })

  const rawTasks: TektonTaskNode[] = useMemo(() => {
    if (!isRun) return declaredTasks
    const liveByTaskName = new Map<string, Array<{ status: TektonTaskNodeStatus; reason?: string; taskRunName: string }>>()
    flatChildren.forEach(([pipelineTaskName, ref], i) => {
      const q = queries[i]
      const live = q.isLoading || !q.data
        ? { status: 'unknown' as const, taskRunName: ref.taskRunName }
        : { ...tektonNodeStatusFromConditions(q.data.resource?.status?.conditions), taskRunName: ref.taskRunName }
      const existing = liveByTaskName.get(pipelineTaskName)
      if (existing) existing.push(live)
      else liveByTaskName.set(pipelineTaskName, [live])
    })
    // A non-matrix task's array always has exactly one entry, so aggregation
    // is a no-op there — only a matrix task's several parallel expansions
    // actually get collapsed, worst-status-wins, into the one node the DAG
    // shows.
    const statusByTaskName = new Map<string, { status: TektonTaskNodeStatus; reason?: string; taskRunName?: string }>()
    for (const [pipelineTaskName, live] of liveByTaskName) {
      statusByTaskName.set(pipelineTaskName, aggregateMatrixStatuses(live))
    }
    return applyTaskRunStatuses(declaredTasks, statusByTaskName, buildSkippedTaskReasons(status))
    // queries is a fresh array each render from useQueries; flatChildren + isLoading/data
    // per-entry is what actually needs to retrigger this, which the queries objects
    // capture already — omitting `queries` itself from deps would be wrong since its
    // contents (not identity) are what we read, so keep it.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [isRun, declaredTasks, flatChildren, queries, status])

  // Stabilize the array reference by content, not just recompute on every
  // render. `queries` is a fresh array from useQueries every render (even
  // when no query's data actually changed), so rawTasks above is a brand-new
  // array on every single poll tick. Feeding ReactFlow a "new" nodes array
  // that frequently — even when every task's status is byte-identical to
  // before — makes it re-enter its measure-via-ResizeObserver path, which
  // can get stuck hiding nodes/edges indefinitely (the on-screen size never
  // actually changes, so the observer has nothing to report back).
  const tasksSigRef = useRef('')
  const tasksRef = useRef(rawTasks)
  const sig = rawTasks.map((t) => `${t.name}:${t.dependsOn.join(',')}:${t.status ?? ''}:${t.reason ?? ''}:${t.taskRunName ?? ''}`).join('|')
  if (sig !== tasksSigRef.current) {
    tasksSigRef.current = sig
    tasksRef.current = rawTasks
  }
  const tasks = tasksRef.current

  const pipelineRefLabel = isRun ? tektonRefName(resource?.spec?.pipelineRef) : name

  // Stable across renders (as long as namespace/onNavigateToResource don't
  // change) — an inline arrow prop here would, like the unstabilized tasks
  // array above, hand ReactFlow a "new" nodes array on every poll tick even
  // when nothing visible changed.
  const handleTaskClick = useCallback(
    (task: TektonTaskNode) => {
      if (!task.taskRunName || !onNavigateToResource) return
      onNavigateToResource({ kind: 'taskruns', namespace, name: task.taskRunName, group: 'tekton.dev' })
    },
    [namespace, onNavigateToResource],
  )

  return (
    <div className="flex h-full flex-col gap-3 p-4">
      <div className="shrink-0 text-sm text-theme-text-secondary">
        {isRun ? (
          <>Pipeline <span className="font-medium text-theme-text-primary">{pipelineRefLabel}</span> · {tasks.length} task{tasks.length === 1 ? '' : 's'}</>
        ) : (
          <>{tasks.length} task{tasks.length === 1 ? '' : 's'} declared</>
        )}
      </div>
      <div className="min-h-0 flex-1">
        <PipelineDagView tasks={tasks} onTaskClick={handleTaskClick} />
      </div>
    </div>
  )
}
