import { describe, expect, it } from 'vitest'
import { gitOpsActionPermissions, gitOpsDisabledReasons, isGitOpsActionTarget } from './gitOpsPermissions'

const argoApp = { group: 'argoproj.io', resource: 'applications', kind: 'Application', namespace: 'argocd', name: 'demo' }
const kustomization = { group: 'kustomize.toolkit.fluxcd.io', resource: 'kustomizations', kind: 'Kustomization', namespace: 'apps', name: 'web' }
const gitRepository = { group: 'source.toolkit.fluxcd.io', resource: 'gitrepositories', kind: 'GitRepository', namespace: 'sources', name: 'repo', source: true }

describe('GitOps action permission states', () => {
  it('does not probe colliding Application kinds', () => {
    expect(isGitOpsActionTarget('applications', 'core.oam.dev')).toBe(false)
    expect(isGitOpsActionTarget('applications', 'app.k8s.io')).toBe(false)
    expect(isGitOpsActionTarget('applications', 'argoproj.io')).toBe(true)
    expect(isGitOpsActionTarget('kustomizations', 'kustomize.toolkit.fluxcd.io')).toBe(true)
    // The capability endpoint resolves the controller kind, so a lookalike group must not probe it.
    expect(isGitOpsActionTarget('kustomizations', 'vendor.kustomize.toolkit.fluxcd.io')).toBe(false)
    expect(isGitOpsActionTarget('kustomizations', 'source.toolkit.fluxcd.io')).toBe(false)
    expect(isGitOpsActionTarget('helmreleases', 'source.toolkit.fluxcd.io')).toBe(false)
    expect(isGitOpsActionTarget('alerts', 'notification.toolkit.fluxcd.io')).toBe(true)
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
    expect(reason).toBe("Sync with source also reconciles GitRepository sources/repo — Kubernetes denied permission to read or patch it.")
  })
  it('names both the target and its source when the role can patch neither', () => {
    const reason = gitOpsDisabledReasons('supported', { actions: { 'sync-with-source': {
      allowed: false, denied: [{ ...kustomization, verb: 'patch' }, { ...gitRepository, verb: 'patch' }],
    } } }, false)['sync-with-source']
    expect(reason).toBe("Kubernetes denied permission to patch Flux Kustomization web in apps or its source GitRepository sources/repo.")
  })
  it('names the Argo CD Application and every denied operation without an access parenthetical', () => {
    const reasons = gitOpsDisabledReasons('supported', { actions: {
      refresh: { allowed: false, denied: [{ ...argoApp, verb: 'patch' }] },
      sync: { allowed: false, denied: [{ ...argoApp, verb: 'get' }, { ...argoApp, verb: 'patch' }] },
      suspend: { allowed: false, denied: [{ ...argoApp, verb: 'get' }] },
    } }, false)
    expect(reasons.refresh).toBe("Kubernetes denied permission to patch Argo CD Application demo in argocd.")
    expect(reasons.sync).toBe("Kubernetes denied permission to read or patch Argo CD Application demo in argocd.")
    expect(reasons.suspend).toBe("Kubernetes denied permission to read Argo CD Application demo in argocd.")
  })
  it('disables an unsupported action with its reason, outside the role permissions', () => {
    const reason = "no source reference found in Kustomization apps/web."
    const data = { actions: { reconcile: { allowed: true }, 'sync-with-source': { unsupported: true, reason } } }
    expect(gitOpsDisabledReasons('supported', data, false)['sync-with-source']).toBe(reason)
    expect(gitOpsDisabledReasons('supported', data, false).reconcile).toBeUndefined()
  })
  it('names both missing grants on a Flux target the role can neither read nor patch', () => {
    const denied = [{ ...kustomization, verb: 'get' }, { ...kustomization, verb: 'patch' }]
    expect(gitOpsActionPermissions('supported', { actions: { reconcile: { allowed: false, denied } } }, false)?.reconcile.denied).toEqual(denied)
    expect(gitOpsDisabledReasons('supported', { actions: { reconcile: { allowed: false, denied } } }, false).reconcile).toBe("Kubernetes denied permission to read or patch Flux Kustomization web in apps.")
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
})

it('names every source in a denied HelmChart chain with its own verbs', () => {
  const reason = gitOpsDisabledReasons('supported', { actions: { 'sync-with-source': {
    allowed: false, denied: [
      { ...gitRepository, verb: 'get' },
      { ...gitRepository, verb: 'patch' },
      { group: 'source.toolkit.fluxcd.io', resource: 'helmcharts', kind: 'HelmChart', namespace: 'charts', name: 'shared', source: true, verb: 'patch' },
    ],
  } } }, false)['sync-with-source']
  expect(reason).toBe("Kubernetes denied permission to read or patch source GitRepository sources/repo or patch source HelmChart charts/shared.")
})

it.each(['buckets', 'helmcharts'])('probes %s only in its exact Flux API group', kind => {
  expect(isGitOpsActionTarget(kind, 'source.toolkit.fluxcd.io')).toBe(true)
  expect(isGitOpsActionTarget(kind, 'vendor.source.toolkit.fluxcd.io')).toBe(false)
  expect(isGitOpsActionTarget(kind, 'helm.toolkit.fluxcd.io')).toBe(false)
})
