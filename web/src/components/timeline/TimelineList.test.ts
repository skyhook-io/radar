import { describe, expect, it } from 'vitest'
import { applyClientFilters } from '../../api/timelineSource'
import type { TimelineEvent } from '../../types'
import { splitRoutineActivity } from './TimelineList'

let clock = Date.parse('2024-01-01T00:00:00.000Z')
function ev(over: Partial<TimelineEvent> & { id: string }): TimelineEvent {
  clock += 1000
  return {
    timestamp: new Date(clock).toISOString(),
    source: 'informer',
    kind: 'Deployment',
    namespace: 'ns-a',
    name: over.id,
    eventType: 'update',
    ...over,
  }
}

const deploy = ev({ id: 'deploy', kind: 'Deployment' })
const podStarted = ev({ id: 'pod-started', kind: 'Pod', source: 'k8s_event', eventType: 'Normal', reason: 'Started' })
const podOom = ev({ id: 'pod-oom', kind: 'Pod', source: 'k8s_event', eventType: 'Warning', reason: 'OOMKilled' })
const rsScaled = ev({ id: 'rs-scaled', kind: 'ReplicaSet' })
const cronJobOk = ev({ id: 'cron-job-ok', kind: 'Job', owner: { kind: 'CronJob', name: 'nightly' }, healthState: 'healthy' })
const cronJobFailed = ev({ id: 'cron-job-failed', kind: 'Job', owner: { kind: 'CronJob', name: 'nightly' }, healthState: 'unhealthy' })
const standaloneJob = ev({ id: 'standalone-job', kind: 'Job' })
const ALL = [deploy, podStarted, podOom, rsScaled, cronJobOk, cronJobFailed, standaloneJob]

// Mirrors the host: one uncapped query through the source's client filters.
function split(events: TimelineEvent[], kinds: string[], limit = 2000) {
  return splitRoutineActivity(applyClientFilters(events, { kinds, includeManaged: true }), limit)
}
const ids = (rows: TimelineEvent[]) => rows.map((e) => e.id).sort()
const oom = (id: string) => ev({ id, kind: 'Pod', source: 'k8s_event', eventType: 'Warning', reason: 'OOMKilled' })
const started = (id: string) => ev({ id, kind: 'Pod', source: 'k8s_event', eventType: 'Normal', reason: 'Started' })

describe('splitRoutineActivity', () => {
  it('keeps owner rows and child problems, holds routine child rows apart', () => {
    const { withoutRoutine, routine } = split(ALL, [])
    expect(ids(withoutRoutine)).toEqual(['cron-job-failed', 'deploy', 'pod-oom', 'standalone-job'])
    expect(ids(routine)).toEqual(['cron-job-ok', 'pod-started', 'rs-scaled'])
  })

  it('shows a picked kind\'s problems and holds its routine rows apart', () => {
    const pods = split(ALL, ['Pod'])
    expect(ids(pods.withoutRoutine)).toEqual(['pod-oom'])
    expect(ids(pods.routine)).toEqual(['pod-started'])

    const jobs = split(ALL, ['Job'])
    expect(ids(jobs.withoutRoutine)).toEqual(['cron-job-failed', 'standalone-job'])
    expect(ids(jobs.routine)).toEqual(['cron-job-ok'])
  })

  it('lists rows newest first', () => {
    expect(split(ALL, []).withoutRoutine.map((e) => e.id)).toEqual(['standalone-job', 'cron-job-failed', 'pod-oom', 'deploy'])
  })

  it('never lets owner rows crowd out child problems', () => {
    const owners = [1, 2, 3].map((n) => ev({ id: `deploy-${n}`, kind: 'Deployment' }))
    const { withoutRoutine, truncated } = split([oom('old-oom'), ...owners], [], 2)
    expect(ids(withoutRoutine)).toEqual(['deploy-2', 'deploy-3', 'old-oom'])
    expect(truncated).toBe(true)
  })

  it('only adds rows when routine activity is shown', () => {
    const olderDeploy = ev({ id: 'older-deploy', kind: 'Deployment' })
    const burst = [1, 2, 3].map((n) => started(`started-${n}`))
    const { withoutRoutine, withRoutine, routine, routineTruncated } = split([olderDeploy, ...burst], [], 2)
    expect(ids(withoutRoutine)).toEqual(['older-deploy'])
    expect(ids(withRoutine)).toEqual(['older-deploy', 'started-2', 'started-3'])
    expect(ids(routine)).toEqual(['started-2', 'started-3'])
    expect(routineTruncated).toBe(true)
  })
})
