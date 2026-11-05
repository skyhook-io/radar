import { describe, expect, it } from 'vitest'
import { appVerdictText, buildAppVerdict, classifyAppCause } from './app-verdict'
import type { AppEvent, AppRow, AppWorkload } from './applications'

function workload(overrides: Partial<AppWorkload> = {}): AppWorkload {
  return { kind: 'Deployment', namespace: 'prod', name: 'api', health: 'healthy', ready: 3, desired: 3, restarts: 0, ...overrides }
}

function app(overrides: Partial<AppRow> = {}): AppRow {
  return { key: 'checkout', name: 'checkout', health: 'healthy', runtimeHealth: 'healthy', workloads: [workload()], ...overrides }
}

function event(reason: string, overrides: Partial<AppEvent> = {}): AppEvent {
  return { type: 'Warning', reason, count: 1, object: 'Pod/api-1', ...overrides }
}

const failing = workload({ health: 'unhealthy', ready: 0, reason: 'CrashLoopBackOff' })

describe('classifyAppCause', () => {
  it('is null for a healthy app, whatever old reasons it carries', () => {
    expect(classifyAppCause(app({ workloads: [workload({ reason: 'OOMKilled' })], events: [event('FailedScheduling')] }))).toBeNull()
  })

  it('reads crashes as the app', () => {
    expect(classifyAppCause(app({ workloads: [failing], events: [event('BackOff')] }))).toBe('app')
  })

  it('reads an out-of-memory kill as the app', () => {
    expect(classifyAppCause(app({ workloads: [workload({ health: 'degraded', reason: 'OOMKilled' })] }))).toBe('app')
  })

  it('reads a missing secret as the app', () => {
    expect(classifyAppCause(app({ workloads: [workload({ health: 'unhealthy', reason: 'CreateContainerConfigError' })] }))).toBe('app')
  })

  it('reads scheduling failures as the platform', () => {
    expect(classifyAppCause(app({ workloads: [workload({ health: 'unhealthy', ready: 0 })], events: [event('NodeNotReady')] }))).toBe('platform')
  })

  it('reads evictions as the platform, in any case', () => {
    expect(classifyAppCause(app({ workloads: [workload({ health: 'degraded', reason: 'Evicted' })] }))).toBe('platform')
    expect(classifyAppCause(app({ workloads: [workload({ health: 'degraded', reason: 'evicted' })] }))).toBe('platform')
  })

  it('stays open when the two sides disagree', () => {
    expect(classifyAppCause(app({ workloads: [failing], events: [event('FailedScheduling')] }))).toBeNull()
  })

  it('stays open on an image pull, which can be a bad tag or a registry outage', () => {
    expect(classifyAppCause(app({ workloads: [workload({ health: 'unhealthy', reason: 'ImagePullBackOff' })] }))).toBeNull()
  })

  it('stays open on a failed mount, which can be a missing secret or broken storage', () => {
    expect(classifyAppCause(app({ workloads: [workload({ health: 'unhealthy' })], events: [event('FailedMount')] }))).toBeNull()
  })

  it('stays open on a reason it does not know, even next to a clear one', () => {
    expect(classifyAppCause(app({ workloads: [failing], events: [event('SomeOperatorWarning')] }))).toBeNull()
  })

  it('stays open with only signals that follow from either side', () => {
    expect(classifyAppCause(app({ workloads: [workload({ health: 'unhealthy' })], events: [event('BackOff'), event('Unhealthy')] }))).toBeNull()
  })

  it('stays open with no reasons at all', () => {
    expect(classifyAppCause(app({ workloads: [workload({ health: 'unhealthy' })] }))).toBeNull()
  })
})

describe('buildAppVerdict', () => {
  describe('one instance', () => {
    it('names the environment and readiness when healthy', () => {
      const verdict = buildAppVerdict({ app: app({ identity: { key: 'checkout', env: 'prod', confidence: 'high', evidence: '' } }) })

      expect(verdict).toMatchObject({ health: 'healthy', summary: 'Healthy in prod, 3/3 ready' })
    })

    it('does not say healthy when a workload has no reading', () => {
      const verdict = buildAppVerdict({ app: app({ workloads: [workload(), workload({ name: 'worker', health: 'unknown' })] }) })

      expect(verdict.health).toBe('unknown')
      expect(verdict.summary).toBe('Unknown: no reading for 1 of 2 workloads')
    })

    it('does not say healthy with no workloads', () => {
      const verdict = buildAppVerdict({ app: app({ workloads: [] }) })

      expect(verdict.health).toBe('unknown')
      expect(verdict.summary).toContain('no workloads reported')
    })

    it('keeps the failure when another workload has no reading', () => {
      const verdict = buildAppVerdict({ app: app({ runtimeHealth: 'unhealthy', workloads: [failing, workload({ name: 'worker', health: 'unknown' })] }) })

      expect(verdict.health).toBe('unhealthy')
    })

    it('names the one failing workload, since when, and the likely cause', () => {
      const verdict = buildAppVerdict({
        app: app({
          runtimeHealth: 'unhealthy',
          workloads: [failing],
          events: [event('BackOff', { firstSeen: '2026-11-05T10:00:00Z' }), event('BackOff', { firstSeen: '2026-11-05T09:00:00Z' })],
        }),
      })

      expect(verdict).toMatchObject({
        health: 'unhealthy',
        summary: 'Unhealthy: api 0/3 ready',
        since: '2026-11-05T09:00:00Z',
        cause: 'app',
      })
    })

    it('counts several failing workloads', () => {
      const verdict = buildAppVerdict({
        app: app({ runtimeHealth: 'degraded', workloads: [workload({ health: 'degraded' }), workload({ name: 'b', health: 'degraded' }), workload({ name: 'c' })] }),
      })

      expect(verdict.summary).toBe('Degraded: 2 of 3 workloads')
    })

    it('names the rollout on a healthy app, without a problem time', () => {
      const verdict = buildAppVerdict({
        app: app({
          events: [event('BackOff', { firstSeen: '2026-11-05T10:00:00Z' })],
          workloads: [workload({ rollout: { phase: 'progressing', active: true, manual: false, label: 'Rolling out', desired: 3, updated: 1, ready: 3, available: 3 } })],
        }),
      })

      expect(verdict.rollout).toBe('api: Rolling out')
      expect(verdict.since).toBeUndefined()
    })
  })

  describe('several instances', () => {
    const instances = [
      { env: 'dev', health: 'healthy' as const },
      { env: 'staging', health: 'healthy' as const },
      { env: 'prod', health: 'degraded' as const },
    ]

    it('says where each state applies, worst first', () => {
      const verdict = buildAppVerdict({ app: app(), instances, activeEnv: 'dev' })

      expect(verdict.health).toBe('degraded')
      expect(verdict.summary).toBe('Degraded in prod · healthy in dev, staging')
    })

    it('does not fold an unread instance into healthy', () => {
      const verdict = buildAppVerdict({
        app: app(),
        instances: [{ env: 'dev', health: 'healthy' }, { env: 'prod', health: 'unknown' }],
      })

      expect(verdict.health).toBe('unknown')
      expect(verdict.summary).toBe('Unknown in prod · healthy in dev')
    })

    it('takes the worst of one env spread over clusters', () => {
      const verdict = buildAppVerdict({
        app: app(),
        instances: [{ env: 'prod', health: 'healthy' }, { env: 'prod', health: 'unhealthy' }],
      })

      expect(verdict.summary).toBe('Unhealthy in prod')
    })

    it('shortens a long list of environments', () => {
      const verdict = buildAppVerdict({
        app: app(),
        instances: ['a', 'b', 'c', 'd', 'e'].map((env) => ({ env, health: 'healthy' as const })),
      })

      expect(verdict.summary).toBe('Healthy in a, b, c +2')
    })

    it('scopes details to the instance on screen', () => {
      const verdict = buildAppVerdict({
        app: app({ runtimeHealth: 'unhealthy', workloads: [failing], events: [event('BackOff')] }),
        instances,
        activeEnv: 'prod',
      })

      expect(appVerdictText(verdict)).toBe('Degraded in prod · healthy in dev, staging · prod: likely the app')
    })

    it('ignores a single instance list', () => {
      const verdict = buildAppVerdict({ app: app(), instances: [{ env: 'prod', health: 'healthy' }] })

      expect(verdict.summary).toBe('Healthy, 3/3 ready')
    })
  })
})

describe('appVerdictText', () => {
  it('leaves the cause off when it is open', () => {
    expect(appVerdictText({ health: 'degraded', summary: 'Degraded', cause: null })).toBe('Degraded')
  })

  it('joins what is known', () => {
    expect(appVerdictText({ health: 'degraded', summary: 'Degraded', rollout: 'api: Rolling out', cause: 'platform' })).toBe(
      'Degraded · api: Rolling out · likely the platform',
    )
  })
})
