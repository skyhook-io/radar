import { describe, expect, it } from 'vitest'
import type { WorkloadPodContainerInfo, WorkloadPodInfo } from '../types/core'
import { initContainerNames, initializingPodsNotice } from './workload-log-sources'

const app = (state: WorkloadPodContainerInfo['state'], reason?: string): WorkloadPodContainerInfo => ({ name: 'kserve-container', ready: state === 'running', restartCount: 0, state, reason })
const init = (state: WorkloadPodContainerInfo['state'], reason?: string, extra: Partial<WorkloadPodContainerInfo> = {}): WorkloadPodContainerInfo => ({ name: 'storage-initializer', init: true, ready: false, restartCount: 3, state, reason, ...extra })
const pod = (name: string, statuses: WorkloadPodContainerInfo[]): WorkloadPodInfo => ({ name, containers: statuses.map(s => s.name), ready: false, containerStatuses: statuses })

describe('initializingPodsNotice', () => {
  it('names the app container and the init container holding it up', () => {
    expect(initializingPodsNotice([pod('p-1', [init('waiting', 'CrashLoopBackOff'), app('waiting', 'PodInitializing')])]))
      .toBe("kserve-container hasn't started: init container storage-initializer is in CrashLoopBackOff.")
    expect(initializingPodsNotice([pod('p-1', [init('running'), app('waiting', 'PodInitializing')])]))
      .toBe("kserve-container hasn't started: init container storage-initializer is still running.")
    expect(initializingPodsNotice([pod('p-1', [init('terminated', 'Error'), app('waiting', 'PodInitializing')])]))
      .toBe("kserve-container hasn't started: init container storage-initializer exited (Error).")
  })

  it('skips init containers that completed and sidecars that started', () => {
    const migrate = { ...init('terminated', 'Completed'), name: 'migrate' }
    const proxy = { ...init('running', undefined, { sidecar: true, started: true }), name: 'istio-proxy' }
    expect(initializingPodsNotice([pod('p-1', [migrate, proxy, init('waiting', 'CrashLoopBackOff'), app('waiting', 'PodInitializing')])]))
      .toBe("kserve-container hasn't started: init container storage-initializer is in CrashLoopBackOff.")
    const startingProxy = { ...proxy, started: false }
    expect(initializingPodsNotice([pod('p-1', [startingProxy, app('waiting', 'PodInitializing')])]))
      .toBe("kserve-container hasn't started: sidecar istio-proxy is still running.")
  })

  it('summarizes several Pods', () => {
    const stuck = (name: string) => pod(name, [init('waiting', 'CrashLoopBackOff'), app('waiting', 'PodInitializing')])
    expect(initializingPodsNotice([stuck('p-1'), stuck('p-2')]))
      .toBe("2 Pods: kserve-container hasn't started: init container storage-initializer is in CrashLoopBackOff.")
    expect(initializingPodsNotice([stuck('p-1'), pod('p-2', [init('running'), app('waiting', 'PodInitializing')])]))
      .toBe('2 Pods are still initializing, so their app containers have no output yet.')
  })

  it('stays quiet for running Pods, other selected containers and Radars without container states', () => {
    expect(initializingPodsNotice([pod('p-1', [init('terminated', 'Completed'), app('running')])])).toBe('')
    expect(initializingPodsNotice([pod('p-1', [init('waiting', 'CrashLoopBackOff'), app('waiting', 'PodInitializing')])], 'storage-initializer')).toBe('')
    expect(initializingPodsNotice([pod('p-1', [init('waiting', 'CrashLoopBackOff'), app('waiting', 'PodInitializing')])], 'kserve-container')).not.toBe('')
    expect(initializingPodsNotice([pod('p-1', [{ name: 'storage-initializer', init: true, ready: false, restartCount: 1 }, { name: 'app', ready: false, restartCount: 0 }])])).toBe('')
  })
})

describe('initContainerNames', () => {
  it('lists regular init containers, not sidecars', () => {
    const proxy = { ...init('running', undefined, { sidecar: true }), name: 'istio-proxy' }
    expect([...initContainerNames([pod('p-1', [init('waiting'), proxy, app('waiting')])])]).toEqual(['storage-initializer'])
  })
})
