import { useState, type ReactNode } from 'react'
import {
  GitOpsWriteWarning,
  ROLLBACK_WRITES,
  canConfirmGitOpsWrite,
  type GitOpsOwnerRef,
  type GitOpsWriteTarget,
  type HelmOwnerRef,
} from '@skyhook-io/k8s-ui'
import { useGitOpsWriteGuard } from './useGitOpsWriteGuard'
import type { ResolvedGitOpsOwner } from './useResolvedGitOpsOwner'

// The GitOps write guard for a rollback: a warning to show and whether confirming
// is allowed. Until `enabled`, nothing is read and confirming isn't blocked,
// because the caller only enables it when the rollback is being confirmed.
export function useRollbackWriteGuard({
  target,
  ownership,
  relationshipsUnavailable,
  enabled,
  onOpenOwner,
  onOpenHelmRelease,
}: {
  target: GitOpsWriteTarget
  /** Pass when the host already resolved ownership for this target. */
  ownership?: ResolvedGitOpsOwner
  relationshipsUnavailable?: boolean
  enabled: boolean
  onOpenOwner: (owner: GitOpsOwnerRef) => void
  onOpenHelmRelease: (ref: HelmOwnerRef) => void
}): { warning: ReactNode; satisfied: boolean } {
  const [acknowledged, setAcknowledged] = useState(false)
  const { guard, ownership: resolved } = useGitOpsWriteGuard({
    target,
    writes: ROLLBACK_WRITES,
    ownership,
    relationshipsUnavailable,
    enabled,
  })
  if (!enabled) return { warning: null, satisfied: true }
  if (!guard) return { warning: null, satisfied: false }
  const { owner, helmOwner, ownerVerified } = resolved
  return {
    warning: (
      <GitOpsWriteWarning
        guard={guard}
        acknowledged={acknowledged}
        onAcknowledgedChange={setAcknowledged}
        onOpenManager={onOpenOwner}
        onOpenOwner={
          owner
            ? ownerVerified
              ? () => onOpenOwner(owner)
              : undefined
            : helmOwner
              ? () => onOpenHelmRelease(helmOwner)
              : undefined
        }
      />
    ),
    satisfied: canConfirmGitOpsWrite(guard, acknowledged),
  }
}
