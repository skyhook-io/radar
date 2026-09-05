// Tekton Pipelines CRD utility functions.
//
// Targets tekton.dev/v1 status shapes:
//   - PipelineRun.status.childReferences[] (name + pipelineTaskName), not the
//     older v1beta1 inline `status.taskRuns` map — a TaskRun's own status
//     must be fetched separately.
//   - PipelineRun.status.pipelineSpec carries the Pipeline's spec.tasks at
//     run time, so a PipelineRun's DAG never needs a second fetch for the
//     Pipeline object itself.

import type { StatusBadge } from './resource-utils'
import { healthColors } from './resource-utils'

// ============================================================================
// STATUS UTILITIES
// ============================================================================

// Every Tekton run object (PipelineRun, TaskRun) reports progress through a
// single `status.conditions[].type === 'Succeeded'` condition: True/False/
// Unknown, plus a `reason` that names *why* (Running, Failed, TaskRunTimeout,
// PipelineRunTimeout, Cancelled, ...). Unlike Flux's Ready condition, an
// Unknown status is the normal in-progress state, not an unhealthy one.
function succeededCondition(conditions: any[] | undefined): { status?: string; reason?: string; message?: string } | undefined {
  return (conditions ?? []).find((c: any) => c?.type === 'Succeeded')
}

function runStatus(conditions: any[] | undefined, noRunsYetText: string): StatusBadge {
  const cond = succeededCondition(conditions)
  if (!cond) return { text: noRunsYetText, color: healthColors.unknown, level: 'unknown' }
  const reason = cond.reason || ''
  if (cond.status === 'True') return { text: reason || 'Succeeded', color: healthColors.healthy, level: 'healthy' }
  if (cond.status === 'False') {
    if (reason === 'Cancelled' || reason.endsWith('Cancelled')) {
      return { text: reason, color: healthColors.degraded, level: 'degraded' }
    }
    return { text: reason || 'Failed', color: healthColors.unhealthy, level: 'unhealthy' }
  }
  // Unknown: actively running unless the reason says otherwise (e.g. a
  // PipelineRun/TaskRun that's been created but whose controller hasn't
  // picked it up yet still reports Unknown/Started or Unknown/Pending).
  return { text: reason || 'Running', color: healthColors.neutral, level: 'neutral' }
}

// A Pipeline is a template, not a run — it carries no status.conditions of
// its own. "status" here just means "is this a well-formed, usable
// template," which in practice means it has at least one task.
export function getTektonPipelineStatus(pipeline: any): StatusBadge {
  const taskCount = (pipeline?.spec?.tasks ?? []).length + (pipeline?.spec?.finally ?? []).length
  if (taskCount === 0) return { text: 'Empty', color: healthColors.degraded, level: 'degraded' }
  return { text: `${taskCount} task${taskCount === 1 ? '' : 's'}`, color: healthColors.healthy, level: 'healthy' }
}

export function getTektonPipelineRunStatus(pipelineRun: any): StatusBadge {
  return runStatus(pipelineRun?.status?.conditions, 'Pending')
}

export function getTektonTaskRunStatus(taskRun: any): StatusBadge {
  return runStatus(taskRun?.status?.conditions, 'Pending')
}

// A Pipeline/Task reference is either a plain `.name`, or resolver-based
// (cluster/git/bundle resolvers), which carries the target name as a
// resolver param instead — common once a cluster moves Pipelines/Tasks out
// of raw YAML into a catalog resolved at run time.
export function tektonRefName(ref: any): string {
  if (!ref) return '(inline)'
  if (ref.name) return ref.name
  const nameParam = (ref.params ?? []).find((p: any) => p?.name === 'name')
  return nameParam?.value ? `${nameParam.value} (via ${ref.resolver} resolver)` : `(${ref.resolver ?? 'unknown'} resolver)`
}

// ============================================================================
// TASK DAG
// ============================================================================

export type TektonTaskNodeStatus = 'succeeded' | 'failed' | 'running' | 'pending' | 'skipped' | 'unknown'

export interface TektonTaskNode {
  name: string
  dependsOn: string[]
  status?: TektonTaskNodeStatus
  reason?: string
  // Set once a live PipelineRun has actually created this task's TaskRun —
  // absent for a Pipeline's static template graph, and for a task the run
  // hasn't reached yet. Presence is what makes a DAG node clickable: nothing
  // to open for a task that never ran.
  taskRunName?: string
  // True for a spec.finally task. Tekton runs these only after every regular
  // PipelineTask has settled (success or failure) — consumers that want to
  // set them apart visually (a cleanup/notify row after the main DAG) can key
  // off this instead of re-deriving it from dependsOn.
  isFinally?: boolean
}

// Tekton infers task ordering two ways: an explicit `runAfter` list, and
// implicitly whenever a task consumes another task's results via
// `$(tasks.<name>.results.*)` (in params, `when` expressions, or
// `matrix.params`) — the second form is common enough (result piping without
// a redundant explicit runAfter) that reading `runAfter` alone under-counts
// real edges in the DAG.
const RESULT_REF_PATTERN = /\$\(tasks\.([a-zA-Z0-9_.-]+)\.results\./g

function resultRefsIn(value: unknown, out: Set<string>): void {
  if (typeof value === 'string') {
    for (const m of value.matchAll(RESULT_REF_PATTERN)) out.add(m[1])
    return
  }
  if (Array.isArray(value)) {
    for (const v of value) resultRefsIn(v, out)
    return
  }
  if (value && typeof value === 'object') {
    for (const v of Object.values(value)) resultRefsIn(v, out)
  }
}

export function buildPipelineTaskGraph(pipelineSpec: any): TektonTaskNode[] {
  const tasks: any[] = pipelineSpec?.tasks ?? []
  const regularNames = new Set<string>(tasks.map((t) => t.name))
  const regularNodes = tasks.map((task) => {
    const deps = new Set<string>(task.runAfter ?? [])
    resultRefsIn(task.params, deps)
    resultRefsIn(task.when, deps)
    resultRefsIn(task.matrix, deps)
    deps.delete(task.name)
    return { name: task.name, dependsOn: [...deps] }
  })
  // finally tasks don't support runAfter (Tekton rejects the field there) —
  // the ordering constraint is implicit and absolute: every finally task
  // starts only once ALL regular PipelineTasks have settled, success or
  // failure, not just the ones it happens to read a result from. Model that
  // as a dependency on every regular task with no other regular task
  // depending on it (the DAG's own terminal/leaf tasks) — reduceToDirectDeps
  // below then drops any of a finally task's own result-ref deps that are
  // already implied by one of those barrier edges.
  const dependedOn = new Set<string>(regularNodes.flatMap((n) => n.dependsOn))
  const terminalRegularTasks = regularNodes.filter((n) => !dependedOn.has(n.name)).map((n) => n.name)
  const finallyTasks: any[] = pipelineSpec?.finally ?? []
  const finallyNodes = finallyTasks.map((task) => {
    const deps = new Set<string>(terminalRegularTasks)
    resultRefsIn(task.params, deps)
    resultRefsIn(task.when, deps)
    // A finally task's result-refs only ever point at regular tasks, never at
    // other finally tasks (Tekton doesn't allow it) — but guard anyway so a
    // malformed spec can't produce a dep on an unknown/finally name.
    for (const d of deps) if (!regularNames.has(d)) deps.delete(d)
    deps.delete(task.name)
    return { name: task.name, dependsOn: [...deps], isFinally: true }
  })
  return reduceToDirectDeps([...regularNodes, ...finallyNodes])
}

// reduceToDirectDeps computes the transitive reduction of the dependency
// graph: drops a dep `d` from a task whenever `d` is already an ancestor of
// one of that task's *other* deps, since the ordering it implies is already
// guaranteed by that longer path. Real Tekton pipelines commonly pipe a
// value (a trace ID, a shared config blob) from an early task like
// start-flow into nearly every later task's params — each of those is a
// genuine result-ref dependency (correct per Tekton's own scheduling), but
// drawing it as a direct edge alongside the task's actual immediate
// runAfter parent produces a fan of redundant arrows into one node instead
// of the single "what does this run right after" edge a reader expects.
function reduceToDirectDeps(nodes: TektonTaskNode[]): TektonTaskNode[] {
  const depsByName = new Map(nodes.map((n) => [n.name, n.dependsOn]))
  const ancestorsCache = new Map<string, Set<string>>()
  function ancestorsOf(name: string, seen: Set<string> = new Set()): Set<string> {
    const cached = ancestorsCache.get(name)
    if (cached) return cached
    if (seen.has(name)) return new Set()
    seen.add(name)
    const result = new Set<string>()
    for (const dep of depsByName.get(name) ?? []) {
      result.add(dep)
      for (const anc of ancestorsOf(dep, seen)) result.add(anc)
    }
    ancestorsCache.set(name, result)
    return result
  }
  return nodes.map((node) => {
    const direct = node.dependsOn.filter((dep) =>
      !node.dependsOn.some((other) => other !== dep && ancestorsOf(other).has(dep)),
    )
    return { ...node, dependsOn: direct }
  })
}

// A PipelineRun's status.childReferences names each TaskRun it created,
// keyed by pipelineTaskName — but a task the run hasn't reached yet (still
// pending, or skipped by a `when` guard) has no childReference at all, so
// absence from the map means "not started," not an error.
//
// A `matrix`-strategy task expands into several childReferences that all
// share the same pipelineTaskName (one per parameter combination) — return
// every one of them, in order, rather than keeping only the last. A caller
// that only wants a single representative (a non-matrix task always has
// exactly one) can just read index 0.
export interface TektonChildTaskRun {
  taskRunName: string
  status?: TektonTaskNodeStatus
  reason?: string
}

export function buildChildTaskRunRefs(pipelineRunStatus: any): Map<string, TektonChildTaskRun[]> {
  const refs = new Map<string, TektonChildTaskRun[]>()
  for (const child of pipelineRunStatus?.childReferences ?? []) {
    if (child?.kind === 'TaskRun' && child?.pipelineTaskName && child?.name) {
      const existing = refs.get(child.pipelineTaskName)
      const entry = { taskRunName: child.name }
      if (existing) existing.push(entry)
      else refs.set(child.pipelineTaskName, [entry])
    }
  }
  return refs
}

// STATUS_SEVERITY ranks how "worth surfacing" a status is when a single DAG
// node has to summarize several TaskRuns at once (a matrix task's parallel
// expansions) — the worst outcome wins, matching how any CI dashboard reads
// a fan-out: one failure is the headline even if the other nine succeeded.
//
// unknown ranks last (least severe), not first — it means "this one
// sibling's fetch hasn't resolved (still loading) or never will (its
// TaskRun/pod was already garbage-collected)," not "something is wrong."
// Ranking it above a genuinely-known state would let one permanently-404ing
// matrix child (a common GC race, not an error) mask the fact that the
// other nine are known to have succeeded — every other status here is
// itself an actual outcome Tekton reported, so any of them is more
// informative than "we don't know" and should win.
const STATUS_SEVERITY: Record<TektonTaskNodeStatus, number> = {
  failed: 0,
  running: 1,
  pending: 2,
  skipped: 3,
  succeeded: 4,
  unknown: 5,
}

// aggregateMatrixStatuses collapses a matrix task's several live statuses
// (one per childReferences entry sharing its pipelineTaskName) into the one
// the DAG node shows, plus the TaskRun navigation should land on — the
// worst-ranked entry in both cases, so a click always reaches an actionable
// (e.g. failed) run instead of an arbitrary sibling.
export function aggregateMatrixStatuses(
  entries: Array<{ status: TektonTaskNodeStatus; reason?: string; taskRunName: string }>,
): { status: TektonTaskNodeStatus; reason?: string; taskRunName: string } {
  return entries.reduce((worst, entry) =>
    STATUS_SEVERITY[entry.status] < STATUS_SEVERITY[worst.status] ? entry : worst,
  )
}

export function tektonNodeStatusFromConditions(conditions: any[] | undefined): { status: TektonTaskNodeStatus; reason?: string } {
  const cond = succeededCondition(conditions)
  if (!cond) return { status: 'unknown' }
  if (cond.status === 'True') return { status: 'succeeded', reason: cond.reason }
  if (cond.status === 'False') {
    if (cond.reason === 'ConditionCheckFailed' || cond.reason === 'Skipped') {
      return { status: 'skipped', reason: cond.reason }
    }
    return { status: 'failed', reason: cond.reason }
  }
  return { status: 'running', reason: cond.reason }
}

// A task Tekton skips (a false `when` guard, or a parent that failed/was
// itself skipped) never gets a childReference — same absence-from-the-map
// signal as a task that simply hasn't been reached yet. The two cases are
// told apart only by status.skippedTasks[], which names every task the
// controller decided not to run, with the reason it gave for each.
export function buildSkippedTaskReasons(pipelineRunStatus: any): Map<string, string | undefined> {
  const reasons = new Map<string, string | undefined>()
  for (const skipped of pipelineRunStatus?.skippedTasks ?? []) {
    if (skipped?.name) reasons.set(skipped.name, skipped.reason)
  }
  return reasons
}

// Merges the Pipeline's declared task graph with live per-task status. Tasks
// with no matching child are 'skipped' when status.skippedTasks names them
// (a completed run's when-guard/parent-skip outcome), else 'pending' (not
// yet reached) — either way non-clickable, since there's genuinely nothing
// to open.
export function applyTaskRunStatuses(
  tasks: TektonTaskNode[],
  statusByTaskName: Map<string, { status: TektonTaskNodeStatus; reason?: string; taskRunName?: string }>,
  skippedTaskReasons: Map<string, string | undefined> = new Map(),
): TektonTaskNode[] {
  return tasks.map((task) => {
    const live = statusByTaskName.get(task.name)
    if (live) return { ...task, status: live.status, reason: live.reason, taskRunName: live.taskRunName }
    if (skippedTaskReasons.has(task.name)) {
      return { ...task, status: 'skipped', reason: skippedTaskReasons.get(task.name) }
    }
    return { ...task, status: 'pending' }
  })
}
