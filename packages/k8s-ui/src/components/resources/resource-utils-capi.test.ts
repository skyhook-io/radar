import { describe, it, expect } from 'vitest'
import { getClusterCPReplicas, getClusterWorkerReplicas } from './resource-utils-capi'

// Cluster replica counts live in three places depending on which contract the
// object was read through: status.workers (v1beta2), status.v1beta2.workers
// (the bridge a served v1beta1 carries while both contracts coexist), and
// nowhere at all on a plain v1beta1 Cluster. `status.workersReady` - which the
// worker accessor used to fall back to - has never existed in any release.

describe('CAPI Cluster replica counts', () => {
  it('reads the v1beta2 contract', () => {
    const c = {
      status: {
        controlPlane: { readyReplicas: 3, desiredReplicas: 3 },
        workers: { readyReplicas: 4, desiredReplicas: 5 },
      },
    }
    expect(getClusterCPReplicas(c)).toBe('3/3')
    expect(getClusterWorkerReplicas(c)).toBe('4/5')
  })

  it('reaches the counts through the v1beta1 bridge', () => {
    const c = {
      status: {
        controlPlaneReady: true,
        v1beta2: {
          controlPlane: { readyReplicas: 1, desiredReplicas: 3 },
          workers: { readyReplicas: 0, desiredReplicas: 2 },
        },
      },
    }
    expect(getClusterCPReplicas(c)).toBe('1/3')
    expect(getClusterWorkerReplicas(c)).toBe('0/2')
  })

  it('falls back to the v1beta1 control-plane boolean when no counts exist', () => {
    expect(getClusterCPReplicas({ status: { controlPlaneReady: true } })).toBe('Ready')
    expect(getClusterCPReplicas({ status: { controlPlaneReady: false } })).toBe('NotReady')
  })

  it('reports absence for workers on a plain v1beta1 Cluster', () => {
    // v1beta1 carries no worker counts on the Cluster; they live on the
    // MachineDeployments. Rendering a fabricated 0/0 would read as "no workers".
    expect(getClusterWorkerReplicas({ status: { controlPlaneReady: true } })).toBe('-')
    expect(getClusterWorkerReplicas({ status: {} })).toBe('-')
    expect(getClusterWorkerReplicas({})).toBe('-')
  })

  it('shows a desired count even before any replica is ready', () => {
    const c = { status: { workers: { desiredReplicas: 3 } } }
    expect(getClusterWorkerReplicas(c)).toBe('0/3')
  })
})
