import { describe, it, expect } from 'vitest'
import { gitOpsRouteForOwner, gitOpsRouteForResource } from './gitops-route'

describe('gitOpsRouteForOwner', () => {
  it('routes to detail URL for Argo with known namespace', () => {
    expect(gitOpsRouteForOwner({ tool: 'argocd', kind: 'applications', namespace: 'argocd', name: 'guestbook' }))
      .toBe('/gitops/detail/applications/argocd/guestbook')
  })
  it('routes to fleet when Argo namespace is unknown (bare instance label)', () => {
    expect(gitOpsRouteForOwner({ tool: 'argocd', kind: 'applications', namespace: '', name: 'guestbook' }))
      .toBe('/gitops')
  })
  it('routes Flux Kustomization owners with their namespace', () => {
    expect(gitOpsRouteForOwner({ tool: 'fluxcd', kind: 'kustomizations', namespace: 'flux-system', name: 'podinfo' }))
      .toBe('/gitops/detail/kustomizations/flux-system/podinfo')
  })
  it('routes Flux HelmRelease owners', () => {
    expect(gitOpsRouteForOwner({ tool: 'fluxcd', kind: 'helmreleases', namespace: 'flux-system', name: 'podinfo' }))
      .toBe('/gitops/detail/helmreleases/flux-system/podinfo')
  })
  it('encodes path components', () => {
    expect(gitOpsRouteForOwner({ tool: 'fluxcd', kind: 'kustomizations', namespace: 'has space', name: 'a/b' }))
      .toBe('/gitops/detail/kustomizations/has%20space/a%2Fb')
  })
})

describe('gitOpsRouteForResource', () => {
  const mk = (api: string, kind: string, ns = 'x', name = 'y') => ({
    apiVersion: api, kind, metadata: { namespace: ns, name },
  })

  it('routes Argo CRs (Application/ApplicationSet/AppProject)', () => {
    expect(gitOpsRouteForResource(mk('argoproj.io/v1alpha1', 'Application', 'argocd', 'app')))
      .toBe('/gitops/detail/applications/argocd/app')
    expect(gitOpsRouteForResource(mk('argoproj.io/v1alpha1', 'ApplicationSet', 'argocd', 'set')))
      .toBe('/gitops/detail/applicationsets/argocd/set')
    expect(gitOpsRouteForResource(mk('argoproj.io/v1alpha1', 'AppProject', 'argocd', 'proj')))
      .toBe('/gitops/detail/appprojects/argocd/proj')
  })

  it('routes Flux reconcilers (Kustomization/HelmRelease)', () => {
    expect(gitOpsRouteForResource(mk('kustomize.toolkit.fluxcd.io/v1', 'Kustomization', 'flux-system', 'k')))
      .toBe('/gitops/detail/kustomizations/flux-system/k')
    expect(gitOpsRouteForResource(mk('helm.toolkit.fluxcd.io/v2', 'HelmRelease', 'flux-system', 'hr')))
      .toBe('/gitops/detail/helmreleases/flux-system/hr')
  })

  // Source CRs are *not* portals — the resource drawer renders them better.
  // If this ever flips, also update pkg/gitops/tree/graph.go classifyGitOpsKind
  // and web/src/components/gitops/GitOpsView.tsx isGitOpsDetailRef.
  it('returns null for Flux source CRs (drawer-only)', () => {
    expect(gitOpsRouteForResource(mk('source.toolkit.fluxcd.io/v1', 'GitRepository'))).toBeNull()
    expect(gitOpsRouteForResource(mk('source.toolkit.fluxcd.io/v1', 'HelmRepository'))).toBeNull()
    expect(gitOpsRouteForResource(mk('source.toolkit.fluxcd.io/v1beta2', 'OCIRepository'))).toBeNull()
    expect(gitOpsRouteForResource(mk('source.toolkit.fluxcd.io/v1beta2', 'Bucket'))).toBeNull()
    expect(gitOpsRouteForResource(mk('source.toolkit.fluxcd.io/v1', 'HelmChart'))).toBeNull()
  })

  it('returns null for ordinary K8s resources', () => {
    expect(gitOpsRouteForResource(mk('apps/v1', 'Deployment'))).toBeNull()
    expect(gitOpsRouteForResource(mk('v1', 'Service'))).toBeNull()
    expect(gitOpsRouteForResource(mk('v1', 'Pod'))).toBeNull()
  })

  it('returns null on malformed input', () => {
    expect(gitOpsRouteForResource(null)).toBeNull()
    expect(gitOpsRouteForResource(undefined)).toBeNull()
    expect(gitOpsRouteForResource({})).toBeNull()
    expect(gitOpsRouteForResource({ kind: 'Application' })).toBeNull() // no apiVersion → no group
  })

  it('distinguishes name collisions by group', () => {
    // Knative Service vs core Service — not a portal kind, but pins behavior.
    expect(gitOpsRouteForResource(mk('serving.knative.dev/v1', 'Service'))).toBeNull()
    // A hypothetical "Application" CR in another group must not portal.
    expect(gitOpsRouteForResource(mk('example.com/v1', 'Application'))).toBeNull()
  })
})
