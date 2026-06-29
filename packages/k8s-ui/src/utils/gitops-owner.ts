// Map a resource's server-side `Relationships.managedBy` to a navigable
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

// gitOpsOwnerFromRelationships reads relationships.managedBy[0] and maps it to
// a GitOpsOwnerRef when the manager is a GitOps controller. Returns null when
// the manager is a native K8s owner (Deployment, ReplicaSet, etc.), a plain
// Helm release, or when no manager is reported.
//
// Old radar binaries (pre-T2) emit no managedBy field; this returns null in
// that case and the drawer silently skips the chip — natural pressure to
// upgrade rather than carry a label-parsing fallback in the client.
export function gitOpsOwnerFromRelationships(
  rel: Relationships | undefined | null,
): GitOpsOwnerRef | null {
  const refs = rel?.managedBy
  if (!refs || refs.length === 0) return null
  return refToGitOpsOwner(refs[0])
}

function refToGitOpsOwner(ref: ResourceRef): GitOpsOwnerRef | null {
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
