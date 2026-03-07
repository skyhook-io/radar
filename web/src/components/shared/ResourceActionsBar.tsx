import {
  ResourceActionsBar as BaseResourceActionsBar,
} from '@skyhook-io/k8s-ui'
import {
  useDeleteResource,
  useTriggerCronJob,
  useSuspendCronJob,
  useResumeCronJob,
  useRestartWorkload,
  useWorkloadRevisions,
  useRollbackWorkload,
  useFluxReconcile,
  useFluxSyncWithSource,
  useFluxSuspend,
  useFluxResume,
  useArgoSync,
  useArgoRefresh,
  useArgoSuspend,
  useArgoResume,
} from '../../api/client'
import type { SelectedResource } from '../../types'
import { useOpenTerminal, useOpenLogs, useOpenWorkloadLogs } from '../dock'
import { PortForwardButton } from '../portforward/PortForwardButton'
import { useCanExec, useCanViewLogs, useCanPortForward } from '../../contexts/CapabilitiesContext'
import { useToast } from '../ui/Toast'

interface ResourceActionsBarProps {
  resource: SelectedResource
  data: any
  onClose?: () => void
  hideLogs?: boolean
  showYaml?: boolean
  onToggleYaml?: () => void
}

export function ResourceActionsBar({ resource, data, onClose, hideLogs, showYaml, onToggleYaml }: ResourceActionsBarProps) {
  const { showCopied } = useToast()
  const openTerminal = useOpenTerminal()
  const openLogs = useOpenLogs()
  const openWorkloadLogs = useOpenWorkloadLogs()

  const canExec = useCanExec()
  const canViewLogs = useCanViewLogs()
  const canPortForward = useCanPortForward()

  const deleteMutation = useDeleteResource()
  const triggerCronJobMutation = useTriggerCronJob()
  const suspendCronJobMutation = useSuspendCronJob()
  const resumeCronJobMutation = useResumeCronJob()
  const restartWorkloadMutation = useRestartWorkload()
  const rollbackMutation = useRollbackWorkload()

  const kind = resource.kind.toLowerCase()
  const isRollbackKind = ['deployments', 'statefulsets', 'daemonsets'].includes(kind)
  const { data: revisionsList, isLoading: revisionsLoading, error: revisionsError } = useWorkloadRevisions(kind, resource.namespace, resource.name, isRollbackKind)

  const fluxReconcileMutation = useFluxReconcile()
  const fluxSyncWithSourceMutation = useFluxSyncWithSource()
  const fluxSuspendMutation = useFluxSuspend()
  const fluxResumeMutation = useFluxResume()

  const argoSyncMutation = useArgoSync()
  const argoRefreshMutation = useArgoRefresh()
  const argoSuspendMutation = useArgoSuspend()
  const argoResumeMutation = useArgoResume()

  return (
    <BaseResourceActionsBar
      resource={resource}
      data={data}
      onClose={onClose}
      hideLogs={hideLogs}
      showYaml={showYaml}
      onToggleYaml={onToggleYaml}

      canExec={canExec}
      canViewLogs={canViewLogs}
      canPortForward={canPortForward}

      onOpenTerminal={openTerminal}
      onOpenLogs={openLogs}
      onOpenWorkloadLogs={openWorkloadLogs}
      onCopyCommand={(text, message, event) => showCopied(text, message, event)}

      renderPortForward={({ type, namespace, name, className }) => (
        <PortForwardButton type={type} namespace={namespace} name={name} className={className} />
      )}

      onDelete={(params, callbacks) => deleteMutation.mutate(params, { onSuccess: callbacks?.onSuccess })}
      isDeleting={deleteMutation.isPending}

      onRestart={(params) => restartWorkloadMutation.mutate(params)}
      isRestarting={restartWorkloadMutation.isPending}

      revisions={revisionsList}
      revisionsLoading={revisionsLoading}
      revisionsError={revisionsError ?? null}
      onRollback={(params, callbacks) => rollbackMutation.mutate(params, { onSuccess: callbacks?.onSuccess })}
      isRollingBack={rollbackMutation.isPending}

      onTriggerCronJob={(params) => triggerCronJobMutation.mutate(params)}
      isTriggeringCronJob={triggerCronJobMutation.isPending}
      onSuspendCronJob={(params) => suspendCronJobMutation.mutate(params)}
      isSuspendingCronJob={suspendCronJobMutation.isPending}
      onResumeCronJob={(params) => resumeCronJobMutation.mutate(params)}
      isResumingCronJob={resumeCronJobMutation.isPending}

      onFluxReconcile={(params) => fluxReconcileMutation.mutate(params)}
      isFluxReconciling={fluxReconcileMutation.isPending}
      onFluxSyncWithSource={(params) => fluxSyncWithSourceMutation.mutate(params)}
      isFluxSyncing={fluxSyncWithSourceMutation.isPending}
      onFluxSuspend={(params) => fluxSuspendMutation.mutate(params)}
      isFluxSuspending={fluxSuspendMutation.isPending}
      onFluxResume={(params) => fluxResumeMutation.mutate(params)}
      isFluxResuming={fluxResumeMutation.isPending}

      onArgoSync={(params) => argoSyncMutation.mutate(params)}
      isArgoSyncing={argoSyncMutation.isPending}
      onArgoRefresh={(params) => argoRefreshMutation.mutate(params)}
      isArgoRefreshing={argoRefreshMutation.isPending}
      onArgoSuspend={(params) => argoSuspendMutation.mutate(params)}
      isArgoSuspending={argoSuspendMutation.isPending}
      onArgoResume={(params) => argoResumeMutation.mutate(params)}
      isArgoResuming={argoResumeMutation.isPending}
    />
  )
}
