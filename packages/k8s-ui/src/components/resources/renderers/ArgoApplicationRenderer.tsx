import { GitBranch, FolderTree, Settings, Target, XCircle, History, ListChecks, ExternalLink } from 'lucide-react'
import { clsx } from 'clsx'
import { Section, PropertyList, Property, ConditionsSection, ProblemAlerts } from '../../ui/drawer-components'
import { formatAge } from '../resource-utils'
import { GitOpsStatusBadge, ManagedResourcesList, SyncCountdown } from '../../gitops'
import {
  argoStatusToGitOpsStatus,
  parseArgoResources,
  type ArgoAppStatus,
  type ArgoResource,
} from '../../../types/gitops'
import { BADGE_INACTIVE } from '../../../utils/badge-colors'
import { buildRepoBrowseUrl, buildPathBrowseUrl } from '../../../utils/git-provider-urls'

const REPO_LINK_CLASS = 'text-blue-400 hover:text-blue-300 hover:underline break-all'

interface ArgoApplicationRendererProps {
  data: any
  onTerminate?: (params: { namespace: string; name: string }) => void
  isTerminating?: boolean
}

function SourceProperties({ source }: { source: any }) {
  if (!source) return null
  // Helm chart sources point repoURL at a chart registry, not a browseable git repo.
  const isHelmSource = !!source.chart
  const repoHref = isHelmSource ? null : buildRepoBrowseUrl(source.repoURL)
  const pathHref = isHelmSource ? null : buildPathBrowseUrl(source.repoURL, source.path, source.targetRevision)
  return (
    <>
      <Property
        label="Repository"
        value={
          repoHref ? (
            <a
              href={repoHref}
              target="_blank"
              rel="noopener noreferrer"
              title={source.repoURL}
              className={`${REPO_LINK_CLASS} inline-flex items-center gap-1`}
            >
              {source.repoURL}
              <ExternalLink className="w-3 h-3 shrink-0" />
            </a>
          ) : (
            source.repoURL
          )
        }
      />
      {source.path && (
        <Property
          label="Path"
          value={
            pathHref ? (
              <a
                href={pathHref}
                target="_blank"
                rel="noopener noreferrer"
                title={source.path}
                className={REPO_LINK_CLASS}
              >
                {source.path}
              </a>
            ) : (
              source.path
            )
          }
        />
      )}
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
      {source.helm?.parameters && source.helm.parameters.length > 0 && (
        <Property
          label="Helm Parameters"
          value={
            <div className="flex flex-wrap gap-1">
              {source.helm.parameters.filter(Boolean).map((p: { name: string; value: string }, i: number) => (
                <span key={i} className="badge-sm bg-theme-elevated text-theme-text-secondary font-mono">
                  {p.name ?? '?'}={p.value ?? ''}
                </span>
              ))}
            </div>
          }
        />
      )}
      {source.kustomize?.namePrefix && (
        <Property label="Kustomize Prefix" value={source.kustomize.namePrefix} />
      )}
    </>
  )
}

function isSyncResourceFailed(res: any): boolean {
  return res.status === 'SyncFailed' || res.hookPhase === 'Failed' || res.hookPhase === 'Error'
}

function getSyncResourceBadgeClass(status: string, hookPhase?: string): string {
  if (status === 'SyncFailed' || hookPhase === 'Failed' || hookPhase === 'Error') {
    return 'status-unhealthy'
  }
  if (status === 'Synced') return 'status-healthy'
  if (status === 'Pruned') return 'status-neutral'
  if (status === 'PruneSkipped') return 'status-degraded'
  return 'status-unknown'
}

export function ArgoApplicationRenderer({ data, onTerminate, isTerminating }: ArgoApplicationRendererProps) {
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

  // Extract source info — support both spec.source (single) and spec.sources (multi, ArgoCD 2.6+)
  const sources: any[] = (spec.sources && spec.sources.length > 0) ? spec.sources : (spec.source ? [spec.source] : [])
  const isMultiSource = Array.isArray(spec.sources) && spec.sources.length > 0
  const destination = spec.destination || {}
  const syncPolicy = spec.syncPolicy || {}
  const operationState = status.operationState

  // Check if sync is in progress
  const isSyncing = operationState?.phase === 'Running'

  // Extract sync result resources for per-resource failure details
  const syncResultResources: any[] = operationState?.syncResult?.resources || []
  const failedSyncResources = syncResultResources.filter(isSyncResourceFailed)
  const otherSyncResources = syncResultResources.filter((r: any) => !isSyncResourceFailed(r))
  const sortedSyncResources = [...failedSyncResources, ...otherSyncResources]

  return (
    <>
      <ProblemAlerts problems={problems} />

      {/* Status section */}
      <Section title="Status">
        <div className="space-y-3">
          <div className="flex items-center justify-between">
            <GitOpsStatusBadge status={gitOpsStatus} />
            {/* Terminate button (only when syncing and handler provided) */}
            {isSyncing && onTerminate && (
              <button
                onClick={() => onTerminate({ namespace, name })}
                disabled={isTerminating}
                className="flex items-center gap-1.5 px-2 py-1 rounded text-xs bg-red-500/20 text-red-400 hover:bg-red-500/30 transition-colors disabled:opacity-50"
                title="Terminate sync"
              >
                <XCircle className="w-3.5 h-3.5" />
                {isTerminating ? 'Terminating...' : 'Terminate'}
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

      {/* Source section — renders each source for multi-source apps */}
      {isMultiSource ? (
        <Section title={`Sources (${sources.length})`} icon={FolderTree}>
          <div className="space-y-3">
            {sources.map((src: any, idx: number) => (
              <div key={idx} className="card-inner">
                <div className="text-xs font-medium text-theme-text-tertiary mb-1.5">Source {idx + 1}</div>
                <PropertyList>
                  <SourceProperties source={src} />
                </PropertyList>
              </div>
            ))}
          </div>
        </Section>
      ) : sources.length > 0 ? (
        <Section title="Source" icon={FolderTree}>
          <PropertyList>
            <SourceProperties source={sources[0]} />
          </PropertyList>
        </Section>
      ) : null}

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
                  'badge',
                  syncPolicy.automated ? 'bg-green-500/20 text-green-400' : BADGE_INACTIVE
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
                    'badge',
                    operationState.phase === 'Succeeded'
                      ? 'bg-green-500/20 text-green-400'
                      : operationState.phase === 'Running'
                      ? 'bg-blue-500/20 text-blue-400'
                      : operationState.phase === 'Failed' || operationState.phase === 'Error'
                      ? 'bg-red-500/20 text-red-400'
                      : BADGE_INACTIVE
                  )}
                >
                  {operationState.phase}
                </span>
              }
            />
            {operationState.message && (
              <Property label="Message" value={operationState.message} />
            )}
            {operationState.startedAt && (
              <Property label="Started" value={formatAge(operationState.startedAt)} />
            )}
            {operationState.finishedAt && (
              <Property label="Finished" value={formatAge(operationState.finishedAt)} />
            )}
            {operationState.retryCount != null && (
              <Property
                label="Retries"
                value={
                  syncPolicy.retry?.limit
                    ? `${operationState.retryCount}/${syncPolicy.retry.limit}`
                    : String(operationState.retryCount)
                }
              />
            )}
            {operationState.syncResult?.revision && (
              <Property label="Revision" value={operationState.syncResult.revision} />
            )}
          </PropertyList>
        </Section>
      )}

      {/* Sync Result Resources — per-resource sync status with failure details */}
      {sortedSyncResources.length > 0 && (
        <Section
          title={`Sync Result Resources (${sortedSyncResources.length})`}
          icon={ListChecks}
          defaultExpanded={failedSyncResources.length > 0}
        >
          <div className="space-y-1.5">
            {sortedSyncResources.map((res: any, idx: number) => {
              const isFailed = isSyncResourceFailed(res)
              return (
                <div
                  key={`${res.kind}-${res.namespace}-${res.name}-${idx}`}
                  className={clsx(
                    'card-inner px-3 py-2',
                    isFailed && 'border-l-2 border-red-500'
                  )}
                >
                  <div className="flex items-center gap-2 text-sm">
                    <span className={clsx('badge badge-sm', getSyncResourceBadgeClass(res.status, res.hookPhase))}>
                      {res.status || res.hookPhase || 'Unknown'}
                    </span>
                    <span className="text-theme-text-primary">
                      {res.kind}/{res.name}
                    </span>
                    {res.namespace && (
                      <span className="text-theme-text-tertiary text-xs">({res.namespace})</span>
                    )}
                  </div>
                  {res.message && (
                    <div className={clsx(
                      'text-xs mt-1 break-all',
                      isFailed ? 'text-red-400' : 'text-theme-text-secondary'
                    )}>
                      {res.message}
                    </div>
                  )}
                  {res.hookPhase && res.hookPhase !== res.status && (
                    <div className="text-xs text-theme-text-tertiary mt-0.5">
                      Hook: {res.hookPhase}
                    </div>
                  )}
                </div>
              )
            })}
          </div>
        </Section>
      )}

      {/* Managed Resources */}
      {managedResources.length > 0 && (
        <ManagedResourcesList
          resources={managedResources}
          title={`Managed Resources (${managedResources.length})`}
        />
      )}

      {/* Revision History */}
      {status.history && status.history.length > 0 && (
        <Section title={`Revision History (${status.history.length})`} icon={History} defaultExpanded={false}>
          <div className="space-y-2 max-h-48 overflow-y-auto">
            {status.history
              .slice(0, 10)
              .map((entry, idx) => (
                <div
                  key={entry.id ?? idx}
                  className={clsx(
                    'p-2 rounded text-sm',
                    idx === 0
                      ? 'bg-green-500/10 border border-green-500/30'
                      : 'bg-theme-elevated/30'
                  )}
                >
                  <div className="flex items-center justify-between">
                    <span className="font-medium text-theme-text-primary font-mono text-xs">
                      {entry.revision
                        ? entry.revision.length > 12
                          ? entry.revision.slice(0, 12)
                          : entry.revision
                        : '-'}
                    </span>
                    {entry.deployedAt && (
                      <span className="text-xs text-theme-text-tertiary">
                        {formatAge(entry.deployedAt)}
                      </span>
                    )}
                  </div>
                  {entry.source?.path && (
                    <div className="text-xs text-theme-text-secondary mt-0.5 truncate" title={entry.source.path}>
                      {entry.source.path}
                    </div>
                  )}
                </div>
              ))}
          </div>
        </Section>
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
