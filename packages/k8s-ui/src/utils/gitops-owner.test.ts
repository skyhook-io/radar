import { describe, it, expect } from 'vitest'

import type { Relationships } from '../types/core'

import { gitOpsOwnerFromRelationships, managerListing } from './gitops-owner'

describe('gitOpsOwnerFromRelationships', () => {
  it('returns null when relationships is undefined', () => {
    expect(gitOpsOwnerFromRelationships(undefined)).toBeNull()
  })

  it('returns null when managedBy is absent', () => {
    expect(gitOpsOwnerFromRelationships({})).toBeNull()
  })

  it('returns null when managedBy is empty', () => {
    expect(gitOpsOwnerFromRelationships({ managedBy: [] })).toBeNull()
  })

  it('maps an ArgoCD Application ref', () => {
    const rel: Relationships = {
      managedBy: [{ kind: 'Application', group: 'argoproj.io', namespace: 'argocd', name: 'storefront' }],
    }
    expect(gitOpsOwnerFromRelationships(rel)).toEqual({
      tool: 'argocd',
      kind: 'applications',
      namespace: 'argocd',
      name: 'storefront',
    })
  })

  it('maps a Flux Kustomization ref', () => {
    const rel: Relationships = {
      managedBy: [{ kind: 'Kustomization', group: 'kustomize.toolkit.fluxcd.io', namespace: 'flux-system', name: 'prod-apps' }],
    }
    expect(gitOpsOwnerFromRelationships(rel)).toEqual({
      tool: 'fluxcd',
      kind: 'kustomizations',
      namespace: 'flux-system',
      name: 'prod-apps',
    })
  })

  it('maps a Flux HelmRelease ref', () => {
    const rel: Relationships = {
      managedBy: [{ kind: 'HelmRelease', group: 'helm.toolkit.fluxcd.io', namespace: 'flux-system', name: 'cert-manager' }],
    }
    expect(gitOpsOwnerFromRelationships(rel)).toEqual({
      tool: 'fluxcd',
      kind: 'helmreleases',
      namespace: 'flux-system',
      name: 'cert-manager',
    })
  })

  it('disambiguates Flux HelmRelease from native Helm via API group', () => {
    // A native Helm release would not carry the helm.toolkit.fluxcd.io group;
    // server-side SynthesizeManagedBy only assigns that group for Flux.
    const rel: Relationships = {
      managedBy: [{ kind: 'HelmRelease', group: 'helm.sh', namespace: 'default', name: 'native-helm' }],
    }
    expect(gitOpsOwnerFromRelationships(rel)).toBeNull()
  })

  it('returns null for native K8s owners (no GitOps chip)', () => {
    const rel: Relationships = {
      managedBy: [{ kind: 'Deployment', namespace: 'prod', name: 'web' }],
    }
    expect(gitOpsOwnerFromRelationships(rel)).toBeNull()
  })

  it('returns null for an unrecognized manager kind', () => {
    const rel: Relationships = {
      managedBy: [{ kind: 'CustomController', group: 'example.com', namespace: 'prod', name: 'thing' }],
    }
    expect(gitOpsOwnerFromRelationships(rel)).toBeNull()
  })

  it('only considers the first managedBy entry', () => {
    // The server emits the topmost meaningful manager first; if there are
    // multiple, downstream chips intentionally only render the primary.
    const rel: Relationships = {
      managedBy: [
        { kind: 'Application', group: 'argoproj.io', namespace: 'argocd', name: 'primary' },
        { kind: 'Kustomization', group: 'kustomize.toolkit.fluxcd.io', namespace: 'flux-system', name: 'secondary' },
      ],
    }
    expect(gitOpsOwnerFromRelationships(rel)).toEqual({
      tool: 'argocd',
      kind: 'applications',
      namespace: 'argocd',
      name: 'primary',
    })
  })
})

describe('managerListing', () => {
  const owner = { tool: 'argocd' as const, kind: 'applications' as const, namespace: 'argocd', name: 'billing' }
  const app = (name: string) => ({ kind: 'Application', group: 'argoproj.io', namespace: 'argocd', name })

  it('confirms an owner that is the only manager listing the resource', () => {
    expect(managerListing(owner, [app('billing')], true, true)).toEqual({ state: 'confirmed' })
  })

  it('confirms an instance-label owner whose namespace is not known yet', () => {
    expect(managerListing({ ...owner, namespace: '' }, [app('billing')], true, true)).toEqual({ state: 'confirmed' })
  })

  it('reports a conflict when other managers list the resource and the owner does not', () => {
    expect(managerListing(owner, [app('hub')], true, true)).toEqual({
      state: 'conflict',
      listedBy: [{ tool: 'argocd', kind: 'applications', namespace: 'argocd', name: 'hub' }],
    })
  })

  it('says nothing when the owner is one of several managers', () => {
    expect(managerListing(owner, [app('billing'), app('hub')], true, true)).toBeUndefined()
  })

  it('says nothing without managers to compare', () => {
    expect(managerListing(owner, undefined, true, true)).toBeUndefined()
    expect(managerListing(owner, [], true, true)).toBeUndefined()
    expect(managerListing(null, [app('billing')], true, true)).toBeUndefined()
    expect(managerListing(owner, [{ kind: 'Deployment', group: 'apps', namespace: 'x', name: 'y' }], true, true)).toBeUndefined()
  })

  it('claims nothing for an owner not verified in this cluster', () => {
    expect(managerListing(owner, [app('billing')], false, true)).toBeUndefined()
    expect(managerListing(owner, [app('hub')], false, true)).toBeUndefined()
  })

  it('claims nothing when the search left managers unsettled, since a listed one may be a topology link', () => {
    expect(managerListing(owner, [app('billing')], true, false)).toBeUndefined()
    expect(managerListing(owner, [app('hub')], true, false)).toBeUndefined()
  })
})
