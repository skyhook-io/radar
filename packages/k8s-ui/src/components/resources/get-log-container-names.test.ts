import { describe, it, expect } from 'vitest'
import { getDefaultLogContainerName, getLogContainerNames } from './resource-utils'

const initializingPod = (initStatuses: any[], annotation?: string) => ({
  metadata: annotation ? { annotations: { 'kubectl.kubernetes.io/default-container': annotation } } : {},
  spec: {
    initContainers: [{ name: 'migrate' }, { name: 'mesh', restartPolicy: 'Always' }, { name: 'seed' }],
    containers: [{ name: 'app' }],
  },
  status: { phase: 'Pending', initContainerStatuses: initStatuses },
})

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
      { name: 'mesh', state: { running: {} } },
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
      { name: 'mesh', state: { running: {} } },
      { name: 'seed', state: { running: {} } },
    ])
    expect(getDefaultLogContainerName(pod)).toBe('seed')
  })

  it('falls back to the default container once init has finished', () => {
    const pod = initializingPod([
      { name: 'migrate', state: { terminated: { exitCode: 0 } } },
      { name: 'mesh', state: { running: {} } },
      { name: 'seed', state: { terminated: { exitCode: 0 } } },
    ], 'app')
    expect(getDefaultLogContainerName(pod)).toBe('app')
  })

  it('falls back to the default container before any init status exists', () => {
    expect(getDefaultLogContainerName(initializingPod([]))).toBe('app')
  })
})
