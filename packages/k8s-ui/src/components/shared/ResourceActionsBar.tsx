import { useState, useRef } from 'react'
import {
  RefreshCw,
  Terminal,
  FileText,
  Trash2,
  Play,
  Pause,
  Ban,
  AlertTriangle,
  Box,
  ChevronDown,
  History,
  GitCompare,
  Code,
  FileCode2,
  X,
} from 'lucide-react'
import { createTwoFilesPatch } from 'diff'
import { clsx } from 'clsx'
import { ForceDeleteConfirmDialog, type CascadeDependent } from '../ui/ForceDeleteConfirmDialog'
import { ConfirmDialog } from '../ui/ConfirmDialog'
import { DialogPortal } from '../ui/DialogPortal'
import type { SelectedResource, WorkloadRevision } from '../../types'
import { formatKindName } from '../ui/drawer-components'

// ============================================================================
// ACTIONS BAR - Interactive buttons that change based on resource kind
// ============================================================================

interface ResourceActionsBarProps {
  resource: SelectedResource
  data: any
  onClose?: () => void
  hideLogs?: boolean
  showYaml?: boolean
  onToggleYaml?: () => void

  // Capabilities (injected by platform)
  canExec?: boolean
  canViewLogs?: boolean
  canPortForward?: boolean

  // Dock actions (injected by platform)
  onOpenTerminal?: (params: { namespace: string; podName: string; containerName: string; containers: string[] }) => void
  onOpenLogs?: (params: { namespace: string; podName: string; containers: string[]; containerName?: string }) => void
  onOpenWorkloadLogs?: (params: { namespace: string; workloadKind: string; workloadName: string }) => void
  onCopyCommand?: (text: string, message: string, event: React.MouseEvent) => void

  // Port forward render prop (injected by platform)
  renderPortForward?: (props: { type: 'pod' | 'service'; namespace: string; name: string; className?: string }) => React.ReactNode

  // Delete
  onDelete?: (params: { kind: string; namespace: string; name: string; force: boolean }, callbacks?: { onSuccess?: () => void; onError?: (err: unknown) => void }) => void
  isDeleting?: boolean
  cascadeDependents?: CascadeDependent[]
  cascadeLoading?: boolean

  // Workload restart
  onRestart?: (params: { kind: string; namespace: string; name: string }, callbacks?: { onSuccess?: () => void; onError?: (err: unknown) => void }) => void
  isRestarting?: boolean

  // Rollback
  revisions?: WorkloadRevision[]
  revisionsLoading?: boolean
  revisionsError?: Error | null
  onRollback?: (params: { kind: string; namespace: string; name: string; revision: number }, callbacks?: { onSuccess?: () => void; onError?: (err: unknown) => void }) => void
  isRollingBack?: boolean

  // CronJob actions
  onTriggerCronJob?: (params: { namespace: string; name: string }) => void
  isTriggeringCronJob?: boolean
  onSuspendCronJob?: (params: { namespace: string; name: string }) => void
  isSuspendingCronJob?: boolean
  onResumeCronJob?: (params: { namespace: string; name: string }) => void
  isResumingCronJob?: boolean

  // Flux actions
  onFluxReconcile?: (params: { kind: string; namespace: string; name: string }) => void
  isFluxReconciling?: boolean
  onFluxSyncWithSource?: (params: { kind: string; namespace: string; name: string }) => void
  isFluxSyncing?: boolean
  onFluxSuspend?: (params: { kind: string; namespace: string; name: string }) => void
  isFluxSuspending?: boolean
  onFluxResume?: (params: { kind: string; namespace: string; name: string }) => void
  isFluxResuming?: boolean

  // Argo actions
  onArgoSync?: (params: { namespace: string; name: string }) => void
  isArgoSyncing?: boolean
  onArgoRefresh?: (params: { namespace: string; name: string; hard: boolean }) => void
  isArgoRefreshing?: boolean
  onArgoSuspend?: (params: { namespace: string; name: string }) => void
  isArgoSuspending?: boolean
  onArgoResume?: (params: { namespace: string; name: string }) => void
  isArgoResuming?: boolean

  // Node debug shell
  onOpenNodeTerminal?: (params: { nodeName: string }) => void

  // Node operations (cordon/uncordon/drain)
  canNodeWrite?: boolean
  onCordonNode?: (params: { name: string }) => void
  isCordoningNode?: boolean
  onUncordonNode?: (params: { name: string }) => void
  isUncordoningNode?: boolean
  onDrainNode?: (params: { name: string; options?: { deleteEmptyDirData?: boolean; force?: boolean } }) => void
  isDrainingNode?: boolean
}

export function ResourceActionsBar({
  resource, data, onClose, hideLogs, showYaml, onToggleYaml,
  canExec, canViewLogs, canPortForward,
  onOpenTerminal, onOpenLogs: openLogs, onOpenWorkloadLogs: openWorkloadLogs, onCopyCommand,
  renderPortForward,
  onDelete, isDeleting, cascadeDependents, cascadeLoading,
  onRestart, isRestarting,
  revisions: revisionsList, revisionsLoading, revisionsError, onRollback, isRollingBack,
  onTriggerCronJob, isTriggeringCronJob,
  onSuspendCronJob, isSuspendingCronJob,
  onResumeCronJob, isResumingCronJob,
  onFluxReconcile, isFluxReconciling,
  onFluxSyncWithSource, isFluxSyncing,
  onFluxSuspend, isFluxSuspending,
  onFluxResume, isFluxResuming,
  onArgoSync, isArgoSyncing,
  onArgoRefresh, isArgoRefreshing,
  onArgoSuspend, isArgoSuspending,
  onArgoResume, isArgoResuming,
  onOpenNodeTerminal,
  canNodeWrite,
  onCordonNode, isCordoningNode,
  onUncordonNode, isUncordoningNode,
  onDrainNode, isDrainingNode,
}: ResourceActionsBarProps) {
  const kind = resource.kind.toLowerCase()

  // Delete confirmation state
  const [showDeleteConfirm, setShowDeleteConfirm] = useState(false)

  // Node operation confirmation state
  const [showCordonConfirm, setShowCordonConfirm] = useState(false)
  const [showDrainConfirm, setShowDrainConfirm] = useState(false)
  const [drainForce, setDrainForce] = useState(false)

  // Rollback dialog state
  const [showRevisions, setShowRevisions] = useState(false)
  const isRollbackKind = ['deployments', 'statefulsets', 'daemonsets'].includes(kind)
  const hasMultipleRevisions = (revisionsList?.length ?? 0) > 1

  function handleDeleteConfirm(force: boolean) {
    onDelete?.(
      { kind: resource.kind, namespace: resource.namespace, name: resource.name, force },
      {
        onSuccess: () => {
          setShowDeleteConfirm(false)
          onClose?.()
        },
      }
    )
  }

  const isRunning = kind === 'pods' ? data?.status?.phase === 'Running' : true
  const containers = data?.spec?.containers?.map((c: any) => c.name) || []

  const handleOpenTerminal = () => {
    if (resource.namespace && resource.name && containers.length > 0) {
      onOpenTerminal?.({
        namespace: resource.namespace,
        podName: resource.name,
        containerName: containers[0],
        containers,
      })
    }
  }

  const handleOpenLogs = (containerName?: string) => {
    if (resource.namespace && resource.name && containers.length > 0) {
      openLogs?.({
        namespace: resource.namespace,
        podName: resource.name,
        containers,
        containerName,
      })
    }
  }

  const [showLogsMenu, setShowLogsMenu] = useState(false)
  const logsMenuTimeout = useRef<ReturnType<typeof setTimeout> | null>(null)

  const handleLogsMouseEnter = () => {
    if (logsMenuTimeout.current) clearTimeout(logsMenuTimeout.current)
    if (containers.length > 1) setShowLogsMenu(true)
  }
  const handleLogsMouseLeave = () => {
    logsMenuTimeout.current = setTimeout(() => setShowLogsMenu(false), 150)
  }

  return (
    <div className="flex items-center gap-2 px-4 py-2 flex-wrap">
      {/* Kind-specific actions (left) */}
      {kind === 'pods' && (
        <>
          {isRunning && canExec && onOpenTerminal && (
            <button
              onClick={handleOpenTerminal}
              className="flex items-center gap-1.5 px-3 py-1.5 text-xs font-medium btn-brand rounded-lg"
            >
              <Terminal className="w-3.5 h-3.5" />
              Terminal
            </button>
          )}
          {canViewLogs && !hideLogs && openLogs && (
            <div
              className="relative"
              onMouseEnter={handleLogsMouseEnter}
              onMouseLeave={handleLogsMouseLeave}
            >
              <button
                onClick={() => handleOpenLogs(containers.length === 1 ? containers[0] : undefined)}
                className="flex items-center gap-1.5 px-3 py-1.5 text-xs font-medium btn-brand-muted rounded-lg"
              >
                <FileText className="w-3.5 h-3.5" />
                Logs
                {containers.length > 1 && <ChevronDown className="w-3 h-3 ml-0.5" />}
              </button>
              {showLogsMenu && containers.length > 1 && (
                <div className="absolute top-full left-0 mt-1 min-w-[160px] py-1 bg-theme-surface border border-theme-border rounded-lg shadow-lg z-50">
                  {containers.map((container: string) => (
                    <button
                      key={container}
                      onClick={() => {
                        handleOpenLogs(container)
                        setShowLogsMenu(false)
                      }}
                      className="flex items-center gap-2 w-full px-3 py-1.5 text-xs text-theme-text-primary hover:bg-theme-hover transition-colors text-left"
                    >
                      <Box className="w-3 h-3 text-theme-text-tertiary shrink-0" />
                      <span className="truncate">{container}</span>
                    </button>
                  ))}
                </div>
              )}
            </div>
          )}
          {isRunning && canPortForward && resource.namespace && resource.name && renderPortForward && (
            renderPortForward({
              type: 'pod',
              namespace: resource.namespace,
              name: resource.name,
              className: '!px-3 !py-1.5 !text-xs',
            })
          )}
        </>
      )}

      {/* Node actions */}
      {kind === 'nodes' && (
        <>
          {canExec && onOpenNodeTerminal && (
            <button
              onClick={() => onOpenNodeTerminal({ nodeName: resource.name })}
              className="flex items-center gap-1.5 px-3 py-1.5 text-xs font-medium btn-brand rounded-lg"
            >
              <Terminal className="w-3.5 h-3.5" />
              Debug Shell
            </button>
          )}

          {canNodeWrite && (
            <>
              {data?.spec?.unschedulable ? (
                onUncordonNode && (
                  <button
                    onClick={() => onUncordonNode({ name: resource.name })}
                    disabled={isUncordoningNode}
                    className="flex items-center gap-1.5 px-3 py-1.5 text-xs font-medium text-white bg-emerald-600 hover:bg-emerald-700 rounded-lg transition-colors disabled:opacity-50"
                  >
                    <Play className={`w-3.5 h-3.5 ${isUncordoningNode ? 'animate-pulse' : ''}`} />
                    {isUncordoningNode ? 'Uncordoning...' : 'Uncordon'}
                  </button>
                )
              ) : (
                onCordonNode && (
                  <button
                    onClick={() => setShowCordonConfirm(true)}
                    disabled={isCordoningNode}
                    className="flex items-center gap-1.5 px-3 py-1.5 text-xs font-medium text-white bg-amber-600 hover:bg-amber-700 rounded-lg transition-colors disabled:opacity-50"
                  >
                    <Ban className={`w-3.5 h-3.5 ${isCordoningNode ? 'animate-pulse' : ''}`} />
                    {isCordoningNode ? 'Cordoning...' : 'Cordon'}
                  </button>
                )
              )}

              {onDrainNode && (
                <button
                  onClick={() => setShowDrainConfirm(true)}
                  disabled={isDrainingNode}
                  className="flex items-center gap-1.5 px-3 py-1.5 text-xs font-medium text-white bg-red-600 hover:bg-red-700 rounded-lg transition-colors disabled:opacity-50"
                >
                  <AlertTriangle className={`w-3.5 h-3.5 ${isDrainingNode ? 'animate-pulse' : ''}`} />
                  {isDrainingNode ? 'Draining...' : 'Drain'}
                </button>
              )}
            </>
          )}
        </>
      )}

      {/* Service actions */}
      {kind === 'services' && !data?.apiVersion?.includes('serving.knative.dev') && canPortForward && resource.namespace && resource.name && renderPortForward && (
        renderPortForward({
          type: 'service',
          namespace: resource.namespace,
          name: resource.name,
          className: '!px-3 !py-1.5 !text-xs',
        })
      )}

      {/* Workload actions - restart, rollback, and logs */}
      {['deployments', 'statefulsets', 'daemonsets', 'rollouts'].includes(kind) && (
        <>
          {onRestart && (
            <button
              onClick={() => onRestart({
                kind: resource.kind,
                namespace: resource.namespace,
                name: resource.name,
              })}
              disabled={isRestarting}
              className="flex items-center gap-1.5 px-3 py-1.5 text-xs font-medium btn-brand-muted rounded-lg"
            >
              <RefreshCw className={`w-3.5 h-3.5 ${isRestarting ? 'animate-spin' : ''}`} />
              {isRestarting ? 'Restarting...' : 'Restart'}
            </button>
          )}
          {isRollbackKind && onRollback && (
            <button
              onClick={() => setShowRevisions(true)}
              disabled={!hasMultipleRevisions}
              title={hasMultipleRevisions ? 'View revision history and rollback' : 'Only one revision exists'}
              className={clsx(
                "flex items-center gap-1.5 px-3 py-1.5 text-xs font-medium rounded-lg transition-colors",
                hasMultipleRevisions
                  ? "text-white bg-amber-600 hover:bg-amber-700"
                  : "text-theme-text-disabled bg-theme-elevated"
              )}
            >
              <History className="w-3.5 h-3.5" />
              Rollback
            </button>
          )}
          {canViewLogs && !hideLogs && ['deployments', 'statefulsets', 'daemonsets'].includes(kind) && openWorkloadLogs && (
            <button
              onClick={() => openWorkloadLogs({
                namespace: resource.namespace,
                workloadKind: kind,
                workloadName: resource.name,
              })}
              className="flex items-center gap-1.5 px-3 py-1.5 text-xs font-medium btn-brand-muted rounded-lg"
            >
              <FileText className="w-3.5 h-3.5" />
              Logs
            </button>
          )}
        </>
      )}

      {/* CronJob actions */}
      {kind === 'cronjobs' && (
        <>
          {onTriggerCronJob && (
            <button
              onClick={() => onTriggerCronJob({
                namespace: resource.namespace,
                name: resource.name,
              })}
              disabled={isTriggeringCronJob}
              className="flex items-center gap-1.5 px-3 py-1.5 text-xs font-medium btn-brand rounded-lg"
            >
              <Play className={`w-3.5 h-3.5 ${isTriggeringCronJob ? 'animate-pulse' : ''}`} />
              {isTriggeringCronJob ? 'Triggering...' : 'Trigger'}
            </button>
          )}
          {data?.spec?.suspend ? (
            onResumeCronJob && (
              <button
                onClick={() => onResumeCronJob({
                  namespace: resource.namespace,
                  name: resource.name,
                })}
                disabled={isResumingCronJob}
                className="flex items-center gap-1.5 px-3 py-1.5 text-xs font-medium text-white bg-green-600 hover:bg-green-700 rounded-lg transition-colors disabled:opacity-50"
              >
                <Play className="w-3.5 h-3.5" />
                {isResumingCronJob ? 'Resuming...' : 'Resume'}
              </button>
            )
          ) : (
            onSuspendCronJob && (
              <button
                onClick={() => onSuspendCronJob({
                  namespace: resource.namespace,
                  name: resource.name,
                })}
                disabled={isSuspendingCronJob}
                className="flex items-center gap-1.5 px-3 py-1.5 text-xs font-medium btn-brand-muted rounded-lg"
              >
                <Pause className="w-3.5 h-3.5" />
                {isSuspendingCronJob ? 'Suspending...' : 'Suspend'}
              </button>
            )
          )}
        </>
      )}

      {/* FluxCD actions */}
      {['gitrepositories', 'ocirepositories', 'helmrepositories', 'kustomizations', 'helmreleases', 'alerts'].includes(kind) && (
        <FluxActions
          resource={resource}
          data={data}
          onReconcile={onFluxReconcile}
          isReconciling={isFluxReconciling}
          onSyncWithSource={onFluxSyncWithSource}
          isSyncing={isFluxSyncing}
          onSuspend={onFluxSuspend}
          isSuspending={isFluxSuspending}
          onResume={onFluxResume}
          isResuming={isFluxResuming}
        />
      )}

      {/* ArgoCD actions */}
      {kind === 'applications' && (
        <ArgoActions
          resource={resource}
          data={data}
          onSync={onArgoSync}
          isSyncing={isArgoSyncing}
          onRefresh={onArgoRefresh}
          isRefreshing={isArgoRefreshing}
          onSuspend={onArgoSuspend}
          isSuspending={isArgoSuspending}
          onResume={onArgoResume}
          isResuming={isArgoResuming}
        />
      )}

      {/* Job logs */}
      {kind === 'jobs' && onCopyCommand && (
        <button
          onClick={(e) => onCopyCommand(
            `kubectl logs job/${resource.name} -n ${resource.namespace} -f`,
            'Logs command copied',
            e
          )}
          className="flex items-center gap-1.5 px-3 py-1.5 text-xs font-medium btn-brand-muted rounded-lg"
        >
          <FileText className="w-3.5 h-3.5" />
          Logs
        </button>
      )}

      {/* Spacer pushes universal actions to the right */}
      <div className="flex-1" />

      {/* Universal actions (right-aligned) */}
      {onToggleYaml && (
        <button
          onClick={onToggleYaml}
          className={clsx(
            'flex items-center gap-1.5 px-3 py-1.5 text-xs font-medium rounded-lg transition-colors',
            showYaml
              ? 'btn-brand'
              : 'text-theme-text-secondary hover:text-theme-text-primary border border-theme-border-light hover:bg-theme-elevated'
          )}
          title="Toggle YAML view"
        >
          <FileCode2 className="w-3.5 h-3.5" />
          YAML
        </button>
      )}

      {onDelete && (
        <button
          onClick={() => setShowDeleteConfirm(true)}
          className="flex items-center gap-1.5 px-3 py-1.5 text-xs font-medium text-red-400 hover:text-white hover:bg-red-600 border border-red-400/50 hover:border-red-600 rounded-lg transition-colors"
        >
          <Trash2 className="w-3.5 h-3.5" />
          Delete
        </button>
      )}

      <ForceDeleteConfirmDialog
        open={showDeleteConfirm}
        onClose={() => setShowDeleteConfirm(false)}
        onConfirm={handleDeleteConfirm}
        resourceName={resource.name}
        resourceKind={formatKindName(resource.kind)}
        namespaceName={resource.namespace}
        isLoading={isDeleting ?? false}
        cascadeDependents={cascadeDependents}
        cascadeLoading={cascadeLoading}
      />

      {/* Node cordon confirmation */}
      <ConfirmDialog
        open={showCordonConfirm}
        onClose={() => setShowCordonConfirm(false)}
        onConfirm={() => {
          onCordonNode?.({ name: resource.name })
          setShowCordonConfirm(false)
        }}
        title="Cordon Node"
        message={`Mark node "${resource.name}" as unschedulable? No new pods will be scheduled on this node.`}
        confirmLabel="Cordon"
        variant="warning"
        isLoading={isCordoningNode}
      />

      {/* Node drain confirmation */}
      <ConfirmDialog
        open={showDrainConfirm}
        onClose={() => {
          setShowDrainConfirm(false)
          setDrainForce(false)
        }}
        onConfirm={() => {
          onDrainNode?.({
            name: resource.name,
            options: { deleteEmptyDirData: true, force: drainForce || undefined },
          })
          setShowDrainConfirm(false)
          setDrainForce(false)
        }}
        title="Drain Node"
        message={`Cordon and evict all pods from node "${resource.name}"? DaemonSet pods will be skipped.`}
        confirmLabel={isDrainingNode ? 'Draining...' : 'Drain'}
        variant="danger"
        isLoading={isDrainingNode}
        isClosable
      >
        <div className="flex flex-col gap-2 text-sm text-theme-text-secondary">
          <label className="flex items-center gap-2 cursor-pointer">
            <input
              type="checkbox"
              checked={drainForce}
              onChange={(e) => setDrainForce(e.target.checked)}
              className="rounded border-theme-border"
            />
            Force (evict pods not managed by a controller)
          </label>
        </div>
      </ConfirmDialog>

      {showRevisions && ['deployments', 'statefulsets', 'daemonsets'].includes(kind) && (
        <RevisionHistoryDialog
          kind={resource.kind}
          namespace={resource.namespace}
          name={resource.name}
          open={showRevisions}
          onClose={() => setShowRevisions(false)}
          revisions={revisionsList}
          isLoading={revisionsLoading}
          error={revisionsError}
          onRollback={onRollback}
          isRollingBack={isRollingBack}
        />
      )}
    </div>
  )
}

// ============================================================================
// FLUX ACTIONS
// ============================================================================

function FluxActions({ resource, data, onReconcile, isReconciling, onSyncWithSource, isSyncing, onSuspend, isSuspending, onResume, isResuming }: {
  resource: SelectedResource; data: any
  onReconcile?: (params: { kind: string; namespace: string; name: string }) => void; isReconciling?: boolean
  onSyncWithSource?: (params: { kind: string; namespace: string; name: string }) => void; isSyncing?: boolean
  onSuspend?: (params: { kind: string; namespace: string; name: string }) => void; isSuspending?: boolean
  onResume?: (params: { kind: string; namespace: string; name: string }) => void; isResuming?: boolean
}) {
  const isSuspended = data?.spec?.suspend === true
  const hasSource = resource.kind === 'kustomizations' || resource.kind === 'helmreleases'

  return (
    <>
      {onReconcile && (
        <button
          onClick={() => onReconcile({
            kind: resource.kind,
            namespace: resource.namespace,
            name: resource.name,
          })}
          disabled={isReconciling || isSuspended}
          className="flex items-center gap-1.5 px-3 py-1.5 text-xs font-medium btn-brand rounded-lg"
          title={isSuspended ? 'Cannot reconcile while suspended' : 'Trigger reconciliation'}
        >
          <RefreshCw className={`w-3.5 h-3.5 ${isReconciling ? 'animate-spin' : ''}`} />
          {isReconciling ? 'Reconciling...' : 'Reconcile'}
        </button>
      )}

      {hasSource && onSyncWithSource && (
        <button
          onClick={() => onSyncWithSource({
            kind: resource.kind,
            namespace: resource.namespace,
            name: resource.name,
          })}
          disabled={isSyncing || isSuspended}
          className="flex items-center gap-1.5 px-3 py-1.5 text-xs font-medium text-white bg-purple-600 hover:bg-purple-700 rounded-lg transition-colors disabled:opacity-50"
          title={isSuspended ? 'Cannot sync while suspended' : 'Fetch latest from source, then reconcile'}
        >
          <RefreshCw className={`w-3.5 h-3.5 ${isSyncing ? 'animate-spin' : ''}`} />
          {isSyncing ? 'Syncing...' : 'Sync with Source'}
        </button>
      )}

      {isSuspended ? (
        onResume && (
          <button
            onClick={() => onResume({
              kind: resource.kind,
              namespace: resource.namespace,
              name: resource.name,
            })}
            disabled={isResuming}
            className="flex items-center gap-1.5 px-3 py-1.5 text-xs font-medium text-white bg-green-600 hover:bg-green-700 rounded-lg transition-colors disabled:opacity-50"
          >
            <Play className="w-3.5 h-3.5" />
            {isResuming ? 'Resuming...' : 'Resume'}
          </button>
        )
      ) : (
        onSuspend && (
          <button
            onClick={() => onSuspend({
              kind: resource.kind,
              namespace: resource.namespace,
              name: resource.name,
            })}
            disabled={isSuspending}
            className="flex items-center gap-1.5 px-3 py-1.5 text-xs font-medium btn-brand-muted rounded-lg"
          >
            <Pause className="w-3.5 h-3.5" />
            {isSuspending ? 'Suspending...' : 'Suspend'}
          </button>
        )
      )}
    </>
  )
}

// ============================================================================
// ARGO ACTIONS
// ============================================================================

function ArgoActions({ resource, data, onSync, isSyncing, onRefresh, isRefreshing, onSuspend, isSuspending, onResume, isResuming }: {
  resource: SelectedResource; data: any
  onSync?: (params: { namespace: string; name: string }) => void; isSyncing?: boolean
  onRefresh?: (params: { namespace: string; name: string; hard: boolean }) => void; isRefreshing?: boolean
  onSuspend?: (params: { namespace: string; name: string }) => void; isSuspending?: boolean
  onResume?: (params: { namespace: string; name: string }) => void; isResuming?: boolean
}) {
  const hasAutomatedSync = !!data?.spec?.syncPolicy?.automated

  return (
    <>
      {onSync && (
        <button
          onClick={() => onSync({
            namespace: resource.namespace,
            name: resource.name,
          })}
          disabled={isSyncing}
          className="flex items-center gap-1.5 px-3 py-1.5 text-xs font-medium btn-brand rounded-lg"
          title="Sync application"
        >
          <RefreshCw className={`w-3.5 h-3.5 ${isSyncing ? 'animate-spin' : ''}`} />
          {isSyncing ? 'Syncing...' : 'Sync'}
        </button>
      )}

      {onRefresh && (
        <button
          onClick={() => onRefresh({
            namespace: resource.namespace,
            name: resource.name,
            hard: false,
          })}
          disabled={isRefreshing}
          className="flex items-center gap-1.5 px-3 py-1.5 text-xs font-medium btn-brand-muted rounded-lg"
          title="Refresh (re-read from git)"
        >
          <RefreshCw className={`w-3.5 h-3.5 ${isRefreshing ? 'animate-spin' : ''}`} />
          {isRefreshing ? 'Refreshing...' : 'Refresh'}
        </button>
      )}

      {hasAutomatedSync ? (
        onSuspend && (
          <button
            onClick={() => onSuspend({
              namespace: resource.namespace,
              name: resource.name,
            })}
            disabled={isSuspending}
            className="flex items-center gap-1.5 px-3 py-1.5 text-xs font-medium btn-brand-muted rounded-lg"
          >
            <Pause className="w-3.5 h-3.5" />
            {isSuspending ? 'Suspending...' : 'Suspend'}
          </button>
        )
      ) : (
        onResume && (
          <button
            onClick={() => onResume({
              namespace: resource.namespace,
              name: resource.name,
            })}
            disabled={isResuming}
            className="flex items-center gap-1.5 px-3 py-1.5 text-xs font-medium text-white bg-green-600 hover:bg-green-700 rounded-lg transition-colors disabled:opacity-50"
          >
            <Play className="w-3.5 h-3.5" />
            {isResuming ? 'Enabling...' : 'Enable Auto-Sync'}
          </button>
        )
      )}
    </>
  )
}

// ============================================================================
// REVISION HISTORY DIALOG
// ============================================================================

export function RevisionHistoryDialog({ kind, namespace, name, open, onClose, revisions, isLoading, error, onRollback, isRollingBack }: {
  kind: string
  namespace: string
  name: string
  open: boolean
  onClose: () => void
  revisions?: WorkloadRevision[]
  isLoading?: boolean
  error?: Error | null
  onRollback?: (params: { kind: string; namespace: string; name: string; revision: number }, callbacks?: { onSuccess?: () => void; onError?: (err: unknown) => void }) => void
  isRollingBack?: boolean
}) {
  const [confirmRevision, setConfirmRevision] = useState<number | null>(null)
  const [diffRevision, setDiffRevision] = useState<number | null>(null)

  const handleClose = () => { setDiffRevision(null); onClose() }

  const currentRevision = revisions?.find(r => r.isCurrent)
  const selectedRevision = revisions?.find(r => r.number === diffRevision)
  const hasDiffData = currentRevision?.template && selectedRevision?.template

  function handleRollback(revision: number) {
    onRollback?.(
      { kind, namespace, name, revision },
      {
        onSuccess: () => {
          setConfirmRevision(null)
          setDiffRevision(null)
          onClose()
        },
      }
    )
  }

  function formatTimeAgo(dateStr: string): string {
    const date = new Date(dateStr)
    const now = new Date()
    const seconds = Math.floor((now.getTime() - date.getTime()) / 1000)
    if (seconds < 60) return `${seconds}s ago`
    const minutes = Math.floor(seconds / 60)
    if (minutes < 60) return `${minutes}m ago`
    const hours = Math.floor(minutes / 60)
    if (hours < 24) return `${hours}h ago`
    const days = Math.floor(hours / 24)
    return `${days}d ago`
  }

  function getImageTag(image: string): string {
    if (!image) return '-'
    const parts = image.split(':')
    if (parts.length > 1) return parts[parts.length - 1]
    const slashParts = image.split('/')
    return slashParts[slashParts.length - 1]
  }

  return (
    <DialogPortal
      open={open}
      onClose={handleClose}
      closable={!isRollingBack}
      className={clsx(
        "flex flex-col",
        diffRevision ? "max-w-5xl w-full max-h-[85vh]" : "max-w-lg w-full"
      )}
    >
      <div className="flex items-center justify-between p-4 border-b border-theme-border shrink-0">
        <div className="flex items-center gap-2">
          <History className="w-5 h-5 text-amber-500" />
          <h3 className="text-lg font-semibold text-theme-text-primary">Revision History</h3>
          {diffRevision && currentRevision && (
            <span className="badge ml-2 bg-blue-500/15 text-blue-400">
              <GitCompare className="w-3 h-3" />
              #{currentRevision.number} vs #{diffRevision}
            </span>
          )}
        </div>
        <button
          onClick={handleClose}
          disabled={isRollingBack}
          className="p-1 text-theme-text-secondary hover:text-theme-text-primary hover:bg-theme-elevated rounded disabled:opacity-50"
        >
          <X className="w-5 h-5" />
        </button>
      </div>

      <div className="flex-1 min-h-0 flex flex-col overflow-hidden">
        <div className={clsx("p-4 overflow-y-auto", diffRevision ? "max-h-48 shrink-0" : "max-h-80")}>
          {isLoading && (
            <div className="flex items-center justify-center py-8 text-theme-text-secondary text-sm">
              Loading revisions...
            </div>
          )}

          {error && (
            <div className="flex items-center justify-center py-8 text-red-400 text-sm">
              Failed to load revisions: {error instanceof Error ? error.message : 'Unknown error'}
            </div>
          )}

          {revisions && revisions.length === 0 && (
            <div className="flex items-center justify-center py-8 text-theme-text-secondary text-sm">
              No revisions found
            </div>
          )}

          {revisions && revisions.length > 0 && (
            <table className="w-full text-sm">
              <thead>
                <tr className="text-theme-text-secondary text-left text-xs uppercase tracking-wider">
                  <th className="pb-2 pr-3 font-medium">Rev</th>
                  <th className="pb-2 pr-3 font-medium">Image</th>
                  <th className="pb-2 pr-3 font-medium">Age</th>
                  <th className="pb-2 font-medium text-right">Action</th>
                </tr>
              </thead>
              <tbody>
                {revisions.map((rev: WorkloadRevision) => (
                  <tr
                    key={rev.number}
                    className={clsx(
                      "border-t border-theme-border/50",
                      diffRevision === rev.number && "bg-blue-500/10"
                    )}
                  >
                    <td className="py-2 pr-3 text-theme-text-primary font-mono">
                      #{rev.number}
                    </td>
                    <td className="py-2 pr-3 text-theme-text-secondary font-mono truncate max-w-[180px]" title={rev.image}>
                      {getImageTag(rev.image)}
                    </td>
                    <td className="py-2 pr-3 text-theme-text-secondary whitespace-nowrap">
                      {formatTimeAgo(rev.createdAt)}
                    </td>
                    <td className="py-2 text-right">
                      <div className="flex items-center gap-1 justify-end">
                        {!rev.isCurrent && rev.template && currentRevision?.template && (
                          <button
                            onClick={() => setDiffRevision(diffRevision === rev.number ? null : rev.number)}
                            className={clsx(
                              "px-2 py-0.5 text-xs font-medium rounded transition-colors flex items-center gap-1",
                              diffRevision === rev.number
                                ? "bg-blue-500/20 text-blue-400 border border-blue-400/50"
                                : "text-blue-400 hover:text-blue-300 hover:bg-blue-500/10 border border-transparent"
                            )}
                            title={`Compare with current revision`}
                          >
                            <GitCompare className="w-3 h-3" />
                            Diff
                          </button>
                        )}
                        {rev.isCurrent ? (
                          <span className="badge status-healthy">
                            Current
                          </span>
                        ) : confirmRevision === rev.number ? (
                          <>
                            <button
                              onClick={() => handleRollback(rev.number)}
                              disabled={isRollingBack}
                              className="px-2 py-0.5 text-xs font-medium text-white bg-amber-600 hover:bg-amber-700 rounded transition-colors disabled:opacity-50"
                            >
                              {isRollingBack ? 'Rolling back...' : 'Confirm'}
                            </button>
                            <button
                              onClick={() => setConfirmRevision(null)}
                              disabled={isRollingBack}
                              className="px-2 py-0.5 text-xs font-medium text-theme-text-secondary hover:text-theme-text-primary rounded transition-colors disabled:opacity-50"
                            >
                              Cancel
                            </button>
                          </>
                        ) : (
                          <button
                            onClick={() => setConfirmRevision(rev.number)}
                            className="px-2 py-0.5 text-xs font-medium text-amber-400 hover:text-white hover:bg-amber-600 border border-amber-400/50 hover:border-amber-600 rounded transition-colors"
                          >
                            Rollback
                          </button>
                        )}
                      </div>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </div>

        {diffRevision && hasDiffData && (
          <RevisionDiffView
            currentTemplate={currentRevision!.template!}
            selectedTemplate={selectedRevision!.template!}
            currentRevision={currentRevision!.number}
            selectedRevision={diffRevision}
          />
        )}
      </div>

      <div className="flex items-center justify-end p-4 border-t border-theme-border shrink-0">
        <button
          onClick={handleClose}
          disabled={isRollingBack}
          className="px-4 py-2 text-sm font-medium text-theme-text-secondary hover:text-theme-text-primary hover:bg-theme-elevated rounded-lg transition-colors disabled:opacity-50"
        >
          Close
        </button>
      </div>
    </DialogPortal>
  )
}

// ============================================================================
// REVISION DIFF VIEW
// ============================================================================

function stripAutoLabels(templateYaml: string): string {
  return templateYaml
    .split('\n')
    .filter(line => !line.match(/^\s+pod-template-hash:/))
    .join('\n')
}

function RevisionDiffView({ currentTemplate, selectedTemplate, currentRevision, selectedRevision }: {
  currentTemplate: string
  selectedTemplate: string
  currentRevision: number
  selectedRevision: number
}) {
  const [expanded, setExpanded] = useState(false)

  const cleanCurrent = stripAutoLabels(currentTemplate)
  const cleanSelected = stripAutoLabels(selectedTemplate)

  const patch = createTwoFilesPatch(
    `Revision #${currentRevision} (current)`,
    `Revision #${selectedRevision}`,
    cleanCurrent,
    cleanSelected,
    '', '',
    expanded ? { context: 999999 } : { context: 3 }
  )

  const lines = patch.split('\n')
  const diffLines = lines.filter(line =>
    !line.startsWith('===') && !line.startsWith('Index:')
  )

  const hasChanges = diffLines.some(l => (l.startsWith('+') && !l.startsWith('+++')) || (l.startsWith('-') && !l.startsWith('---')))

  return (
    <div className="border-t border-theme-border flex flex-col shrink-0">
      <div className="flex items-center justify-between px-4 py-2 bg-theme-elevated/50 text-xs text-theme-text-secondary shrink-0">
        <div className="flex items-center gap-4">
          <span className="flex items-center gap-1">
            <span className="w-3 h-3 bg-red-500/20 border border-red-500/50 rounded" /> Revision #{currentRevision} (current)
          </span>
          <span className="flex items-center gap-1">
            <span className="w-3 h-3 bg-green-500/20 border border-green-500/50 rounded" /> Revision #{selectedRevision}
          </span>
        </div>
        {hasChanges && (
          <button
            onClick={() => setExpanded(!expanded)}
            className="flex items-center gap-1 px-2 py-0.5 text-xs text-theme-text-secondary hover:text-theme-text-primary hover:bg-theme-elevated rounded transition-colors"
          >
            <Code className="w-3 h-3" />
            {expanded ? 'Show changes only' : 'Show full spec'}
          </button>
        )}
      </div>
      <div className="overflow-auto max-h-[400px]">
        {hasChanges ? (
          <pre className="text-xs font-mono p-0 m-0">
            {diffLines.map((line, index) => {
              const isAddition = line.startsWith('+') && !line.startsWith('+++')
              const isDeletion = line.startsWith('-') && !line.startsWith('---')
              const isHeader = line.startsWith('@@') || line.startsWith('---') || line.startsWith('+++')

              return (
                <div
                  key={index}
                  className={clsx(
                    'flex',
                    isAddition && 'bg-green-500/10',
                    isDeletion && 'bg-red-500/10',
                    isHeader && 'bg-blue-500/10'
                  )}
                >
                  <span className="w-10 shrink-0 text-right pr-2 py-0.5 text-theme-text-disabled select-none border-r border-theme-border/50">
                    {index + 1}
                  </span>
                  <span
                    className={clsx(
                      'flex-1 px-3 py-0.5 whitespace-pre',
                      isAddition && 'text-green-400',
                      isDeletion && 'text-red-400',
                      isHeader && 'text-blue-400 font-medium',
                      !isAddition && !isDeletion && !isHeader && 'text-theme-text-secondary'
                    )}
                  >
                    {line || ' '}
                  </span>
                </div>
              )
            })}
          </pre>
        ) : (
          <div className="flex flex-col items-center justify-center py-12 text-theme-text-tertiary">
            <GitCompare className="w-8 h-8 mb-2 text-theme-text-disabled" />
            <span className="text-sm">Templates are identical</span>
          </div>
        )}
      </div>
    </div>
  )
}
