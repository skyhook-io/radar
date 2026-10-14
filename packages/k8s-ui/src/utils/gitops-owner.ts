// Map server-side GitOps management evidence to a navigable
// GitOps owner ref, used by the drawer to render the "Managed by <app>" chip.
//
// The detection (label/annotation parsing, Argo tracking-id decoding, Flux
// label inspection, Helm release annotation, owner-chain walk) lives server-
// side in pkg/topology.SynthesizeManagedBy. This file is now a thin mapper
// from the structured ref into the discriminated union the chip + router need.

import type { Relationships, ResourceRef } from '../types/core'

// GitOpsOwnerRef is a discriminated union — the tool determines which kind is
// valid. Modeling it this way prevents callers (and consumers of the returned
// type) from constructing `{ tool: 'argo', kind: 'helmreleases' }` which would
// route to a non-existent page.
export type GitOpsOwnerRef =
  | { tool: 'argocd'; kind: 'applications'; namespace: string; name: string }
  | { tool: 'fluxcd'; kind: 'kustomizations' | 'helmreleases'; namespace: string; name: string }

// Vocabulary mirrors `pkg/gitops/tree.Tool` so the wire labels match end-to-end.
export type GitOpsOwnerTool = GitOpsOwnerRef['tool']

// API groups for GitOps manager kinds. Used to disambiguate Flux HelmRelease
// from a future native-Helm kind, etc.
const ARGO_APPLICATION_GROUP = 'argoproj.io'
const FLUX_KUSTOMIZE_GROUP = 'kustomize.toolkit.fluxcd.io'
const FLUX_HELM_GROUP = 'helm.toolkit.fluxcd.io'

// gitOpsOwnerFromRelationships maps tracking metadata or logical managers to
// a GitOpsOwnerRef. Tracking metadata has precedence; logical management
// remains independent of the Kubernetes owner chain.
//
// Old radar binaries (pre-T2) emit no managedBy field; this returns null in
// that case and the drawer silently skips the chip — natural pressure to
// upgrade rather than carry a label-parsing fallback in the client.
export function gitOpsOwnerFromRelationships(
  rel: Relationships | undefined | null,
): GitOpsOwnerRef | null {
  const trackedManager = rel?.managedBy?.[0]
  if (trackedManager) {
    const owner = gitOpsOwnerFromRef(trackedManager)
    if (owner) return owner
  }
  for (const manager of rel?.managers ?? []) {
    const owner = gitOpsOwnerFromRef(manager)
    if (owner) return owner
  }
  return null
}

/** A manager ref (relationships.managedBy, a workspace's managedBy) as a GitOps owner, or null when the manager is not a GitOps controller. */
export function gitOpsOwnerFromRef(ref: ResourceRef): GitOpsOwnerRef | null {
  switch (true) {
    case ref.kind === 'Application' && ref.group === ARGO_APPLICATION_GROUP:
      return { tool: 'argocd', kind: 'applications', namespace: ref.namespace, name: ref.name }
    case ref.kind === 'Kustomization' && ref.group === FLUX_KUSTOMIZE_GROUP:
      return { tool: 'fluxcd', kind: 'kustomizations', namespace: ref.namespace, name: ref.name }
    case ref.kind === 'HelmRelease' && ref.group === FLUX_HELM_GROUP:
      return { tool: 'fluxcd', kind: 'helmreleases', namespace: ref.namespace, name: ref.name }
    default:
      return null
  }
}
