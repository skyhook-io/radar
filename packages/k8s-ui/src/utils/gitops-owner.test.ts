import { describe, it, expect } from 'vitest'
import { detectGitOpsOwner } from './gitops-owner'

const FLUX_HELM_NAME = 'helm.toolkit.fluxcd.io/name'
const FLUX_HELM_NS = 'helm.toolkit.fluxcd.io/namespace'
const FLUX_KUSTOMIZE_NAME = 'kustomize.toolkit.fluxcd.io/name'
const FLUX_KUSTOMIZE_NS = 'kustomize.toolkit.fluxcd.io/namespace'
const ARGO_TRACKING_ID = 'argocd.argoproj.io/tracking-id'
const ARGO_INSTANCE = 'argocd.argoproj.io/instance'
const HELM_INSTANCE = 'app.kubernetes.io/instance'

function resource(labels: Record<string, string> = {}, annotations: Record<string, string> = {}) {
  return { metadata: { labels, annotations } }
}

describe('detectGitOpsOwner', () => {
  it('returns null for non-objects', () => {
    expect(detectGitOpsOwner(null)).toBeNull()
    expect(detectGitOpsOwner(undefined)).toBeNull()
    expect(detectGitOpsOwner('string')).toBeNull()
  })

  it('returns null when no GitOps labels/annotations present', () => {
    expect(detectGitOpsOwner(resource({ app: 'foo' }))).toBeNull()
  })

  describe('Flux HelmRelease labels', () => {
    it('extracts name + namespace', () => {
      const got = detectGitOpsOwner(resource({
        [FLUX_HELM_NAME]: 'podinfo',
        [FLUX_HELM_NS]: 'flux-system',
      }))
      expect(got).toEqual({ tool: 'fluxcd', kind: 'helmreleases', namespace: 'flux-system', name: 'podinfo' })
    })

    it('wins over Flux Kustomize labels when both are present (most-direct owner)', () => {
      const got = detectGitOpsOwner(resource({
        [FLUX_HELM_NAME]: 'podinfo',
        [FLUX_HELM_NS]: 'flux-system',
        [FLUX_KUSTOMIZE_NAME]: 'parent',
        [FLUX_KUSTOMIZE_NS]: 'flux-system',
      }))
      expect(got?.kind).toBe('helmreleases')
      expect(got?.name).toBe('podinfo')
    })

    it('requires both name and namespace', () => {
      expect(detectGitOpsOwner(resource({ [FLUX_HELM_NAME]: 'podinfo' }))).toBeNull()
      expect(detectGitOpsOwner(resource({ [FLUX_HELM_NS]: 'flux-system' }))).toBeNull()
    })
  })

  describe('Flux Kustomize labels', () => {
    it('extracts name + namespace', () => {
      const got = detectGitOpsOwner(resource({
        [FLUX_KUSTOMIZE_NAME]: 'infra',
        [FLUX_KUSTOMIZE_NS]: 'flux-system',
      }))
      expect(got).toEqual({ tool: 'fluxcd', kind: 'kustomizations', namespace: 'flux-system', name: 'infra' })
    })
  })

  describe('Argo tracking-id annotation', () => {
    it('parses the namespaced form (<ns>_<name>:...)', () => {
      const got = detectGitOpsOwner(resource({}, {
        [ARGO_TRACKING_ID]: 'argocd_my-app:apps/Deployment:default/web',
      }))
      expect(got).toEqual({ tool: 'argocd', kind: 'applications', namespace: 'argocd', name: 'my-app' })
    })

    it('parses the legacy single-name form (no underscore)', () => {
      const got = detectGitOpsOwner(resource({}, {
        [ARGO_TRACKING_ID]: 'my-app:apps/Deployment:default/web',
      }))
      // Empty namespace is the contract — caller defaults / routes accordingly.
      expect(got).toEqual({ tool: 'argocd', kind: 'applications', namespace: '', name: 'my-app' })
    })

    it('returns null on a malformed tracking-id (no colon)', () => {
      const got = detectGitOpsOwner(resource({}, { [ARGO_TRACKING_ID]: 'just-garbage-no-colon' }))
      // Falls through to label scan; with no labels present, returns null.
      expect(got).toBeNull()
    })

    it('returns null on tracking-id with empty name after underscore', () => {
      const got = detectGitOpsOwner(resource({}, { [ARGO_TRACKING_ID]: 'my-ns_:apps/Deployment:default/web' }))
      expect(got).toBeNull()
    })

    it('wins over the bare instance label (most authoritative)', () => {
      const got = detectGitOpsOwner(resource(
        { [ARGO_INSTANCE]: 'wrong-app' },
        { [ARGO_TRACKING_ID]: 'argocd_right-app:apps/Deployment:default/web' },
      ))
      expect(got?.name).toBe('right-app')
      expect(got?.namespace).toBe('argocd')
    })
  })

  describe('Argo instance label fallback', () => {
    it('extracts name from argocd-specific instance label', () => {
      const got = detectGitOpsOwner(resource({ [ARGO_INSTANCE]: 'guestbook' }))
      expect(got).toEqual({ tool: 'argocd', kind: 'applications', namespace: '', name: 'guestbook' })
    })

    it('does NOT fall back to standard k8s instance label', () => {
      // app.kubernetes.io/instance is stamped by virtually every Helm chart.
      // Treating it as an Argo signal produced a false "Managed by <release>"
      // chip on plain Helm-installed resources, so detection now requires an
      // Argo-specific signal (tracking-id annotation or argocd.argoproj.io/instance).
      expect(detectGitOpsOwner(resource({ [HELM_INSTANCE]: 'guestbook-healthy' }))).toBeNull()
    })

    it('argocd-specific label still wins when both labels present', () => {
      const got = detectGitOpsOwner(resource({
        [ARGO_INSTANCE]: 'argo-pick',
        [HELM_INSTANCE]: 'helm-pick',
      }))
      expect(got?.name).toBe('argo-pick')
    })
  })

  describe('Flux labels beat Argo annotations', () => {
    it('Flux HelmRelease wins over Argo tracking-id', () => {
      const got = detectGitOpsOwner(resource(
        {
          [FLUX_HELM_NAME]: 'podinfo',
          [FLUX_HELM_NS]: 'flux-system',
        },
        { [ARGO_TRACKING_ID]: 'argocd_some-app:apps/Deployment:default/web' },
      ))
      expect(got?.tool).toBe('fluxcd')
      expect(got?.kind).toBe('helmreleases')
    })
  })
})
