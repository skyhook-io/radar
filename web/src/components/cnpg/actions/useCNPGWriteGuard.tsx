import { useState, type ReactNode } from 'react'
import { useNavigate } from 'react-router-dom'
import { GitOpsWriteWarning, canConfirmGitOpsWrite, gitOpsRouteForOwner, type GitOpsWrite } from '@skyhook-io/k8s-ui'
import { useGitOpsWriteGuard } from '../../../hooks/useGitOpsWriteGuard'

export type CNPGWriteScope =
  | { kind: 'status' }
  | { kind: 'metadata' | 'spec'; paths: string[] }
  | { kind: 'create-child' }
  | { kind: 'delete-operator-owned'; owner: string }

/**
 * The shared GitOps write guard for a CNPG action on `name` (a Cluster unless
 * `targetKind` says otherwise). Returns the warning block and whether the
 * dialog may confirm.
 */
export function useCNPGWriteGuard({
  namespace,
  name,
  scope,
  targetKind = 'Cluster',
}: {
  namespace: string
  name: string
  scope: CNPGWriteScope
  targetKind?: string
}): { node: ReactNode; satisfied: boolean } {
  const navigate = useNavigate()
  const [acked, setAcked] = useState(false)
  const operatorOwned = scope.kind === 'delete-operator-owned'
  const writes: GitOpsWrite[] = operatorOwned
    ? []
    : [{ scope: scope.kind, paths: 'paths' in scope ? scope.paths : undefined }]
  const { guard } = useGitOpsWriteGuard({
    target: { kind: targetKind, group: 'postgresql.cnpg.io', namespace, name },
    writes,
    enabled: !operatorOwned,
  })
  if (operatorOwned) {
    return {
      node: <div className="text-xs text-theme-text-secondary">{scope.owner} recreates this Pod; GitOps sync is not involved.</div>,
      satisfied: true,
    }
  }
  if (!guard) return { node: null, satisfied: false }
  const route = guard.owner ? gitOpsRouteForOwner(guard.owner) : null
  return {
    node: (
      <GitOpsWriteWarning
        guard={guard}
        acknowledged={acked}
        onAcknowledgedChange={setAcked}
        onOpenOwner={route ? () => navigate(route) : undefined}
      />
    ),
    satisfied: canConfirmGitOpsWrite(guard, acked),
  }
}
