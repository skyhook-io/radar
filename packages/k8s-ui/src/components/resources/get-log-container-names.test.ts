import { describe, it, expect } from 'vitest'
import { getDefaultLogContainerName, getLogContainerNames } from './resource-utils'

const initializingPod = (initStatuses: any[], annotation?: string, status: Record<string, unknown> = { phase: 'Pending' }) => ({
  metadata: annotation ? { annotations: { 'kubectl.kubernetes.io/default-container': annotation } } : {},
  spec: {
    initContainers: [{ name: 'migrate' }, { name: 'mesh', restartPolicy: 'Always' }, { name: 'seed' }],
    containers: [{ name: 'app' }],
  },
  status: { ...status, initContainerStatuses: initStatuses },
})
const initialized = (phase: string) => ({ phase, conditions: [{ type: 'Initialized', status: 'True' }] })

describe('getLogContainerNames', () => {
  it('lists init containers before regular ones', () => {
    expect(getLogContainerNames(initializingPod([]))).toEqual(['migrate', 'mesh', 'seed', 'app'])
  })

  it('returns an empty list for a pod without a spec', () => {
    expect(getLogContainerNames({})).toEqual([])
  })
})

describe('getDefaultLogContainerName', () => {
  it('picks the init container the pod is stuck on', () => {
    const pod = initializingPod([
      { name: 'migrate', state: { terminated: { exitCode: 0, reason: 'Completed' } } },
      { name: 'mesh', started: true, state: { running: {} } },
      { name: 'seed', state: { waiting: { reason: 'CrashLoopBackOff' } } },
    ])
    expect(getDefaultLogContainerName(pod)).toBe('seed')
  })

  it('picks a failed init container', () => {
    const pod = initializingPod([{ name: 'migrate', state: { terminated: { exitCode: 1, reason: 'Error' } } }])
    expect(getDefaultLogContainerName(pod)).toBe('migrate')
  })

  it('skips a running native sidecar', () => {
    const pod = initializingPod([
      { name: 'migrate', state: { terminated: { exitCode: 0 } } },
      { name: 'mesh', started: true, state: { running: {} } },
      { name: 'seed', state: { running: {} } },
    ])
    expect(getDefaultLogContainerName(pod)).toBe('seed')
  })

  it('picks a native sidecar that keeps failing to start', () => {
    const pod = initializingPod([
      { name: 'migrate', state: { terminated: { exitCode: 0 } } },
      { name: 'mesh', state: { waiting: { reason: 'CrashLoopBackOff' } } },
      { name: 'seed', state: { waiting: { reason: 'PodInitializing' } } },
    ])
    expect(getDefaultLogContainerName(pod)).toBe('mesh')
  })

  it('falls back to the default container once init has finished', () => {
    const pod = initializingPod([
      { name: 'migrate', state: { terminated: { exitCode: 0 } } },
      { name: 'mesh', started: true, state: { running: {} } },
      { name: 'seed', state: { terminated: { exitCode: 0 } } },
    ], 'app')
    expect(getDefaultLogContainerName(pod)).toBe('app')
  })

  it('picks a running native sidecar whose startup probe has not passed', () => {
    const pod = initializingPod([
      { name: 'migrate', state: { terminated: { exitCode: 0 } } },
      { name: 'mesh', started: false, state: { running: {} } },
      { name: 'seed', state: { waiting: { reason: 'PodInitializing' } } },
    ])
    expect(getDefaultLogContainerName(pod)).toBe('mesh')
  })

  it('ignores a native sidecar stopped with a non-zero exit when a finished pod shuts down', () => {
    const statuses = [
      { name: 'migrate', state: { terminated: { exitCode: 0 } } },
      { name: 'mesh', started: false, state: { terminated: { exitCode: 137, reason: 'Error' } } },
      { name: 'seed', state: { terminated: { exitCode: 0 } } },
    ]
    expect(getDefaultLogContainerName(initializingPod(statuses, undefined, initialized('Succeeded')))).toBe('app')
    expect(getDefaultLogContainerName(initializingPod(statuses, undefined, initialized('Failed')))).toBe('app')
  })

  it('keeps the default container when a native sidecar fails after initialization', () => {
    const pod = initializingPod([
      { name: 'migrate', state: { terminated: { exitCode: 0 } } },
      { name: 'mesh', started: false, state: { waiting: { reason: 'CrashLoopBackOff' } } },
      { name: 'seed', state: { terminated: { exitCode: 0 } } },
    ], 'app', initialized('Running'))
    expect(getDefaultLogContainerName(pod)).toBe('app')
  })

  it('picks a failed init container after kubelet stops the sidecar ahead of it', () => {
    const pod = initializingPod([
      { name: 'migrate', state: { terminated: { exitCode: 0 } } },
      { name: 'mesh', started: false, state: { terminated: { exitCode: 137, reason: 'Error' } } },
      { name: 'seed', state: { terminated: { exitCode: 1, reason: 'Error' } } },
    ], undefined, { phase: 'Failed', conditions: [{ type: 'Initialized', status: 'False' }] })
    expect(getDefaultLogContainerName(pod)).toBe('seed')
  })

  it('picks an init container that runs again in a recreated sandbox while Initialized stays True', () => {
    const pod = initializingPod([
      { name: 'migrate', state: { running: {} } },
      { name: 'mesh', state: { waiting: { reason: 'PodInitializing' } } },
      { name: 'seed', state: { waiting: { reason: 'PodInitializing' } } },
    ], 'app', initialized('Running'))
    expect(getDefaultLogContainerName(pod)).toBe('migrate')
  })

  // Table rows (?include=summary) drop `started`.
  it('reads a running sidecar in a table row as started', () => {
    const pod = initializingPod([
      { name: 'migrate', state: { terminated: { exitCode: 0 } } },
      { name: 'mesh', state: { running: {} } },
      { name: 'seed', state: { waiting: { reason: 'CrashLoopBackOff' } } },
    ])
    expect(getDefaultLogContainerName(pod)).toBe('seed')
  })

  it('falls back to the default container before any init status exists', () => {
    expect(getDefaultLogContainerName(initializingPod([]))).toBe('app')
  })
})
