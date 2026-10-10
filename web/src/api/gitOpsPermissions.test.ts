import { describe, expect, it } from 'vitest'
import { GitOpsActionError, gitOpsActionPermissions, gitOpsDisabledReasons, isGitOpsActionTarget } from './gitOpsPermissions'

const argoApp = { group: 'argoproj.io', resource: 'applications', kind: 'Application', namespace: 'argocd', name: 'demo' }
const kustomization = { group: 'kustomize.toolkit.fluxcd.io', resource: 'kustomizations', kind: 'Kustomization', namespace: 'apps', name: 'web' }
const gitRepository = { group: 'source.toolkit.fluxcd.io', resource: 'gitrepositories', kind: 'GitRepository', namespace: 'sources', name: 'repo', source: true }

describe('GitOps action permission states', () => {
  it('does not probe colliding Application kinds', () => {
    expect(isGitOpsActionTarget('applications', 'core.oam.dev')).toBe(false)
    expect(isGitOpsActionTarget('applications', 'app.k8s.io')).toBe(false)
    expect(isGitOpsActionTarget('applications', 'argoproj.io')).toBe(true)
    expect(isGitOpsActionTarget('kustomizations', 'kustomize.toolkit.fluxcd.io')).toBe(true)
  })
  it('retains known permissions during errors and treats review uncertainty as unknown', () => {
    const data = { actions: { sync: { allowed: true }, refresh: { reason: "Couldn't check your permissions — retrying" } } }
    expect(gitOpsDisabledReasons('supported', data, false).sync).toBeUndefined()
    expect(gitOpsDisabledReasons('supported', data, false).refresh).toBeUndefined()
  })
  it('names the Flux source the role is denied, with every denied verb in one sentence', () => {
    const reason = gitOpsDisabledReasons('supported', { actions: { 'sync-with-source': {
      allowed: false, denied: [{ ...gitRepository, verb: 'get' }, { ...gitRepository, verb: 'patch' }],
    } } }, false)['sync-with-source']
    expect(reason).toBe("Sync with source also reconciles GitRepository sources/repo — your role can't read or patch it.")
  })
  it('names both the target and its source when the role can patch neither', () => {
    const reason = gitOpsDisabledReasons('supported', { actions: { 'sync-with-source': {
      allowed: false, denied: [{ ...kustomization, verb: 'patch' }, { ...gitRepository, verb: 'patch' }],
    } } }, false)['sync-with-source']
    expect(reason).toBe("Your role can't patch Flux Kustomization web in apps or its source GitRepository sources/repo.")
  })
  it('names the Argo CD Application and explains a denied read of the object on screen', () => {
    const reasons = gitOpsDisabledReasons('supported', { actions: {
      refresh: { allowed: false, denied: [{ ...argoApp, verb: 'patch' }] },
      sync: { allowed: false, denied: [{ ...argoApp, verb: 'get' }, { ...argoApp, verb: 'patch' }] },
      suspend: { allowed: false, denied: [{ ...argoApp, verb: 'get' }] },
    } }, false)
    expect(reasons.refresh).toBe("Your role can't patch Argo CD Application demo in argocd.")
    expect(reasons.sync).toBe("Your role can't read or patch Argo CD Application demo in argocd (Radar reads it with its own access).")
    expect(reasons.suspend).toBe("Your role can't read Argo CD Application demo in argocd (Radar reads it with its own access).")
  })
  it('disables an unsupported action with its reason, outside the role permissions', () => {
    const reason = "Sync with source doesn't support HelmRelease apps/web because it uses spec.chartRef."
    const data = { actions: { reconcile: { allowed: true }, 'sync-with-source': { unsupported: true, reason } } }
    expect(gitOpsDisabledReasons('supported', data, false)['sync-with-source']).toBe(reason)
    expect(gitOpsDisabledReasons('supported', data, false).reconcile).toBeUndefined()
  })
  it('names both missing grants on a Flux target the role can neither read nor patch', () => {
    const denied = [{ ...kustomization, verb: 'get' }, { ...kustomization, verb: 'patch' }]
    expect(gitOpsActionPermissions('supported', { actions: { reconcile: { allowed: false, denied } } }, false)?.reconcile.denied).toEqual(denied)
    expect(gitOpsDisabledReasons('supported', { actions: { reconcile: { allowed: false, denied } } }, false).reconcile).toBe("Your role can't read or patch Flux Kustomization web in apps (Radar reads it with its own access).")
  })
  it('shares denial copy with runtime errors and keeps the raw text as detail', () => {
    const error = new GitOpsActionError({ error: 'raw', error_code: 'rbac_denied', verb: 'patch', ...gitRepository, source: undefined }, 403)
    expect(error.message).toBe("Your role can't patch Flux GitRepository repo in sources.")
    expect(error.rawDetail).toBe('raw')
  })
  it('labels admission denials without blaming a role', () => {
    const raw = 'failed to refresh Application argocd/demo: applications.argoproj.io "demo" is forbidden: ValidatingAdmissionPolicy \'freeze\' with binding \'freeze\' denied request: frozen'
    const error = new GitOpsActionError({ error: raw, error_code: 'admission_denied', summary: 'Rejected by ValidatingAdmissionPolicy freeze: frozen' }, 403)
    expect(error.message).toBe('Rejected by ValidatingAdmissionPolicy freeze: frozen')
    expect(error.rawDetail).toBe(raw)
  })
  it('never presents an unstructured 403 as a role problem', () => {
    const raws = [
      'failed to refresh Application argocd/demo: applications.argoproj.io "demo" is forbidden: ValidatingAdmissionPolicy \'freeze\' with binding \'freeze\' denied request: frozen',
      'applications is forbidden: User "opaque" cannot patch resource',
    ]
    for (const raw of raws) {
      const error = new GitOpsActionError({ error: raw }, 403)
      expect(error.message).toBe(raw)
      expect(error.message).not.toContain('Permission denied')
      expect(error.rawDetail).toBeUndefined()
    }
  })
  it('preserves existing controls for older Radar', () => {
    expect(gitOpsDisabledReasons('unsupported', undefined, false)).toEqual({})
    expect(gitOpsDisabledReasons('unknown', undefined, true)).toEqual({})
  })
  it('allows loading and failed checks without prior data', () => {
    expect(gitOpsDisabledReasons('supported', undefined, false)).toEqual({})
  })
  it('allows local reconcile while denying a cross-namespace source action', () => {
    const reasons = gitOpsDisabledReasons('supported', { actions: {
      reconcile: { allowed: true },
      'sync-with-source': { allowed: false, denied: [{ ...gitRepository, verb: 'patch' }] },
    } }, false)
    expect(reasons.reconcile).toBeUndefined()
    expect(reasons['sync-with-source']).toContain('sources/repo')
  })
  it('humanizes structured denial and preserves opaque identity only in raw details', () => {
    const raw = 'applications.argoproj.io is forbidden: User "opaque-user" cannot patch resource "applications"'
    const error = new GitOpsActionError({ error: raw, error_code: 'rbac_denied', verb: 'patch', ...argoApp }, 403)
    expect(error.message).toBe("Your role can't patch Argo CD Application demo in argocd.")
    expect(error.message).not.toContain('opaque-user')
    expect(error.rawDetail).toBe(raw)
  })
  it('shows other server errors unchanged', () => {
    for (const message of ['operation already in progress', 'source reference not found for Kustomization apps/demo', 'ArgoCD Application argocd/demo not found']) {
      expect(new GitOpsActionError({ error: message }, 500).message).toBe(message)
    }
  })
})
