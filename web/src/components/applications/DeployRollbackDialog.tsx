import { useCallback } from 'react'
import { useNavigate, useSearchParams } from 'react-router-dom'
import {
  ConfirmDialog,
  gitOpsRouteForOwner,
  isRolloutKind,
  type AppDeployRow,
  type GitOpsOwnerRef,
  type HelmOwnerRef,
} from '@skyhook-io/k8s-ui'
import { useRollbackWorkload, useRolloutCapabilities } from '../../api/client'
import { useRollbackWriteGuard } from '../../hooks/useRollbackWriteGuard'
import { kindToPluralWithGroup } from '../../utils/navigation'

export function DeployRollbackDialog({ row, onClose }: { row: AppDeployRow; onClose: () => void }) {
  const navigate = useNavigate()
  const [searchParams] = useSearchParams()
  const { kind, namespace, name, group } = row.workload
  const rollback = useRollbackWorkload()
  const isRollout = isRolloutKind(kind) && group === 'argoproj.io'
  const { data: rolloutCapabilities } = useRolloutCapabilities(namespace, name, isRollout)
  const rolloutRefused = isRollout && rolloutCapabilities?.rollback === false

  const search = useCallback(() => {
    const params = new URLSearchParams()
    const namespaces = searchParams.get('namespaces')
    if (namespaces) params.set('namespaces', namespaces)
    return params
  }, [searchParams])

  const { warning, satisfied } = useRollbackWriteGuard({
    target: { kind, group: group ?? '', namespace, name },
    enabled: true,
    onOpenOwner: (owner: GitOpsOwnerRef) =>
      navigate({ pathname: gitOpsRouteForOwner(owner), search: search().toString() }),
    onOpenHelmRelease: (release: HelmOwnerRef) => {
      const params = search()
      params.set('release', `${release.namespace}/${release.name}`)
      navigate({ pathname: '/helm', search: params.toString() })
    },
  })

  return (
    <ConfirmDialog
      open
      onClose={onClose}
      onConfirm={() =>
        rollback.mutate(
          {
            kind: kindToPluralWithGroup(kind, group ?? '').toLowerCase(),
            namespace,
            name,
            revision: row.revision!,
          },
          { onSuccess: onClose },
        )
      }
      title={`Roll back to revision #${row.revision}?`}
      message={`${kind} ${name} goes back to ${row.version ?? 'this revision'}. This starts a new rollout.`}
      details={rolloutRefused ? "You don't have permission to roll back this Rollout." : undefined}
      confirmLabel="Roll back"
      variant="warning"
      isLoading={rollback.isPending}
      confirmDisabled={!satisfied || rolloutRefused}
    >
      {warning}
    </ConfirmDialog>
  )
}
