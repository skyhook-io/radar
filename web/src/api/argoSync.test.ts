import { describe, expect, it } from 'vitest'
import { argoSyncRequestBody } from './client'

// A one-click sync (the drawer's Sync button, the create-namespace retry)
// names no prune and no options; neither may change meaning on an older Radar.
describe('argoSyncRequestBody for a sync that names no options', () => {
  const vars = { namespace: 'argocd', name: 'guestbook', applicationSyncOptions: ['CreateNamespace=true'] }

  it('sends prune false and lets a current Radar use the Application options', () => {
    const body = argoSyncRequestBody(vars, true)
    expect(body.prune).toBe(false)
    expect(body.syncOptions).toBeUndefined()
  })

  it("sends prune false and the Application's options to an older Radar", () => {
    expect(argoSyncRequestBody(vars, false)).toMatchObject({ prune: false, syncOptions: ['CreateNamespace=true'] })
  })

  it('keeps an explicit prune', () => {
    expect(argoSyncRequestBody({ ...vars, prune: true }, false).prune).toBe(true)
  })
})
