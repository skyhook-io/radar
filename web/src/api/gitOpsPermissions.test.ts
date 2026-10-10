import { describe, expect, it } from 'vitest'
import { GitOpsActionError, gitOpsDisabledReasons } from './gitOpsPermissions'

describe('GitOps action permission states', () => {
  it('preserves existing controls for older Radar', () => {
    expect(gitOpsDisabledReasons('unsupported', undefined, undefined, false)).toEqual({})
    expect(gitOpsDisabledReasons('unknown', undefined, new Error('old route'), true)).toEqual({})
  })
  it('blocks during loading and failed checks without claiming a role denial', () => {
    expect(gitOpsDisabledReasons('supported', undefined, undefined, false).sync).toBe('Checking your permissions…')
    expect(gitOpsDisabledReasons('supported', undefined, new Error('offline'), false).refresh).toBe("Couldn't verify your permissions. Try again.")
  })
  it('allows local reconcile while denying a cross-namespace source action', () => {
    const reasons = gitOpsDisabledReasons('supported', { actions: {
      reconcile: { allowed: true },
      'sync-with-source': { allowed: false, reason: "Your role can't patch Flux GitRepository resources in sources." },
    } }, undefined, false)
    expect(reasons.reconcile).toBeUndefined()
    expect(reasons['sync-with-source']).toContain('in sources')
  })
  it('humanizes structured denial and preserves opaque identity only in raw details', () => {
    const raw = 'applications.argoproj.io is forbidden: User "opaque-user" cannot patch resource "applications"'
    const error = new GitOpsActionError({ error: raw, code: 'rbac_denied', verb: 'patch', group: 'argoproj.io', resource: 'applications', namespace: 'argocd' }, 403)
    expect(error.message).toContain("Your role can't patch applications")
    expect(error.message).toContain('namespace argocd')
    expect(error.message).not.toContain('opaque-user')
    expect(error.rawDetail).toBe(raw)
  })
  it('uses the existing humanizer for older action responses', () => {
    const error = new GitOpsActionError({ error: 'applications is forbidden: User "opaque" cannot patch resource' }, 403)
    expect(error.message).toContain('Permission denied')
    expect(error.message).not.toContain('opaque')
    expect(new GitOpsActionError({ error: 'operation already in progress' }, 409).message).toBe('operation already in progress')
  })
})
