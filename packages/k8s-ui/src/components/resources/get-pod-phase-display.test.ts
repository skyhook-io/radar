import { describe, it, expect } from 'vitest'
import { getPodPhaseDisplay } from './resource-utils'

const podRunningHealthy = {
  status: {
    phase: 'Running',
    containerStatuses: [{ name: 'app', ready: true, restartCount: 0 }],
  },
}

const podRunningNotReady = {
  status: {
    phase: 'Running',
    containerStatuses: [{ name: 'app', ready: false, restartCount: 0 }],
  },
}

const podRunningCycling = {
  status: {
    phase: 'Running',
    containerStatuses: [{ name: 'app', ready: true, restartCount: 6301 }],
  },
}

const podRunningNotReadyAndCycling = {
  status: {
    phase: 'Running',
    containerStatuses: [{ name: 'app', ready: false, restartCount: 6301 }],
  },
}

const podCrashLooping = {
  status: {
    phase: 'Running',
    containerStatuses: [
      {
        name: 'app',
        ready: false,
        restartCount: 12,
        state: { waiting: { reason: 'CrashLoopBackOff' } },
      },
    ],
  },
}

const podImagePullBackOff = {
  status: {
    phase: 'Pending',
    containerStatuses: [
      {
        name: 'app',
        ready: false,
        restartCount: 0,
        state: { waiting: { reason: 'ImagePullBackOff' } },
      },
    ],
  },
}

const podOOMKilled = {
  status: {
    phase: 'Running',
    containerStatuses: [
      {
        name: 'app',
        ready: false,
        restartCount: 3,
        state: { terminated: { reason: 'OOMKilled', exitCode: 137 } },
      },
    ],
  },
}

const podTerminating = {
  metadata: { deletionTimestamp: '2026-04-29T10:00:00Z' },
  status: {
    phase: 'Running',
    containerStatuses: [{ name: 'app', ready: true, restartCount: 0 }],
  },
}

const podErrImagePull = {
  status: {
    phase: 'Pending',
    containerStatuses: [
      {
        name: 'app',
        ready: false,
        restartCount: 0,
        state: { waiting: { reason: 'ErrImagePull' } },
      },
    ],
  },
}

const podCreateContainerConfigError = {
  status: {
    phase: 'Pending',
    containerStatuses: [
      {
        name: 'app',
        ready: false,
        restartCount: 0,
        state: { waiting: { reason: 'CreateContainerConfigError' } },
      },
    ],
  },
}

const podMultiContainerPartiallyReady = {
  status: {
    phase: 'Running',
    containerStatuses: [
      { name: 'app', ready: true, restartCount: 0 },
      { name: 'sidecar', ready: false, restartCount: 0 },
      { name: 'proxy', ready: true, restartCount: 0 },
    ],
  },
}

const podMultiContainerCyclingAcrossContainers = {
  status: {
    phase: 'Running',
    containerStatuses: [
      { name: 'app', ready: true, restartCount: 3 },
      { name: 'sidecar', ready: true, restartCount: 4 },
    ],
  },
}

const podSucceededWithOOMKilledSidecar = {
  status: {
    phase: 'Succeeded',
    containerStatuses: [
      { name: 'main', ready: false, restartCount: 0, state: { terminated: { reason: 'Completed', exitCode: 0 } } },
      { name: 'sidecar', ready: false, restartCount: 1, state: { terminated: { reason: 'OOMKilled', exitCode: 137 } } },
    ],
  },
}

describe('getPodPhaseDisplay', () => {
  it('returns Running, healthy for a fully-ready pod with no restarts', () => {
    const r = getPodPhaseDisplay(podRunningHealthy)
    expect(r.phase).toBe('Running')
    expect(r.text).toBe('Running')
    expect(r.level).toBe('healthy')
    expect(r.hint).toBeUndefined()
  })

  it('downgrades a Running pod that is not ready (the "0/1" case)', () => {
    const r = getPodPhaseDisplay(podRunningNotReady)
    expect(r.phase).toBe('Running')
    expect(r.text).toBe('Running — Not Ready (0/1)')
    expect(r.level).toBe('degraded')
    expect(r.hint).toMatch(/not ready/i)
  })

  it('downgrades a Running pod with high restarts even if currently ready', () => {
    const r = getPodPhaseDisplay(podRunningCycling)
    expect(r.text).toBe('Running — Restarting (6301 restarts)')
    expect(r.level).toBe('degraded')
  })

  it('marks a Running + Not Ready + cycling pod as unhealthy', () => {
    const r = getPodPhaseDisplay(podRunningNotReadyAndCycling)
    expect(r.text).toContain('Not Ready (0/1)')
    expect(r.text).toContain('6301 restarts')
    expect(r.level).toBe('unhealthy')
  })

  it('always preserves the raw phase field so kubectl users can correlate', () => {
    expect(getPodPhaseDisplay(podRunningNotReady).phase).toBe('Running')
    expect(getPodPhaseDisplay(podCrashLooping).phase).toBe('Running')
    expect(getPodPhaseDisplay(podImagePullBackOff).phase).toBe('Pending')
  })

  it('flags CrashLoopBackOff as unhealthy with phase preserved', () => {
    const r = getPodPhaseDisplay(podCrashLooping)
    expect(r.text).toBe('Running — CrashLoopBackOff')
    expect(r.level).toBe('unhealthy')
  })

  it('flags ImagePullBackOff as unhealthy on a Pending pod', () => {
    const r = getPodPhaseDisplay(podImagePullBackOff)
    expect(r.text).toBe('Pending — ImagePullBackOff')
    expect(r.level).toBe('unhealthy')
  })

  it('flags OOMKilled even when phase is Running', () => {
    const r = getPodPhaseDisplay(podOOMKilled)
    expect(r.text).toBe('Running — OOMKilled')
    expect(r.level).toBe('unhealthy')
  })

  it('marks pods with deletionTimestamp as Terminating regardless of phase', () => {
    const r = getPodPhaseDisplay(podTerminating)
    expect(r.text).toBe('Running — Terminating')
    expect(r.level).toBe('degraded')
  })

  it('falls back to Unknown for missing/unknown phase', () => {
    expect(getPodPhaseDisplay({}).level).toBe('unknown')
    expect(getPodPhaseDisplay({}).phase).toBe('Unknown')
    expect(getPodPhaseDisplay({ status: { phase: 'Banana' } }).level).toBe('unknown')
  })

  it('handles Succeeded/Pending/Failed phases', () => {
    expect(getPodPhaseDisplay({ status: { phase: 'Succeeded' } }).level).toBe('neutral')
    expect(getPodPhaseDisplay({ status: { phase: 'Pending' } }).level).toBe('degraded')
    expect(getPodPhaseDisplay({ status: { phase: 'Failed' } }).level).toBe('unhealthy')
  })

  it('flags ErrImagePull as unhealthy', () => {
    const r = getPodPhaseDisplay(podErrImagePull)
    expect(r.text).toBe('Pending — ErrImagePull')
    expect(r.level).toBe('unhealthy')
  })

  it('flags CreateContainerConfigError as unhealthy', () => {
    const r = getPodPhaseDisplay(podCreateContainerConfigError)
    expect(r.text).toBe('Pending — CreateContainerConfigError')
    expect(r.level).toBe('unhealthy')
  })

  it('reports the correct readiness ratio for multi-container pods', () => {
    const r = getPodPhaseDisplay(podMultiContainerPartiallyReady)
    expect(r.text).toBe('Running — Not Ready (2/3)')
    expect(r.level).toBe('degraded')
  })

  it('sums restartCount across containers when deciding cycling', () => {
    const r = getPodPhaseDisplay(podMultiContainerCyclingAcrossContainers)
    expect(r.text).toBe('Running — Restarting (7 restarts)')
    expect(r.level).toBe('degraded')
  })

  it('does not flag a Succeeded Job pod as unhealthy when a sidecar OOMed', () => {
    const r = getPodPhaseDisplay(podSucceededWithOOMKilledSidecar)
    expect(r.phase).toBe('Succeeded')
    expect(r.text).toBe('Completed')
    expect(r.level).toBe('neutral')
  })

  it('does not mistake exactly-RESTART_CYCLING_THRESHOLD restarts for cycling (boundary)', () => {
    const r = getPodPhaseDisplay({
      status: {
        phase: 'Running',
        containerStatuses: [{ name: 'app', ready: true, restartCount: 5 }],
      },
    })
    expect(r.text).toBe('Running')
    expect(r.level).toBe('healthy')
  })
})
