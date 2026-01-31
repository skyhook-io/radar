import { GitBranch, FolderTree, Settings, Target, XCircle } from 'lucide-react'
import { clsx } from 'clsx'
import { Section, PropertyList, Property, ConditionsSection, ProblemAlerts } from '../drawer-components'
import { formatAge } from '../resource-utils'
import { GitOpsStatusBadge, ManagedResourcesList, SyncCountdown } from '../../gitops'
import {
  argoStatusToGitOpsStatus,
  parseArgoResources,
  type ArgoAppStatus,
  type ArgoResource,
} from '../../../types/gitops'
import { useArgoTerminate } from '../../../api/client'

interface ArgoApplicationRendererProps {
  data: any
}

export function ArgoApplicationRenderer({ data }: ArgoApplicationRendererProps) {
  const status = (data.status || {}) as ArgoAppStatus & {
    resources?: ArgoResource[]
    conditions?: Array<{ type: string; status: string; message?: string; lastTransitionTime?: string }>
  }
  const spec = data.spec || {}

  const namespace = data.metadata?.namespace || ''
  const name = data.metadata?.name || ''

  // Convert to unified GitOps status
  const gitOpsStatus = argoStatusToGitOpsStatus(status)

  // Parse managed resources from status.resources
  const managedResources = parseArgoResources(status.resources || [])

  // Terminate hook (for canceling in-progress syncs)
  const terminateMutation = useArgoTerminate()

  // Problem detection
  const problems: Array<{ color: 'red' | 'yellow'; message: string }> = []

  if (gitOpsStatus.suspended) {
    problems.push({ color: 'yellow', message: 'Application automated sync is disabled' })
  }

  if (gitOpsStatus.health === 'Degraded' && gitOpsStatus.message) {
    problems.push({ color: 'red', message: gitOpsStatus.message })
  }

  if (gitOpsStatus.sync === 'OutOfSync') {
    problems.push({ color: 'yellow', message: 'Application is out of sync with git' })
  }

  // Extract source info
  const source = spec.source || {}
  const destination = spec.destination || {}
  const syncPolicy = spec.syncPolicy || {}
  const operationState = status.operationState

  // Check if sync is in progress
  const isSyncing = operationState?.phase === 'Running'

  return (
    <>
      <ProblemAlerts problems={problems} />

      {/* Status section */}
      <Section title="Status">
        <div className="space-y-3">
          <div className="flex items-center justify-between">
            <GitOpsStatusBadge status={gitOpsStatus} />
            {/* Terminate button (only when syncing) */}
            {isSyncing && (
              <button
                onClick={() => terminateMutation.mutate({ namespace, name })}
                disabled={terminateMutation.isPending}
                className="flex items-center gap-1.5 px-2 py-1 rounded text-xs bg-red-500/20 text-red-400 hover:bg-red-500/30 transition-colors disabled:opacity-50"
                title="Terminate sync"
              >
                <XCircle className="w-3.5 h-3.5" />
                {terminateMutation.isPending ? 'Terminating...' : 'Terminate'}
              </button>
            )}
          </div>

          {/* Sync countdown (only if automated sync is enabled) */}
          {syncPolicy.automated && (
            <SyncCountdown
              interval="5m" // ArgoCD default
              lastSyncTime={status.reconciledAt}
              suspended={gitOpsStatus.suspended}
            />
          )}
        </div>
      </Section>

      {/* Source section */}
      <Section title="Source" icon={FolderTree}>
        <PropertyList>
          <Property label="Repository" value={source.repoURL} />
          {source.path && <Property label="Path" value={source.path} />}
          {source.targetRevision && (
            <Property
              label="Target Revision"
              value={
                <span className="flex items-center gap-1">
                  <GitBranch className="w-3.5 h-3.5" />
                  {source.targetRevision}
                </span>
              }
            />
          )}
          {source.chart && <Property label="Helm Chart" value={source.chart} />}
          {source.helm?.valueFiles && source.helm.valueFiles.length > 0 && (
            <Property label="Value Files" value={source.helm.valueFiles.join(', ')} />
          )}
          {source.kustomize?.namePrefix && (
            <Property label="Kustomize Prefix" value={source.kustomize.namePrefix} />
          )}
        </PropertyList>
      </Section>

      {/* Destination section */}
      <Section title="Destination" icon={Target}>
        <PropertyList>
          <Property label="Server" value={destination.server || destination.name || '-'} />
          <Property label="Namespace" value={destination.namespace || 'default'} />
        </PropertyList>
      </Section>

      {/* Sync Policy section */}
      <Section title="Sync Policy" icon={Settings}>
        <PropertyList>
          <Property
            label="Automated Sync"
            value={
              <span
                className={clsx(
                  'px-2 py-0.5 rounded text-xs font-medium',
                  syncPolicy.automated ? 'bg-green-500/20 text-green-400' : 'bg-gray-500/20 text-gray-400'
                )}
              >
                {syncPolicy.automated ? 'Enabled' : 'Disabled'}
              </span>
            }
          />
          {syncPolicy.automated && (
            <>
              <Property
                label="Self Heal"
                value={syncPolicy.automated.selfHeal ? 'Yes' : 'No'}
              />
              <Property
                label="Prune"
                value={syncPolicy.automated.prune ? 'Yes' : 'No'}
              />
            </>
          )}
          {syncPolicy.retry && (
            <>
              <Property label="Retry Limit" value={syncPolicy.retry.limit} />
              {syncPolicy.retry.backoff && (
                <Property label="Backoff Duration" value={syncPolicy.retry.backoff.duration} />
              )}
            </>
          )}
          {syncPolicy.syncOptions && syncPolicy.syncOptions.length > 0 && (
            <Property label="Sync Options" value={syncPolicy.syncOptions.join(', ')} />
          )}
        </PropertyList>
      </Section>

      {/* Operation State (current/last sync) */}
      {operationState && (
        <Section title="Last Operation" defaultExpanded={operationState.phase === 'Running'}>
          <PropertyList>
            <Property
              label="Phase"
              value={
                <span
                  className={clsx(
                    'px-2 py-0.5 rounded text-xs font-medium',
                    operationState.phase === 'Succeeded'
                      ? 'bg-green-500/20 text-green-400'
                      : operationState.phase === 'Running'
                      ? 'bg-blue-500/20 text-blue-400'
                      : operationState.phase === 'Failed' || operationState.phase === 'Error'
                      ? 'bg-red-500/20 text-red-400'
                      : 'bg-gray-500/20 text-gray-400'
                  )}
                >
                  {operationState.phase}
                </span>
              }
            />
            {operationState.message && (
              <Property label="Message" value={operationState.message} />
            )}
            {operationState.finishedAt && (
              <Property label="Finished" value={formatAge(operationState.finishedAt)} />
            )}
            {operationState.syncResult?.revision && (
              <Property label="Revision" value={operationState.syncResult.revision} />
            )}
          </PropertyList>
        </Section>
      )}

      {/* Managed Resources */}
      {managedResources.length > 0 && (
        <ManagedResourcesList
          resources={managedResources}
          title={`Managed Resources (${managedResources.length})`}
        />
      )}

      {/* Revision Info */}
      {(status.sync?.revision || status.reconciledAt) && (
        <Section title="Revision Info" defaultExpanded={false}>
          <PropertyList>
            {status.sync?.revision && (
              <Property label="Current Revision" value={status.sync.revision} />
            )}
            {status.reconciledAt && (
              <Property label="Last Reconciled" value={formatAge(status.reconciledAt)} />
            )}
          </PropertyList>
        </Section>
      )}

      {/* Conditions section */}
      {status.conditions && status.conditions.length > 0 && (
        <ConditionsSection conditions={status.conditions} />
      )}
    </>
  )
}
