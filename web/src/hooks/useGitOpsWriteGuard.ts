import { useMemo } from 'react'
import { useQuery } from '@tanstack/react-query'
import {
  evaluateGitOpsWriteGuard,
  gitOpsWriteEvidencePaths,
  type GitOpsWrite,
  type GitOpsWriteEvidence,
  type GitOpsWriteGuard,
  type GitOpsWriteTarget,
} from '@skyhook-io/k8s-ui'
import type { Relationships } from '../types'
import { fetchGitOpsWriteEvidence, useResource, useRadarFeature } from '../api/client'
import { getApiBase } from '../api/config'
import { kindToPluralWithGroup } from '../utils/navigation'
import { gitOpsOwnerKindRef, useResolvedGitOpsOwner, type ResolvedGitOpsOwner } from './useResolvedGitOpsOwner'

export interface GitOpsWriteGuardState {
  /** Undefined only while disabled. */
  guard: GitOpsWriteGuard | undefined
  ownership: ResolvedGitOpsOwner
  evidence: GitOpsWriteEvidence | undefined
}

// Evaluates the GitOps write guard for a write to `target`: resolves the
// owner (or takes one the host already resolved), asks the server for the
// field-level evidence the browser can't see, and classifies the writes.
//
// `relationships`/`resource` may be omitted; the target is then fetched.
export function useGitOpsWriteGuard({
  target,
  writes,
  relationships,
  resource,
  ownership: resolved,
  relationshipsUnavailable = false,
  declaredManager,
  evidencePaths: requestedPaths,
  enabled = true,
}: {
  target: GitOpsWriteTarget
  writes: GitOpsWrite[]
  /** Paths to ask the server about, when wider than the writes being judged
   *  (every container a dialog lists, while judging the ones being changed). */
  evidencePaths?: string[]
  relationships?: Relationships
  resource?: unknown
  /** Pass when the host already resolved ownership for this target. */
  ownership?: ResolvedGitOpsOwner
  /** The host read the target but the server sent no relationships (its
   *  topology isn't built yet), so ownership is unknown rather than absent. */
  relationshipsUnavailable?: boolean
  /** A manager the server detected on its own (e.g. "Argo CD"). When the
   *  browser can't resolve an owner, ownership is unknown rather than absent. */
  declaredManager?: string | null
  enabled?: boolean
}): GitOpsWriteGuardState {
  const needsTarget = enabled && !resolved && relationships === undefined
  const targetQuery = useResource<any>(
    needsTarget ? kindToPluralWithGroup(target.kind, target.group) : '',
    target.namespace,
    target.name,
    target.group || undefined,
    // Keyed by API base: a host can point one QueryClient at several clusters
    // (Radar Hub's fleet Diagnose panel), and the shared resource keys aren't.
    { enabled: needsTarget, cacheScope: getApiBase() },
  )
  const ownRelationships = relationships ?? targetQuery.relationships
  const ownResolution = useResolvedGitOpsOwner({
    kind: kindToPluralWithGroup(target.kind, target.group),
    group: target.group || undefined,
    namespace: target.namespace,
    name: target.name,
    relationships: ownRelationships,
    resource: resource ?? targetQuery.data,
    enabled: enabled && !resolved,
    cacheScope: getApiBase(),
  })
  const ownership = resolved ?? ownResolution

  const targetLookupPending = needsTarget && targetQuery.isLoading
  const targetLookupFailed = needsTarget && targetQuery.isError
  const lookupPending = targetLookupPending || ownership.lookupPending
  const managed = Boolean(ownership.owner || ownership.helmOwner)

  const writesKey = JSON.stringify(writes)
  const requestedPathsKey = requestedPaths ? JSON.stringify(requestedPaths) : ''
  const paths = useMemo(
    () => (requestedPathsKey ? (JSON.parse(requestedPathsKey) as string[]) : gitOpsWriteEvidencePaths(JSON.parse(writesKey))),
    [requestedPathsKey, writesKey],
  )
  const ownerRef = useMemo(() => {
    if (ownership.owner) {
      return { ...gitOpsOwnerKindRef(ownership.owner), namespace: ownership.owner.namespace, name: ownership.owner.name }
    }
    if (ownership.helmOwner) {
      return { kind: 'HelmRelease', group: '', namespace: ownership.helmOwner.namespace, name: ownership.helmOwner.name }
    }
    return null
  }, [ownership.owner, ownership.helmOwner])
  // An Argo owner without a namespace is still asked about: the server finds
  // the Application whose status lists the object.
  const canFetchEvidence = managed && !lookupPending && !targetLookupFailed && !ownership.lookupError

  const evidenceFeature = useRadarFeature('gitopsWriteEvidence')
  const evidenceQuery = useQuery({
    // The API base identifies the cluster when a host switches it (Radar Hub).
    queryKey: ['gitops-write-evidence', getApiBase(), target.kind, target.group, target.namespace, target.name, paths, ownerRef, ...evidenceFeature.gatedKey],
    queryFn: () =>
      evidenceFeature.guard(() =>
        fetchGitOpsWriteEvidence({
          kind: target.kind,
          group: target.group,
          namespace: target.namespace,
          name: target.name,
          paths,
          owner: ownerRef ?? undefined,
        }),
      ),
    enabled: enabled && canFetchEvidence,
    staleTime: 0,
    retry: false,
  })

  const targetRelationshipsMissing = needsTarget && targetQuery.data != null && targetQuery.relationships == null
  const unresolvedDeclaredManager = Boolean(declaredManager) && !managed && !lookupPending
  const unknownOwner = "so it can't tell whether a GitOps tool or Helm manages it."
  const ownershipError = targetLookupFailed
    ? `Radar couldn't read this resource, ${unknownOwner}`
    : ownership.lookupError
      ? `Radar couldn't read the resource that owns this one, ${unknownOwner}`
      : relationshipsUnavailable || targetRelationshipsMissing || ownership.relationshipsUnavailable
        ? `Radar hasn't mapped this resource's owners yet, ${unknownOwner}`
        : unresolvedDeclaredManager
          ? `Its labels mark it as managed by ${declaredManager}, but Radar couldn't find the owner to check whether it would revert this change.`
          : null

  // React Query keeps earlier data after a failed refetch, and while the query
  // is disabled (an unreadable target or owner); an exemption it carried may no
  // longer hold.
  const evidence = evidenceQuery.isError || !canFetchEvidence ? undefined : evidenceQuery.data

  // Argo CD 3's tracking id omits the namespace of Applications in its own
  // namespace, and the browser's namespace view may hide them; take the
  // Application the server confirmed.
  const confirmedOwnership = useMemo<ResolvedGitOpsOwner>(() => {
    const owner = ownership.owner
    const namespace = evidence?.ownerTracksTarget ? evidence.owner?.namespace : undefined
    if (!owner || owner.namespace || !namespace) return ownership
    return { ...ownership, owner: { ...owner, namespace }, ownerVerified: true }
  }, [ownership, evidence])

  const guard = useMemo(
    () =>
      enabled
        ? evaluateGitOpsWriteGuard({
            target,
            owner: confirmedOwnership.owner,
            helmRelease: ownership.helmOwner,
            // A refetch counts as pending: an earlier verdict may no longer hold.
            ownerPending: lookupPending || (canFetchEvidence && (evidenceQuery.isPending || evidenceQuery.isFetching)),
            ownershipError,
            // The Application's status listing the object confirms a name match.
            ownerMatchedByName: ownership.ownerMatchedByName && !evidence?.ownerTracksTarget,
            evidence,
            evidenceError: evidenceQuery.error instanceof Error ? evidenceQuery.error.message : null,
            writes: JSON.parse(writesKey),
          })
        : undefined,
    // target fields are listed individually so an inline literal doesn't
    // re-evaluate every render.
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [
      enabled,
      target.kind,
      target.group,
      target.namespace,
      target.name,
      confirmedOwnership.owner,
      ownership.helmOwner,
      lookupPending,
      canFetchEvidence,
      evidenceQuery.isPending,
      evidenceQuery.isFetching,
      evidence,
      evidenceQuery.error,
      ownershipError,
      ownership.ownerMatchedByName,
      writesKey,
    ],
  )

  return { guard, ownership: confirmedOwnership, evidence }
}
