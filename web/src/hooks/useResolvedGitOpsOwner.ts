import { useMemo } from 'react'
import {
  getGitOpsResourceStatus,
  gitOpsOwnerFromRelationships,
  type GitOpsOwnerRef,
  type GitOpsStatus,
  type HelmOwnerRef,
} from '@skyhook-io/k8s-ui'
import type { Relationships, ResourceRef } from '../types'
import { kindToPluralWithGroup } from '../utils/navigation'
import { useResource, useResourceWithRelationships, useResources } from '../api/client'

export interface ResolvedGitOpsOwner {
  /** GitOps owner, with an Argo Application's namespace filled in when the
   *  tracking label omitted it and exactly one Application matches. */
  owner: GitOpsOwnerRef | null
  /** Owner as reported by relationships, before namespace resolution. */
  rawOwner: GitOpsOwnerRef | null
  /** The owner CR (Application / Kustomization / HelmRelease). */
  ownerObject: any
  ownerPending: boolean
  ownerVerified: boolean
  ownerStatus: GitOpsStatus | null
  /** The label/annotation that ties the object to its owner. */
  ownerSource: string | null
  helmOwner: HelmOwnerRef | null
  helmSource: string | null
  /** A parent (e.g. the Deployment of a ReplicaSet) consulted when the
   *  object carries no GitOps ownership itself. */
  inheritedLookupRef: ResourceRef | null
  lookupPending: boolean
  lookupError: boolean
}

// Resolves who manages a resource: its own GitOps/Helm tracking metadata or,
// failing that, the metadata of the workload that owns it.
export function useResolvedGitOpsOwner({
  kind,
  group,
  namespace,
  name,
  relationships,
  resource,
  enabled = true,
}: {
  kind: string
  group?: string
  namespace: string
  name: string
  relationships: Relationships | undefined
  resource: any
  enabled?: boolean
}): ResolvedGitOpsOwner {
  const active = enabled && Boolean(relationships)
  const relationshipGitopsOwner = useMemo(
    () => (active ? gitOpsOwnerFromRelationships(relationships) : null),
    [active, relationships],
  )
  const inheritedLookupRef = useMemo(
    () =>
      active
        ? findInheritedGitOpsLookupRef(relationships, relationshipGitopsOwner, { kind, namespace, name, group })
        : null,
    [active, relationships, relationshipGitopsOwner, kind, namespace, name, group],
  )
  const inherited = useResourceWithRelationships<any>(
    inheritedLookupRef ? kindToPluralWithGroup(inheritedLookupRef.kind, inheritedLookupRef.group ?? '') : '',
    inheritedLookupRef?.namespace ?? '',
    inheritedLookupRef?.name ?? '',
    inheritedLookupRef?.group,
  )
  const inheritedGitopsOwner = useMemo(
    () => (inheritedLookupRef ? gitOpsOwnerFromRelationships(inherited.data?.relationships) : null),
    [inheritedLookupRef, inherited.data?.relationships],
  )
  const relationshipHelmOwner = useMemo(
    () => (active ? nativeHelmOwnerFromRelationships(relationships, resource?.metadata?.namespace ?? namespace) : null),
    [active, relationships, resource?.metadata?.namespace, namespace],
  )
  const inheritedHelmOwner = useMemo(
    () =>
      inheritedLookupRef
        ? nativeHelmOwnerFromRelationships(
            inherited.data?.relationships,
            inherited.data?.resource?.metadata?.namespace ?? namespace,
          )
        : null,
    [inheritedLookupRef, inherited.data?.relationships, inherited.data?.resource?.metadata?.namespace, namespace],
  )

  const rawOwner = relationshipGitopsOwner ?? inheritedGitopsOwner
  const gitOpsSourceResource = relationshipGitopsOwner ? resource : inherited.data?.resource
  const helmOwner = relationshipHelmOwner ?? inheritedHelmOwner
  const helmSourceResource = relationshipHelmOwner ? resource : inherited.data?.resource

  const shouldResolveArgoNamespace = rawOwner?.tool === 'argocd' && !rawOwner.namespace
  const { data: argoApplications } = useResources<any>('applications', undefined, 'argoproj.io', {
    enabled: shouldResolveArgoNamespace,
  })
  const owner = useMemo(() => resolveGitOpsOwner(rawOwner, argoApplications), [rawOwner, argoApplications])

  const ownerGroup = owner ? gitOpsOwnerGroup(owner) : ''
  const shouldFetchOwner = Boolean(owner?.namespace)
  const ownerQuery = useResource<any>(
    shouldFetchOwner ? owner!.kind : '',
    owner?.namespace ?? '',
    owner?.name ?? '',
    ownerGroup,
    { enabled: shouldFetchOwner },
  )
  const ownerObject = shouldFetchOwner ? ownerQuery.data : undefined

  const ownerStatus = useMemo(() => deriveGitOpsOwnerStatus(owner, ownerObject), [owner, ownerObject])
  const ownerSource = useMemo(
    () => describeGitOpsOwnerSource(rawOwner, gitOpsSourceResource),
    [rawOwner, gitOpsSourceResource],
  )
  const helmSource = useMemo(() => describeHelmOwnerSource(helmOwner, helmSourceResource), [helmOwner, helmSourceResource])

  return {
    owner,
    rawOwner,
    ownerObject,
    ownerPending: Boolean(shouldFetchOwner && ownerQuery.isLoading && !ownerQuery.data),
    ownerVerified: Boolean(shouldFetchOwner && ownerQuery.data),
    ownerStatus,
    ownerSource,
    helmOwner,
    helmSource,
    inheritedLookupRef,
    lookupPending: Boolean(inheritedLookupRef && inherited.isPending),
    lookupError: Boolean(inheritedLookupRef && inherited.isError),
  }
}

export function gitOpsOwnerGroup(owner: GitOpsOwnerRef): string {
  if (owner.tool === 'argocd') return 'argoproj.io'
  if (owner.kind === 'kustomizations') return 'kustomize.toolkit.fluxcd.io'
  return 'helm.toolkit.fluxcd.io'
}

/** The owner as the singular Kind + group the server's APIs take. */
export function gitOpsOwnerKindRef(owner: GitOpsOwnerRef): { kind: string; group: string } {
  if (owner.tool === 'argocd') return { kind: 'Application', group: 'argoproj.io' }
  if (owner.kind === 'kustomizations') return { kind: 'Kustomization', group: 'kustomize.toolkit.fluxcd.io' }
  return { kind: 'HelmRelease', group: 'helm.toolkit.fluxcd.io' }
}

function resolveGitOpsOwner(owner: GitOpsOwnerRef | null, argoApplications: any[] | undefined): GitOpsOwnerRef | null {
  if (!owner || owner.namespace || owner.tool !== 'argocd') return owner
  const matches = (argoApplications ?? []).filter((app) => app?.metadata?.name === owner.name)
  if (matches.length !== 1) return owner
  const namespace = matches[0]?.metadata?.namespace
  return namespace ? { ...owner, namespace } : owner
}

export function findInheritedGitOpsLookupRef(
  relationships: Relationships | undefined,
  directOwner: GitOpsOwnerRef | null,
  current: ResourceRef,
): ResourceRef | null {
  if (directOwner) return null
  const inheritedManagerRefs = (relationships?.managedBy ?? []).filter(
    (ref) => !gitOpsOwnerFromRelationships({ managedBy: [ref] }) && !isNativeHelmManager(ref),
  )
  const candidates = [relationships?.deployment, ...inheritedManagerRefs, relationships?.owner].filter(
    Boolean,
  ) as ResourceRef[]

  return candidates.find((ref) => !isCurrentResource(ref, current)) ?? null
}

export function nativeHelmOwnerFromRelationships(
  relationships: Relationships | undefined,
  fallbackNamespace: string,
): HelmOwnerRef | null {
  const ref = relationships?.managedBy?.[0]
  if (!ref || !isNativeHelmManager(ref)) return null
  return {
    namespace: ref.namespace || fallbackNamespace,
    name: ref.name,
  }
}

function isCurrentResource(ref: ResourceRef, current: ResourceRef): boolean {
  return (
    kindToPluralWithGroup(ref.kind, ref.group ?? '') === kindToPluralWithGroup(current.kind, current.group ?? '') &&
    ref.namespace === current.namespace &&
    ref.name === current.name &&
    (ref.group ?? '') === (current.group ?? '')
  )
}

function isNativeHelmManager(ref: ResourceRef): boolean {
  return ref.kind === 'HelmRelease' && ref.group !== 'helm.toolkit.fluxcd.io'
}

function describeGitOpsOwnerSource(owner: GitOpsOwnerRef | null, resource: any): string | null {
  if (!owner || !resource) return null
  const labels = resource.metadata?.labels ?? {}
  const annotations = resource.metadata?.annotations ?? {}

  if (owner.tool === 'fluxcd') {
    const nameKey = owner.kind === 'helmreleases' ? 'helm.toolkit.fluxcd.io/name' : 'kustomize.toolkit.fluxcd.io/name'
    const nsKey =
      owner.kind === 'helmreleases' ? 'helm.toolkit.fluxcd.io/namespace' : 'kustomize.toolkit.fluxcd.io/namespace'
    if (labels[nameKey] || labels[nsKey]) {
      return `${nameKey}=${labels[nameKey] ?? ''}, ${nsKey}=${labels[nsKey] ?? ''}`
    }
  }

  const trackingID = annotations['argocd.argoproj.io/tracking-id']
  if (trackingID) return `argocd.argoproj.io/tracking-id=${trackingID}`
  const argoInstance = labels['argocd.argoproj.io/instance']
  if (argoInstance) return `argocd.argoproj.io/instance=${argoInstance}`
  return null
}

function describeHelmOwnerSource(owner: HelmOwnerRef | null, resource: any): string | null {
  if (!owner || !resource) return null
  const annotations = resource.metadata?.annotations ?? {}
  const releaseName = annotations['meta.helm.sh/release-name']
  const releaseNamespace = annotations['meta.helm.sh/release-namespace']
  if (releaseName || releaseNamespace) {
    return `meta.helm.sh/release-name=${releaseName ?? ''}, meta.helm.sh/release-namespace=${releaseNamespace ?? ''}`
  }
  return null
}

function deriveGitOpsOwnerStatus(owner: GitOpsOwnerRef | null, resource: any): GitOpsStatus | null {
  if (!owner || !resource || !hasGitOpsStatusPayload(owner, resource)) return null
  return getGitOpsResourceStatus(owner.kind, resource)
}

function hasGitOpsStatusPayload(owner: GitOpsOwnerRef, resource: any): boolean {
  if (owner.kind === 'applications') {
    const status = resource.status ?? {}
    return Boolean(status.sync?.status || status.health?.status || status.operationState?.phase)
  }
  if (resource.spec?.suspend === true) return true
  return Array.isArray(resource.status?.conditions) && resource.status.conditions.length > 0
}
