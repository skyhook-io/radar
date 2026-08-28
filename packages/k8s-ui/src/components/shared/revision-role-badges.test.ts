import { describe, expect, it } from 'vitest'
import {
  completePromoteAfterRollback,
  offersPromoteAfterRollback,
  promoteFailureGuidance,
  revisionRoleBadges,
} from './ResourceActionsBar'
import type { WorkloadRevision } from '../../types/core'

describe('offersPromoteAfterRollback', () => {
  it('offers the option for both promotion styles — rollback alone lands neither', () => {
    expect(offersPromoteAfterRollback('rollouts', true)).toBe(true)
    expect(offersPromoteAfterRollback('Rollout', true)).toBe(true)
  })

  it('withholds it when the host reports promote-full is denied', () => {
    expect(offersPromoteAfterRollback('rollouts', false)).toBe(false)
  })

  it('never offers it for plain workloads, whose rollback lands immediately', () => {
    for (const kind of ['deployments', 'statefulsets', 'daemonsets']) {
      expect(offersPromoteAfterRollback(kind, true)).toBe(false)
    }
  })
})

function revision(overrides: Partial<WorkloadRevision>): WorkloadRevision {
  return { number: 1, image: 'web:v1', createdAt: '2026-08-07T00:00:00Z', ...overrides }
}

describe('revisionRoleBadges', () => {
  it('labels a Deployment revision Current in the healthy tone', () => {
    expect(revisionRoleBadges(revision({ isCurrent: true }), false)).toEqual([
      { label: 'Current', tone: 'status-healthy' },
    ])
  })

  it('gives a non-current revision no badge', () => {
    expect(revisionRoleBadges(revision({}), false)).toEqual([])
    expect(revisionRoleBadges(revision({}), true)).toEqual([])
  })

  it('splits Rolling out from Stable while a canary is mid-flight', () => {
    const rolling = revisionRoleBadges(revision({ number: 5, isCurrent: true, isStable: false }), true)
    expect(rolling).toEqual([{ label: 'Rolling out', tone: 'status-degraded' }])

    const stable = revisionRoleBadges(revision({ number: 4, isStable: true }), true)
    expect(stable).toEqual([
      { label: 'Stable', tone: 'status-healthy', tip: 'Serving stable traffic — an abort reverts here' },
    ])
  })

  it('collapses to a single Current badge once the canary stabilizes', () => {
    expect(revisionRoleBadges(revision({ isCurrent: true, isStable: true }), true)).toEqual([
      { label: 'Current', tone: 'status-healthy' },
    ])
  })

  // A Rollout paused at step 0 reports current and stable as the same revision.
  it('never emits both Stable and Rolling out for one revision', () => {
    for (const isStable of [true, false, undefined]) {
      const labels = revisionRoleBadges(revision({ isCurrent: true, isStable }), true).map((b) => b.label)
      expect(labels).not.toContain('Stable')
      expect(labels).toHaveLength(1)
    }
  })
})

describe('completePromoteAfterRollback', () => {
  it('does not resolve until promote-full settles', async () => {
    let release: () => void = () => {}
    const gate = new Promise<void>((resolve) => {
      release = resolve
    })
    const pending: boolean[] = []

    let done = false
    const run = completePromoteAfterRollback(
      () => gate,
      (p) => pending.push(p),
    ).then(() => {
      done = true
    })

    // Yield the microtask queue: if it did not await, done would already be true.
    await Promise.resolve()
    expect(done).toBe(false)
    expect(pending).toEqual([true])

    release()
    await run
    expect(done).toBe(true)
    expect(pending).toEqual([true, false])
  })

  // A rejected promote leaves the Rollout replaying the canary steps the operator asked
  // to skip, so the caller has to be able to tell that half of the pair did not land.
  it('reports a promote-full rejection and clears the pending flag', async () => {
    const pending: boolean[] = []
    await expect(
      completePromoteAfterRollback(
        () => Promise.reject(new Error('403 forbidden')),
        (p) => pending.push(p),
      ),
    ).resolves.toEqual({
      promoted: false,
      error: { message: '403 forbidden', controllerLagging: false },
    })
    expect(pending).toEqual([true, false])
  })

  // Keyed on the server's error code, not the status: a lost cluster connection answers
  // 503 on this route too, and offering "retry, it clears in a few seconds" for that
  // would invent a cause.
  it('marks only the controller-lag code as recoverable', async () => {
    const lagging = Object.assign(new Error('controller has not reconciled'), {
      status: 503,
      data: { error_code: 'controller_not_caught_up' },
    })
    await expect(
      completePromoteAfterRollback(() => Promise.reject(lagging), () => {}),
    ).resolves.toEqual({
      promoted: false,
      error: { message: 'controller has not reconciled', controllerLagging: true },
    })

    const disconnected = Object.assign(new Error('cluster client not available'), { status: 503 })
    await expect(
      completePromoteAfterRollback(() => Promise.reject(disconnected), () => {}),
    ).resolves.toEqual({
      promoted: false,
      error: { message: 'cluster client not available', controllerLagging: false },
    })
  })

  it('supports a void-returning callback', async () => {
    const pending: boolean[] = []
    let called = false
    await completePromoteAfterRollback(
      () => {
        called = true
      },
      (p) => pending.push(p),
    )
    expect(called).toBe(true)
    expect(pending).toEqual([true, false])
  })
})

describe('promoteFailureGuidance', () => {
  // The status alone cannot carry this: a lost cluster connection answers 503 on the
  // same route, and telling the operator to retry that would be a fabricated cause.
  it('offers a retry only when the controller was lagging', () => {
    const lagging = promoteFailureGuidance({ message: 'x', controllerLagging: true })
    expect(lagging.canRetry).toBe(true)
    expect(lagging.body).toContain('had not caught up')

    const other = promoteFailureGuidance({ message: 'forbidden', controllerLagging: false })
    expect(other.canRetry).toBe(false)
    expect(other.body).not.toContain('had not caught up')
  })

  // A canary pause is the usual reason to want promote-full, and it will not clear on
  // its own — "it is still running" would read as "wait" when the answer is "act".
  it('warns that a manual pause will not resolve itself', () => {
    for (const controllerLagging of [true, false]) {
      const { body } = promoteFailureGuidance({ message: 'x', controllerLagging })
      expect(body).toContain('manual pause')
      expect(body).not.toContain('progressing normally')
    }
  })
})
