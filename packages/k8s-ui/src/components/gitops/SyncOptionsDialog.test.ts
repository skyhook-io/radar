import { describe, expect, it } from 'vitest'
import { argoSyncOptionsForRequest, buildArgoSyncOpts } from './SyncOptionsDialog'

const base = { revision: '', prune: false, dryRun: false, force: false, applyOnly: false, replace: false, serverSideApply: false }

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

  it('sends no options when the toggles match the Application, so its own apply', () => {
    const opts = buildArgoSyncOpts({ ...base, resourceMode: false, serverSideApply: true, applicationSyncOptions: ['CreateNamespace=true', 'ServerSideApply=true'] })
    expect(opts).not.toHaveProperty('syncOptions')
    expect(buildArgoSyncOpts({ ...base, resourceMode: false })).not.toHaveProperty('syncOptions')
  })

  it("applies a toggle as an edit on top of the Application's options", () => {
    const applicationSyncOptions = ['CreateNamespace=true', 'ServerSideApply=true', 'PruneLast=true']
    expect(buildArgoSyncOpts({ ...base, resourceMode: false, replace: true, serverSideApply: true, applicationSyncOptions }).syncOptions)
      .toEqual(['CreateNamespace=true', 'PruneLast=true', 'Replace=true', 'ServerSideApply=true'])
    expect(buildArgoSyncOpts({ ...base, resourceMode: false, applicationSyncOptions }).syncOptions)
      .toEqual(['CreateNamespace=true', 'PruneLast=true'])
  })

  it("turning on an option the Application sets to false replaces that entry", () => {
    expect(buildArgoSyncOpts({ ...base, resourceMode: true, serverSideApply: true, applicationSyncOptions: ['ServerSideApply=false', 'Validate=false'] }).syncOptions)
      .toEqual(['Validate=false', 'ServerSideApply=true'])
  })

  it("leaves out the Application's other options only when asked", () => {
    expect(buildArgoSyncOpts({ ...base, resourceMode: false, applicationSyncOptions: ['CreateNamespace=true'], keepApplicationSyncOptions: false }).syncOptions)
      .toEqual([])
  })
})

describe('argoSyncOptionsForRequest', () => {
  const app = ['CreateNamespace=true']
  it('omits the options for a Radar that inherits them', () => {
    expect(argoSyncOptionsForRequest(undefined, app, true)).toBeUndefined()
  })
  it("sends the Application's options to an older Radar, which would drop them", () => {
    expect(argoSyncOptionsForRequest(undefined, app, false)).toEqual(app)
    expect(argoSyncOptionsForRequest(undefined, [], false)).toBeUndefined()
    expect(argoSyncOptionsForRequest(undefined, undefined, false)).toBeUndefined()
  })
  it('sends explicit options unchanged either way', () => {
    expect(argoSyncOptionsForRequest([], app, false)).toEqual([])
    expect(argoSyncOptionsForRequest(['Replace=true'], app, true)).toEqual(['Replace=true'])
  })
})

describe('buildArgoSyncOpts with options that are no longer current', () => {
  it('sends the unchanged list instead of omitting it', () => {
    expect(buildArgoSyncOpts({ ...base, resourceMode: false, serverSideApply: true, applicationSyncOptions: ['ServerSideApply=true'], sendUnchanged: true }).syncOptions)
      .toEqual(['ServerSideApply=true'])
  })
})
