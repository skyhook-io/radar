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
  enabled = true,
}: {
  target: GitOpsWriteTarget
  writes: GitOpsWrite[]
  relationships?: Relationships
  resource?: unknown
  /** Pass when the host already resolved ownership for this target. */
  ownership?: ResolvedGitOpsOwner
  enabled?: boolean
}): GitOpsWriteGuardState {
  const needsTarget = enabled && !resolved && relationships === undefined
  const targetQuery = useResource<any>(
    needsTarget ? kindToPluralWithGroup(target.kind, target.group) : '',
    target.namespace,
    target.name,
    target.group || undefined,
    { enabled: needsTarget },
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
  })
  const ownership = resolved ?? ownResolution

  const targetLookupPending = needsTarget && targetQuery.isLoading
  const targetLookupFailed = needsTarget && targetQuery.isError
  const lookupPending = targetLookupPending || ownership.lookupPending
  const managed = Boolean(ownership.owner || ownership.helmOwner)

  const writesKey = JSON.stringify(writes)
  const paths = useMemo(() => gitOpsWriteEvidencePaths(JSON.parse(writesKey)), [writesKey])
  const ownerRef = useMemo(() => {
    if (ownership.owner) {
      return { ...gitOpsOwnerKindRef(ownership.owner), namespace: ownership.owner.namespace, name: ownership.owner.name }
    }
    if (ownership.helmOwner) {
      return { kind: 'HelmRelease', group: '', namespace: ownership.helmOwner.namespace, name: ownership.helmOwner.name }
    }
    return null
  }, [ownership.owner, ownership.helmOwner])

  const evidenceFeature = useRadarFeature('gitopsWriteEvidence')
  const evidenceQuery = useQuery({
    queryKey: ['gitops-write-evidence', target.kind, target.group, target.namespace, target.name, paths, ownerRef, ...evidenceFeature.gatedKey],
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
    enabled: enabled && managed && !lookupPending,
    staleTime: 0,
    retry: false,
  })

  const ownershipError = targetLookupFailed
    ? "Radar couldn't read the resource"
    : ownership.lookupError
      ? "Radar couldn't read the resource's parent workload"
      : null

  const guard = useMemo(
    () =>
      enabled
        ? evaluateGitOpsWriteGuard({
            target,
            owner: ownership.owner,
            helmRelease: ownership.helmOwner,
            ownerPending: lookupPending || (managed && evidenceQuery.isPending),
            ownershipError,
            evidence: evidenceQuery.data,
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
      ownership.owner,
      ownership.helmOwner,
      lookupPending,
      managed,
      evidenceQuery.isPending,
      evidenceQuery.data,
      evidenceQuery.error,
      ownershipError,
      writesKey,
    ],
  )

  return { guard, ownership, evidence: evidenceQuery.data }
}
