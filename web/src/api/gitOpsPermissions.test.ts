import { describe, expect, it } from 'vitest'
import { GitOpsActionError, gitOpsDisabledReasons, isGitOpsActionTarget } from './gitOpsPermissions'

describe('GitOps action permission states', () => {
  it('does not probe colliding Application kinds', () => {
    expect(isGitOpsActionTarget('applications', 'core.oam.dev')).toBe(false)
    expect(isGitOpsActionTarget('applications', 'app.k8s.io')).toBe(false)
    expect(isGitOpsActionTarget('applications', 'argoproj.io')).toBe(true)
    expect(isGitOpsActionTarget('kustomizations', 'kustomize.toolkit.fluxcd.io')).toBe(true)
  })
  it('retains known permissions during errors and treats review uncertainty as unknown', () => {
    const data = { actions: { sync: { allowed: true }, refresh: { reason: "Couldn't check your permissions — retrying" } } }
    expect(gitOpsDisabledReasons('supported', data, new Error('offline'), false).sync).toBeUndefined()
    expect(gitOpsDisabledReasons('supported', data, undefined, false).refresh).toBeUndefined()
  })
  it('names the Flux source and shares denial copy with runtime errors', () => {
    const denial = { allowed: false, error_code: 'rbac_denied', verb: 'patch', resource: 'gitrepositories', kind: 'GitRepository', group: 'source.toolkit.fluxcd.io', namespace: 'gaps-r4-source', name: 'repo', source: true }
    const reason = gitOpsDisabledReasons('supported', { actions: { 'sync-with-source': denial } }, undefined, false)['sync-with-source']
    expect(reason).toBe("Sync with source also reconciles GitRepository gaps-r4-source/repo — your role can't patch it.")
    expect(new GitOpsActionError({ ...denial, error: 'raw' }, 403).message).toBe(reason)
  })
  it('labels admission denials without blaming a role', () => {
    expect(new GitOpsActionError({ error: 'Gatekeeper rejected the patch', error_code: 'admission_denied' }, 403).message).toBe('Rejected by an admission policy: Gatekeeper rejected the patch')
  })
  it('preserves existing controls for older Radar', () => {
    expect(gitOpsDisabledReasons('unsupported', undefined, undefined, false)).toEqual({})
    expect(gitOpsDisabledReasons('unknown', undefined, new Error('old route'), true)).toEqual({})
  })
  it('allows loading and failed checks without prior data', () => {
    expect(gitOpsDisabledReasons('supported', undefined, undefined, false)).toEqual({})
    expect(gitOpsDisabledReasons('supported', undefined, new Error('offline'), false)).toEqual({})
  })
  it('allows local reconcile while denying a cross-namespace source action', () => {
    const reasons = gitOpsDisabledReasons('supported', { actions: {
      reconcile: { allowed: true },
      'sync-with-source': { allowed: false, error_code: 'rbac_denied', verb: 'patch', resource: 'gitrepositories', kind: 'GitRepository', group: 'source.toolkit.fluxcd.io', namespace: 'sources', name: 'repo' },
    } }, undefined, false)
    expect(reasons.reconcile).toBeUndefined()
    expect(reasons['sync-with-source']).toContain('in sources')
  })
  it('humanizes structured denial and preserves opaque identity only in raw details', () => {
    const raw = 'applications.argoproj.io is forbidden: User "opaque-user" cannot patch resource "applications"'
    const error = new GitOpsActionError({ error: raw, error_code: 'rbac_denied', verb: 'patch', group: 'argoproj.io', resource: 'applications', namespace: 'argocd' }, 403)
    expect(error.message).toContain("Your role can't patch Argo CD Applications")
    expect(error.message).toContain('in argocd')
    expect(error.message).not.toContain('opaque-user')
    expect(error.rawDetail).toBe(raw)
  })
  it('uses the existing humanizer for older action responses', () => {
    const error = new GitOpsActionError({ error: 'applications is forbidden: User "opaque" cannot patch resource' }, 403)
    expect(error.message).toContain('Permission denied')
    expect(error.message).not.toContain('opaque')
    for (const message of ['operation already in progress', 'source reference not found for Kustomization apps/demo', 'ArgoCD Application argocd/demo not found']) {
      expect(new GitOpsActionError({ error: message }, 500).message).toBe(message)
    }
  })
})
