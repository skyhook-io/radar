import { describe, expect, it } from 'vitest'
import {
  aggregateMatrixStatuses,
  applyTaskRunStatuses,
  buildChildTaskRunRefs,
  buildPipelineTaskGraph,
  buildSkippedTaskReasons,
  getTektonPipelineStatus,
  type TektonTaskNode,
} from './resource-utils-tekton'

function depsOf(nodes: TektonTaskNode[], name: string): string[] {
  return nodes.find((n) => n.name === name)?.dependsOn ?? []
}

describe('getTektonPipelineStatus', () => {
  it('counts finally tasks alongside regular tasks', () => {
    const got = getTektonPipelineStatus({
      spec: { tasks: [{ name: 'build' }, { name: 'deploy' }], finally: [{ name: 'notify' }] },
    })
    expect(got.text).toBe('3 tasks')
  })
})

describe('buildPipelineTaskGraph', () => {
  it('reads explicit runAfter as a direct dependency', () => {
    const nodes = buildPipelineTaskGraph({
      tasks: [
        { name: 'a', params: [] },
        { name: 'b', runAfter: ['a'], params: [] },
      ],
    })
    expect(depsOf(nodes, 'b')).toEqual(['a'])
    expect(depsOf(nodes, 'a')).toEqual([])
  })

  it('infers a dependency from a $(tasks.X.results.Y) param reference', () => {
    const nodes = buildPipelineTaskGraph({
      tasks: [
        { name: 'a', params: [] },
        { name: 'b', params: [{ name: 'in', value: '$(tasks.a.results.out)' }] },
      ],
    })
    expect(depsOf(nodes, 'b')).toEqual(['a'])
  })

  // Pins the real bug found live on the "build" Pipeline (platform-catalog
  // namespace): build-source's only runAfter is start-build-stage-span, but
  // it also pipes a trace ID from start-flow and a config blob from
  // validate-config into its params — both genuine result-ref dependencies,
  // and both already implied by the runAfter chain
  // (start-build-stage-span <- start-flow <- validate-config). Drawing all
  // three as direct edges put 3 arrows into build-source in the DAG instead
  // of the single "runs right after start-build-stage-span" edge a reader
  // expects.
  it('drops a result-ref dependency already implied by another dependency (transitive reduction)', () => {
    const nodes = buildPipelineTaskGraph({
      tasks: [
        { name: 'clone-repo', params: [] },
        { name: 'validate-config', runAfter: ['clone-repo'], params: [] },
        { name: 'start-flow', runAfter: ['validate-config'], params: [] },
        {
          name: 'start-build-stage-span',
          runAfter: ['start-flow'],
          params: [{ name: 'flow-traceparent', value: '$(tasks.start-flow.results.traceparent)' }],
        },
        {
          name: 'build-source',
          runAfter: ['start-build-stage-span'],
          params: [
            { name: 'config-json', value: '$(tasks.validate-config.results.config-json)' },
            { name: 'flow-traceparent', value: '$(tasks.start-flow.results.traceparent)' },
            { name: 'stage-span-id', value: '$(tasks.start-build-stage-span.results.span-id)' },
          ],
        },
      ],
    })
    expect(depsOf(nodes, 'build-source')).toEqual(['start-build-stage-span'])
  })

  it('keeps a result-ref dependency that is NOT reachable through another dependency', () => {
    // c depends on both a and b, and b is unrelated to a — neither implies
    // the other, so both direct edges are real and must survive reduction.
    const nodes = buildPipelineTaskGraph({
      tasks: [
        { name: 'a', params: [] },
        { name: 'b', params: [] },
        {
          name: 'c',
          params: [
            { name: 'x', value: '$(tasks.a.results.out)' },
            { name: 'y', value: '$(tasks.b.results.out)' },
          ],
        },
      ],
    })
    expect(depsOf(nodes, 'c').sort()).toEqual(['a', 'b'])
  })

  it('preserves a genuine diamond (fan-out then fan-in) without over-pruning', () => {
    const nodes = buildPipelineTaskGraph({
      tasks: [
        { name: 'start', params: [] },
        { name: 'left', runAfter: ['start'], params: [] },
        { name: 'right', runAfter: ['start'], params: [] },
        { name: 'join', runAfter: ['left', 'right'], params: [] },
      ],
    })
    expect(depsOf(nodes, 'join').sort()).toEqual(['left', 'right'])
  })

  describe('finally tasks', () => {
    it('includes a finally task as a node, tagged isFinally', () => {
      const nodes = buildPipelineTaskGraph({
        tasks: [{ name: 'build', params: [] }],
        finally: [{ name: 'notify', params: [] }],
      })
      expect(nodes.map((n) => n.name).sort()).toEqual(['build', 'notify'])
      expect(nodes.find((n) => n.name === 'notify')?.isFinally).toBe(true)
      expect(nodes.find((n) => n.name === 'build')?.isFinally).toBeUndefined()
    })

    it('depends on every terminal regular task, since Tekton waits for all of them regardless of result-refs', () => {
      // build and lint both run after start and don't feed each other —
      // notify has to wait for both, even though it only reads build's result.
      const nodes = buildPipelineTaskGraph({
        tasks: [
          { name: 'start', params: [] },
          { name: 'build', runAfter: ['start'], params: [] },
          { name: 'lint', runAfter: ['start'], params: [] },
        ],
        finally: [
          { name: 'notify', params: [{ name: 'status', value: '$(tasks.build.results.outcome)' }] },
        ],
      })
      expect(depsOf(nodes, 'notify').sort()).toEqual(['build', 'lint'])
    })

    it('depends only on the single terminal task in a linear pipeline (no fan-out to prune)', () => {
      const nodes = buildPipelineTaskGraph({
        tasks: [
          { name: 'clone', params: [] },
          { name: 'build', runAfter: ['clone'], params: [] },
        ],
        finally: [{ name: 'cleanup', params: [] }],
      })
      expect(depsOf(nodes, 'cleanup')).toEqual(['build'])
    })

    it('never depends on another finally task', () => {
      const nodes = buildPipelineTaskGraph({
        tasks: [{ name: 'build', params: [] }],
        finally: [
          { name: 'notify-slack', params: [] },
          { name: 'notify-email', params: [] },
        ],
      })
      expect(depsOf(nodes, 'notify-slack')).toEqual(['build'])
      expect(depsOf(nodes, 'notify-email')).toEqual(['build'])
    })
  })
})

describe('buildChildTaskRunRefs', () => {
  it('returns one entry for a non-matrix task', () => {
    const refs = buildChildTaskRunRefs({
      childReferences: [{ kind: 'TaskRun', pipelineTaskName: 'build', name: 'run-build' }],
    })
    expect(refs.get('build')).toEqual([{ taskRunName: 'run-build' }])
  })

  it('keeps every childReference for a matrix task instead of overwriting to the last one', () => {
    const refs = buildChildTaskRunRefs({
      childReferences: [
        { kind: 'TaskRun', pipelineTaskName: 'test', name: 'run-test-0' },
        { kind: 'TaskRun', pipelineTaskName: 'test', name: 'run-test-1' },
        { kind: 'TaskRun', pipelineTaskName: 'test', name: 'run-test-2' },
      ],
    })
    expect(refs.get('test')).toEqual([
      { taskRunName: 'run-test-0' },
      { taskRunName: 'run-test-1' },
      { taskRunName: 'run-test-2' },
    ])
  })
})

describe('aggregateMatrixStatuses', () => {
  it('is a no-op for a single entry', () => {
    const got = aggregateMatrixStatuses([{ status: 'succeeded', taskRunName: 'a' }])
    expect(got).toEqual({ status: 'succeeded', taskRunName: 'a' })
  })

  it('a failure wins over succeeded and running siblings', () => {
    const got = aggregateMatrixStatuses([
      { status: 'succeeded', taskRunName: 'a' },
      { status: 'failed', reason: 'TaskRunTimeout', taskRunName: 'b' },
      { status: 'running', taskRunName: 'c' },
    ])
    expect(got).toEqual({ status: 'failed', reason: 'TaskRunTimeout', taskRunName: 'b' })
  })

  it('running wins over pending/unknown/skipped/succeeded when nothing failed', () => {
    const got = aggregateMatrixStatuses([
      { status: 'succeeded', taskRunName: 'a' },
      { status: 'skipped', taskRunName: 'b' },
      { status: 'running', taskRunName: 'c' },
      { status: 'pending', taskRunName: 'd' },
    ])
    expect(got.status).toBe('running')
  })

  it('succeeded only wins when every sibling succeeded', () => {
    const got = aggregateMatrixStatuses([
      { status: 'succeeded', taskRunName: 'a' },
      { status: 'succeeded', taskRunName: 'b' },
    ])
    expect(got.status).toBe('succeeded')
  })

  // A matrix sibling whose TaskRun/pod was already garbage-collected fetches
  // as 'unknown' forever (not just while loading) — that one permanently-
  // unresolvable child must not mask that the other nine are known-succeeded.
  it('a known status wins over unknown — one GC-raced sibling does not mask the rest', () => {
    const got = aggregateMatrixStatuses([
      { status: 'succeeded', taskRunName: 'a' },
      { status: 'succeeded', taskRunName: 'b' },
      { status: 'unknown', taskRunName: 'c' },
    ])
    expect(got.status).toBe('succeeded')
  })

  it('unknown only wins when every sibling is unknown', () => {
    const got = aggregateMatrixStatuses([
      { status: 'unknown', taskRunName: 'a' },
      { status: 'unknown', taskRunName: 'b' },
    ])
    expect(got.status).toBe('unknown')
  })
})

describe('buildSkippedTaskReasons', () => {
  it('reads name + reason off status.skippedTasks', () => {
    const reasons = buildSkippedTaskReasons({
      skippedTasks: [{ name: 'deploy-canary', reason: 'When Expressions evaluated to false' }],
    })
    expect(reasons.get('deploy-canary')).toBe('When Expressions evaluated to false')
  })

  it('is empty for a status with no skippedTasks', () => {
    expect(buildSkippedTaskReasons({}).size).toBe(0)
  })
})

describe('applyTaskRunStatuses', () => {
  const tasks: TektonTaskNode[] = [
    { name: 'build', dependsOn: [] },
    { name: 'deploy-canary', dependsOn: ['build'] },
  ]

  it('a task named in skippedTaskReasons is skipped, not pending, when it has no live status', () => {
    const got = applyTaskRunStatuses(tasks, new Map(), buildSkippedTaskReasons({
      skippedTasks: [{ name: 'deploy-canary', reason: 'When Expressions evaluated to false' }],
    }))
    const deploy = got.find((t) => t.name === 'deploy-canary')
    expect(deploy?.status).toBe('skipped')
    expect(deploy?.reason).toBe('When Expressions evaluated to false')
  })

  it('falls back to pending when a task is neither live nor listed as skipped', () => {
    const got = applyTaskRunStatuses(tasks, new Map())
    expect(got.find((t) => t.name === 'deploy-canary')?.status).toBe('pending')
  })

  it('a live status always wins over a skipped-tasks listing for the same task', () => {
    // Shouldn't happen in practice (Tekton never creates a childReference for
    // a task it also lists in skippedTasks), but live data must win if it did.
    const got = applyTaskRunStatuses(
      tasks,
      new Map([['deploy-canary', { status: 'succeeded', taskRunName: 'run-deploy-canary' }]]),
      buildSkippedTaskReasons({ skippedTasks: [{ name: 'deploy-canary', reason: 'stale' }] }),
    )
    expect(got.find((t) => t.name === 'deploy-canary')?.status).toBe('succeeded')
  })
})
