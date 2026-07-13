import { describe, expect, it } from 'vitest'
import { buildArgoSyncOpts } from './SyncOptionsDialog'

describe('buildArgoSyncOpts', () => {
  it('keeps full application sync controls', () => {
    expect(
      buildArgoSyncOpts({
        resourceMode: false,
        revision: ' main ',
        prune: true,
        dryRun: true,
        force: false,
        applyOnly: true,
        replace: true,
        serverSideApply: false,
      }),
    ).toEqual({
      revision: 'main',
      prune: true,
      dryRun: true,
      force: false,
      applyOnly: true,
      syncOptions: ['Replace=true'],
    })
  })

  it('removes revision, prune, and apply-only semantics from resource sync', () => {
    expect(
      buildArgoSyncOpts({
        resourceMode: true,
        revision: 'unsafe-revision',
        prune: true,
        dryRun: false,
        force: true,
        applyOnly: true,
        replace: false,
        serverSideApply: true,
      }),
    ).toEqual({
      revision: undefined,
      prune: false,
      dryRun: false,
      force: true,
      applyOnly: false,
      syncOptions: ['ServerSideApply=true'],
    })
  })
})
