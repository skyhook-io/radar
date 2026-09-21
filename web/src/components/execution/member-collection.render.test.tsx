import { renderToStaticMarkup } from 'react-dom/server'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import type { WorkloadRun, WorkloadRunsResponse } from '../../api/client'
import { BatchExecutionFullscreen } from './BatchExecutionView'
import { ScheduledWorkloadLogsViewer, workloadRunLogsKey } from '../logs/ScheduledWorkloadLogsViewer'

const state = vi.hoisted(() => ({
  response: { collection: 'members', runs: [], total: 0, truncated: false } as WorkloadRunsResponse | undefined,
  isLoading: false,
  error: undefined as Error | undefined,
  useResource: vi.fn().mockReturnValue({ data: { spec: {} } }),
}))

vi.mock('../../api/client', () => ({
  useResource: (...args: unknown[]) => state.useResource(...args),
  useWorkloadPods: () => ({ data: { pods: [], total: 0, truncated: false } }),
  useWorkloadRuns: () => ({ data: state.response, isLoading: state.isLoading, error: state.error }),
}))
vi.mock('../logs/WorkloadLogsViewer', () => ({
  WorkloadLogsViewer: ({ kind, namespace, name }: { kind: string; namespace: string; name: string }) => <div data-log-target={`${kind}/${namespace}/${name}`}>Logs for {name}</div>,
}))

const member: WorkloadRun = {
  group: 'batch', kind: 'jobs', namespace: 'training', name: 'distributed-workers-0',
  phase: 'Running', active: true,
  jobset: { replicatedJob: 'workers', jobIndex: '0', replicatedJobReplicas: '2', restartAttempt: '0', jobRestartAttempt: '0' },
}

function overview(selectedRunKey?: string, apiVersion = 'jobset.x-k8s.io/v1alpha2') {
  return renderToStaticMarkup(<BatchExecutionFullscreen kind="JobSet" apiKind="jobsets" namespace="training" name="distributed" resource={{ apiVersion, kind: 'JobSet', spec: { replicatedJobs: [{ name: 'workers', replicas: 2 }] } }} selectedRunKey={selectedRunKey} />)
}
function logs(selectedRunKey?: string) {
  return renderToStaticMarkup(<ScheduledWorkloadLogsViewer kind="JobSet" namespace="training" name="distributed" selectedRunKey={selectedRunKey} />)
}

beforeEach(() => {
  state.response = { collection: 'members', runs: [member], total: 1, truncated: false }
  state.isLoading = false
  state.error = undefined
  state.useResource.mockClear()
})

describe('member collection consumers', () => {
  it('keeps the root and declared roles visible during the first member load', () => {
    state.response = undefined
    state.isLoading = true
    const html = overview()
    expect(html).toContain('JobSet overview')
    expect(html).toContain('Role progress')
    expect(html).toContain('Loading Jobs')
    expect(html).not.toContain('No child Jobs currently retained')
    expect(html).not.toContain('Inspect')
  })

  it('keeps the root visible when member reads fail, without claiming absence', () => {
    state.response = undefined
    state.error = new Error('Forbidden: Jobs are not readable')
    const html = overview()
    expect(html).toContain('JobSet overview')
    expect(html).toContain('Member Jobs unavailable')
    expect(html).toContain('Forbidden: Jobs are not readable')
    expect(html).not.toContain('No child Jobs currently retained')
    expect(overview(undefined, 'other.example/v1')).not.toContain('JobSet overview')
  })

  it('renders nested role, zero index and selected member in Overview and Logs', () => {
    const html = overview('jobs/training/distributed-workers-0')
    expect(html).toContain('Member Jobs')
    expect(html).toContain('workers #0')
    expect(html).toContain('JobSet restart attempt')
    expect(html).not.toContain('Run history')
    expect(state.useResource).toHaveBeenCalledWith('jobs', 'training', member.name, 'batch', expect.objectContaining({ enabled: true }))
    const logHTML = logs('jobs/training/distributed-workers-0')
    expect(logHTML).toContain('workers #0')
    expect(logHTML).toContain(`Logs for ${member.name}`)
    expect(logHTML).toContain(`data-log-target="jobs/training/${member.name}"`)
    expect(logHTML).not.toContain('data-log-target="jobsets/')
  })

  it('does not silently select a different member outside a truncated window', () => {
    state.response = { collection: 'members', runs: [member], total: 201, truncated: true }
    const missing = 'jobs/training/omitted-worker'
    expect(overview(missing)).toContain('The member list is truncated')
    const html = logs(missing)
    expect(html).toContain('Select a shown member Job')
    expect(html).not.toContain('Logs for')
  })

  it.each([{ remaining: [] }, { remaining: [{ ...member, name: 'other-worker' }] }])('preserves an explicit member through a recreation gap with $remaining', ({ remaining }) => {
    const selected = 'jobs/training/distributed-workers-0'
    state.response = { collection: 'members', runs: remaining, total: remaining.length, truncated: false }
    expect(overview(selected)).toContain('Selected Job is currently unavailable')
    const html = logs(selected)
    expect(html).toContain('Your selection is preserved if it reappears')
    expect(html).not.toContain('truncated window')
    expect(html).not.toContain('Logs for')
    if (remaining.length === 0) expect(html).not.toContain('<select')
    state.response = { collection: 'members', runs: [member], total: 1, truncated: false }
    expect(logs(selected)).toContain(`Logs for ${member.name}`)
    expect(overview(selected)).not.toContain('Selected Job is currently unavailable')
  })

  it('uses collection semantics even when there are no members', () => {
    state.response = { collection: 'members', runs: [], total: 0, truncated: false }
    expect(overview()).toContain('Member Jobs')
    expect(overview()).toContain('No child Jobs currently retained')
    expect(logs()).toContain('No child Jobs found')
  })

  it('reads response semantics instead of guessing membership from the parent kind', () => {
    state.response = { collection: 'runs', runs: [{ ...member, jobset: undefined }], total: 1, truncated: false }
    expect(overview()).toContain('Run history')
    expect(overview()).not.toContain('Member Jobs')
    expect(logs()).not.toContain('workers #0')
  })

  it('restarts logs for either native retry scope at the same Job address', () => {
    expect(workloadRunLogsKey({ ...member })).toBe(workloadRunLogsKey(member))
    expect(workloadRunLogsKey({ ...member, jobset: { ...member.jobset, restartAttempt: '1' } })).not.toBe(workloadRunLogsKey(member))
    expect(workloadRunLogsKey({ ...member, jobset: { ...member.jobset, jobRestartAttempt: '1' } })).not.toBe(workloadRunLogsKey(member))
    expect(workloadRunLogsKey({ ...member, jobset: {} })).not.toBe(workloadRunLogsKey(member))
  })
})
