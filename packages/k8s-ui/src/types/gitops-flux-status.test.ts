import { describe, it, expect } from 'vitest'
import { fluxConditionsToGitOpsStatus, type FluxCondition } from './gitops'

const c = (type: string, status: string, extra: Partial<FluxCondition> = {}): FluxCondition =>
  ({ type, status, ...extra } as FluxCondition)

// Mirrors pkg/gitops/fluxstatus_test.go. The two implementations must not drift:
// they feed the same columns from different sides of the wire.
describe('fluxConditionsToGitOpsStatus truth table', () => {
  it('a stale Reconciling does not outvote an applied Ready', () => {
    // The reported bug: kubectl and k9s read the printer columns and show
    // READY=True while Radar showed the row as syncing.
    const got = fluxConditionsToGitOpsStatus(
      [c('Ready', 'True'), c('Healthy', 'True'), c('Reconciling', 'True')],
      false,
      { generation: 1, observedGeneration: 1 },
    )
    expect(got.sync).toBe('Synced')
    expect(got.health).toBe('Healthy')
  })

  it('generation drift outranks a stale Ready', () => {
    const got = fluxConditionsToGitOpsStatus([c('Ready', 'True')], false, {
      generation: 2,
      observedGeneration: 1,
    })
    expect(got.sync).toBe('Reconciling')
    expect(got.health).toBe('Progressing')
  })

  it('treats the -1 sentinel as drift', () => {
    // A first reconcile reports Ready=Unknown; a reported failure outranks drift.
    const got = fluxConditionsToGitOpsStatus([c('Ready', 'Unknown'), c('Reconciling', 'True')], false, {
      generation: 1,
      observedGeneration: -1,
    })
    expect(got.sync).toBe('Reconciling')
  })

  it('a failure while retrying and never reconciled is degraded', () => {
    // Live shape of a GitRepository whose repository doesn't exist.
    const got = fluxConditionsToGitOpsStatus(
      [c('Reconciling', 'True', { reason: 'ProgressingWithRetry' }), c('Ready', 'False', { reason: 'GitOperationFailed', observedGeneration: 1 })],
      false,
      { generation: 1, observedGeneration: -1 },
    )
    expect(got.sync).toBe('OutOfSync')
    expect(got.health).toBe('Degraded')
  })

  it('a failure about an older generation does not outrank a new spec', () => {
    const got = fluxConditionsToGitOpsStatus([c('Ready', 'False', { reason: 'BuildFailed', observedGeneration: 2 })], false, {
      generation: 3,
      observedGeneration: 1,
    })
    expect(got.sync).toBe('Reconciling')
    expect(got.health).toBe('Progressing')
  })

  it('a retry while reconciling stays progressing', () => {
    const got = fluxConditionsToGitOpsStatus(
      [c('Reconciling', 'True', { reason: 'Progressing' }), c('Ready', 'False', { reason: 'ProgressingWithRetry' })],
      false,
      { generation: 2, observedGeneration: 1 },
    )
    expect(got.sync).toBe('Reconciling')
    expect(got.health).toBe('Progressing')
  })

  it('does not treat an absent observedGeneration as drift', () => {
    // Kinds and controller versions that never set it must not all read stale.
    expect(fluxConditionsToGitOpsStatus([c('Ready', 'True')], false, { generation: 3 }).sync).toBe('Synced')
    expect(fluxConditionsToGitOpsStatus([c('Ready', 'True')], false, {}).sync).toBe('Synced')
    expect(fluxConditionsToGitOpsStatus([c('Ready', 'True')], false).sync).toBe('Synced')
  })

  it('lets Stalled outrank Ready', () => {
    const got = fluxConditionsToGitOpsStatus([c('Ready', 'True'), c('Stalled', 'True')], false, {
      generation: 1,
      observedGeneration: 1,
    })
    expect(got.sync).toBe('OutOfSync')
    expect(got.health).toBe('Degraded')
  })

  it('separates applied from healthy', () => {
    const got = fluxConditionsToGitOpsStatus([c('Ready', 'True'), c('Healthy', 'False')], false, {
      generation: 1,
      observedGeneration: 1,
    })
    expect(got.sync).toBe('Synced')
    expect(got.health).toBe('Degraded')
  })

  it('reports Reconciling when the pass has not reached Ready', () => {
    const got = fluxConditionsToGitOpsStatus([c('Ready', 'Unknown'), c('Reconciling', 'True')], false, {
      generation: 1,
      observedGeneration: 1,
    })
    expect(got.sync).toBe('Reconciling')
  })

  it('keeps a suspended-but-applied object Synced, and drops it on drift', () => {
    const gen = { generation: 1, observedGeneration: 1 }
    expect(fluxConditionsToGitOpsStatus([c('Ready', 'True')], true, gen).sync).toBe('Synced')
    expect(
      fluxConditionsToGitOpsStatus([c('Ready', 'True')], true, { generation: 2, observedGeneration: 1 }).sync,
    ).toBe('Unknown')
  })

  it('treats a retrying Ready=False as progressing, not degraded', () => {
    // Mirrors pkg/gitops/fluxstatus_test.go. Flux composes reasons like
    // ProgressingWithRetry; a retry in flight is out of sync but not settled.
    const got = fluxConditionsToGitOpsStatus(
      [c('Ready', 'False', { reason: 'ProgressingWithRetry' })],
      false,
      { generation: 1, observedGeneration: 1 },
    )
    expect(got.sync).toBe('OutOfSync')
    expect(got.health).toBe('Progressing')
  })

  it('treats a settled Ready=False as degraded', () => {
    const got = fluxConditionsToGitOpsStatus([c('Ready', 'False', { reason: 'BuildFailed' })], false, {
      generation: 1,
      observedGeneration: 1,
    })
    expect(got.sync).toBe('OutOfSync')
    expect(got.health).toBe('Degraded')
  })
})


describe('reconciling activity', () => {
  it('survives even when it no longer drives the status', () => {
    const got = fluxConditionsToGitOpsStatus(
      [c('Ready', 'True'), c('Reconciling', 'True', { lastTransitionTime: '2026-08-11T06:05:00Z' })],
      false,
      { generation: 1, observedGeneration: 1 },
    )
    expect(got.sync).toBe('Synced')
    expect(got.reconciling).toBe(true)
    expect(got.reconcilingSince).toBe('2026-08-11T06:05:00Z')
  })

  it('is absent on a quiet object', () => {
    const got = fluxConditionsToGitOpsStatus([c('Ready', 'True')], false, {
      generation: 1,
      observedGeneration: 1,
    })
    expect(got.reconciling).toBeUndefined()
  })
})

// Source kinds carry no Healthy condition; HelmRelease uses Released.
describe('non-Kustomization Flux kinds', () => {
  it.each([
    ['GitRepository', [c('Ready', 'True'), c('ArtifactInStorage', 'True')]],
    ['HelmRepository', [c('Ready', 'True'), c('ArtifactInStorage', 'True')]],
    ['HelmRelease', [c('Ready', 'True'), c('Released', 'True')]],
  ])('%s reads as Synced/Healthy', (_kind, conds) => {
    const got = fluxConditionsToGitOpsStatus(conds as FluxCondition[], false, {
      generation: 1,
      observedGeneration: 1,
    })
    expect(got.sync).toBe('Synced')
    expect(got.health).toBe('Healthy')
  })
})

// The wedged-controller case end to end: a fully-applied object whose reconcile
// pass never finished must read Synced (it IS applied) AND still carry the
// in-flight fact, or a stuck controller becomes invisible. Before the sync fix
// this state showed a permanent "Syncing", which was the wrong word but the
// only thing in the product that noticed.
describe('a wedged controller stays visible', () => {
  it('reports Synced and keeps the in-flight fact with its start time', () => {
    const got = fluxConditionsToGitOpsStatus(
      [
        c('Ready', 'True', { lastTransitionTime: '2026-08-11T06:00:00Z' }),
        c('Healthy', 'True'),
        c('Reconciling', 'True', { lastTransitionTime: '2026-08-11T06:05:00Z' }),
      ],
      false,
      { generation: 1, observedGeneration: 1 },
    )
    expect(got.sync).toBe('Synced')
    expect(got.health).toBe('Healthy')
    // Both are required: without these the indicator has nothing to render and
    // the operator gets a confident all-clear over a stuck controller.
    expect(got.reconciling).toBe(true)
    expect(got.reconcilingSince).toBe('2026-08-11T06:05:00Z')
  })
})
