// Routes to the GitOps detail page (in fleet view if the lookup is ambiguous).
//
// Two distinct entry points:
//
//   gitOpsRouteForOwner(owner)
//     For "Managed by <app>" affordances: an ordinary K8s resource carries
//     GitOps ownership labels/annotations and we want to navigate to its
//     owning GitOps CR. Wraps the detect→navigate split: detectGitOpsOwner
//     gives us a typed ref, this helper turns the ref into a URL.
//
//   gitOpsRouteForResource(resource)
//     For "Open in GitOps" affordances on the GitOps CR itself
//     (Application, ApplicationSet, AppProject, Kustomization, HelmRelease).
//     Source CRs (GitRepository/HelmRepository/OCIRepository/Bucket/HelmChart)
//     return null — they're not portal kinds; the standard resource drawer
//     renders them better.
//
// Keep the portal catalog in sync with:
//   - pkg/gitops/tree/graph.go        (Go classifier)
//   - web/src/components/gitops/GitOpsView.tsx isGitOpsDetailRef
// If a kind is added or removed here, update those too.

import type { GitOpsOwnerRef } from './gitops-owner'

export interface GitOpsResource {
  apiVersion?: string
  kind?: string
  metadata?: { namespace?: string; name?: string }
}

// gitOpsRouteForOwner turns a detected GitOps owner ref into a navigable URL.
// When the owner namespace is unknown (Argo apps detected only by the bare
// app.kubernetes.io/instance label), routes to the GitOps fleet view so the
// user can locate the app rather than landing on a 404'd detail URL.
export function gitOpsRouteForOwner(owner: GitOpsOwnerRef): string {
  if (owner.tool === 'argocd' && !owner.namespace) {
    return '/gitops'
  }
  return gitOpsDetailUrl(owner.kind, owner.namespace, owner.name)
}

// gitOpsRouteForResource returns the GitOps detail URL for a portal-kind
// resource, or null when the resource isn't itself a GitOps CR that has its
// own detail page. Callers should fall back to the standard resource drawer
// on null.
export function gitOpsRouteForResource(resource: GitOpsResource | null | undefined): string | null {
  if (!resource) return null
  const kind = resource.kind?.toLowerCase()
  const group = apiGroup(resource.apiVersion)
  const ns = resource.metadata?.namespace ?? ''
  const name = resource.metadata?.name ?? ''
  if (!kind || !name) return null

  const plural = portalPluralFor(kind, group)
  if (!plural) return null
  return gitOpsDetailUrl(plural, ns, name)
}

function portalPluralFor(kindLower: string, group: string): string | null {
  if (group === 'argoproj.io') {
    if (kindLower === 'application') return 'applications'
    if (kindLower === 'applicationset') return 'applicationsets'
    if (kindLower === 'appproject') return 'appprojects'
    return null
  }
  if (group === 'kustomize.toolkit.fluxcd.io' && kindLower === 'kustomization') return 'kustomizations'
  if (group === 'helm.toolkit.fluxcd.io' && kindLower === 'helmrelease') return 'helmreleases'
  return null
}

function apiGroup(apiVersion: string | undefined): string {
  if (!apiVersion) return ''
  const slash = apiVersion.indexOf('/')
  return slash < 0 ? '' : apiVersion.slice(0, slash)
}

function gitOpsDetailUrl(kindPlural: string, namespace: string, name: string): string {
  const ns = encodeURIComponent(namespace || '_')
  return `/gitops/detail/${encodeURIComponent(kindPlural)}/${ns}/${encodeURIComponent(name)}`
}

// gitOpsRouteForKind is a kind-only variant for callers that only have
// (kind, namespace, name) — e.g. Audit findings, which today don't carry the
// apiGroup of the resource they subject. Matches by kind alone, accepting the
// (very small) risk of routing a CRD named "Application" or "Kustomization"
// from a non-GitOps group to a "not found" GitOps detail page. When/if those
// callers grow apiGroup access, prefer gitOpsRouteForResource.
export function gitOpsRouteForKind(kind: string, namespace: string, name: string): string | null {
  if (!kind || !name) return null
  switch (kind) {
    case 'Application':
      return gitOpsDetailUrl('applications', namespace, name)
    case 'ApplicationSet':
      return gitOpsDetailUrl('applicationsets', namespace, name)
    case 'AppProject':
      return gitOpsDetailUrl('appprojects', namespace, name)
    case 'Kustomization':
      return gitOpsDetailUrl('kustomizations', namespace, name)
    case 'HelmRelease':
      return gitOpsDetailUrl('helmreleases', namespace, name)
    default:
      return null
  }
}
