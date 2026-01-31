import { GitBranch, AlertTriangle, FolderGit, CheckCircle2 } from 'lucide-react'
import { clsx } from 'clsx'
import { Section, PropertyList, Property, ConditionsSection } from '../drawer-components'
import { formatAge } from '../resource-utils'
import { GitOpsStatusBadge, SyncCountdown } from '../../gitops'
import { fluxConditionsToGitOpsStatus, type FluxCondition } from '../../../types/gitops'

interface GitRepositoryRendererProps {
  data: any
}

export function GitRepositoryRenderer({ data }: GitRepositoryRendererProps) {
  const status = data.status || {}
  const spec = data.spec || {}
  const conditions = (status.conditions || []) as FluxCondition[]
  const artifact = status.artifact || {}

  // Convert to unified GitOps status
  const gitOpsStatus = fluxConditionsToGitOpsStatus(conditions, spec.suspend === true)

  // Problem detection
  const problems: Array<{ color: 'red' | 'yellow'; message: string }> = []

  if (gitOpsStatus.suspended) {
    problems.push({ color: 'yellow', message: 'GitRepository is suspended' })
  }

  if (gitOpsStatus.health === 'Degraded' && gitOpsStatus.message) {
    problems.push({ color: 'red', message: gitOpsStatus.message })
  }

  // Extract repository info
  const url = spec.url || ''
  const ref = spec.ref || {}
  const branch = ref.branch || ref.tag || ref.semver || ref.commit || 'default'

  return (
    <>
      {/* Problem alerts */}
      {problems.map((problem, i) => (
        <div
          key={i}
          className={clsx(
            'mb-4 p-3 border rounded-lg',
            problem.color === 'red'
              ? 'bg-red-500/10 border-red-500/30'
              : 'bg-yellow-500/10 border-yellow-500/30'
          )}
        >
          <div className="flex items-start gap-2">
            <AlertTriangle
              className={clsx(
                'w-4 h-4 mt-0.5 shrink-0',
                problem.color === 'red' ? 'text-red-400' : 'text-yellow-400'
              )}
            />
            <div className="flex-1 min-w-0">
              <div
                className={clsx(
                  'text-sm font-medium',
                  problem.color === 'red' ? 'text-red-400' : 'text-yellow-400'
                )}
              >
                {problem.color === 'red' ? 'Issue Detected' : 'Warning'}
              </div>
              <div
                className={clsx(
                  'text-xs mt-1',
                  problem.color === 'red' ? 'text-red-300/80' : 'text-yellow-300/80'
                )}
              >
                {problem.message}
              </div>
            </div>
          </div>
        </div>
      ))}

      {/* Status section */}
      <Section title="Status">
        <div className="space-y-3">
          <GitOpsStatusBadge status={gitOpsStatus} showHealth={false} />
          {spec.interval && (
            <SyncCountdown
              interval={spec.interval}
              lastSyncTime={status.lastHandledReconcileAt}
              suspended={gitOpsStatus.suspended}
            />
          )}
        </div>
      </Section>

      {/* Source section */}
      <Section title="Source" icon={FolderGit}>
        <PropertyList>
          <Property label="URL" value={url} />
          <Property
            label="Reference"
            value={
              <span className="flex items-center gap-1">
                <GitBranch className="w-3.5 h-3.5" />
                {branch}
              </span>
            }
          />
          {ref.branch && <Property label="Branch" value={ref.branch} />}
          {ref.tag && <Property label="Tag" value={ref.tag} />}
          {ref.semver && <Property label="Semver" value={ref.semver} />}
          {ref.commit && <Property label="Commit" value={ref.commit} />}
          {spec.secretRef?.name && (
            <Property label="Secret" value={spec.secretRef.name} />
          )}
          {spec.ignore && <Property label="Ignore" value={spec.ignore} />}
        </PropertyList>
      </Section>

      {/* Artifact section (last fetched source) */}
      {artifact.revision && (
        <Section title="Latest Artifact" icon={CheckCircle2}>
          <PropertyList>
            <Property label="Revision" value={artifact.revision} />
            <Property label="Digest" value={artifact.digest} />
            <Property
              label="Last Updated"
              value={artifact.lastUpdateTime ? formatAge(artifact.lastUpdateTime) : '-'}
            />
            {artifact.size && (
              <Property label="Size" value={formatBytes(artifact.size)} />
            )}
          </PropertyList>
        </Section>
      )}

      {/* Additional Status Info */}
      {status.observedGeneration !== undefined && (
        <Section title="Additional Info" defaultExpanded={false}>
          <PropertyList>
            <Property label="Observed Generation" value={status.observedGeneration} />
            {status.lastHandledReconcileAt && (
              <Property
                label="Last Reconciled"
                value={formatAge(status.lastHandledReconcileAt)}
              />
            )}
          </PropertyList>
        </Section>
      )}

      {/* Conditions section */}
      <ConditionsSection conditions={conditions} />
    </>
  )
}

function formatBytes(bytes: number): string {
  if (bytes === 0) return '0 B'
  const k = 1024
  const sizes = ['B', 'KB', 'MB', 'GB']
  const i = Math.floor(Math.log(bytes) / Math.log(k))
  return `${parseFloat((bytes / Math.pow(k, i)).toFixed(1))} ${sizes[i]}`
}
