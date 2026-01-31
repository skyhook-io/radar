import { Database, AlertTriangle, CheckCircle2, Shield } from 'lucide-react'
import { clsx } from 'clsx'
import { Section, PropertyList, Property, ConditionsSection } from '../drawer-components'
import { formatAge } from '../resource-utils'
import { GitOpsStatusBadge, SyncCountdown } from '../../gitops'
import { fluxConditionsToGitOpsStatus, type FluxCondition } from '../../../types/gitops'

interface HelmRepositoryRendererProps {
  data: any
}

export function HelmRepositoryRenderer({ data }: HelmRepositoryRendererProps) {
  const status = data.status || {}
  const spec = data.spec || {}
  const conditions = (status.conditions || []) as FluxCondition[]
  const artifact = status.artifact || {}

  // Convert to unified GitOps status
  const gitOpsStatus = fluxConditionsToGitOpsStatus(conditions, spec.suspend === true)

  // Determine repository type
  const isOCI = spec.type === 'oci'

  // Problem detection
  const problems: Array<{ color: 'red' | 'yellow'; message: string }> = []

  if (gitOpsStatus.suspended) {
    problems.push({ color: 'yellow', message: 'HelmRepository is suspended' })
  }

  if (gitOpsStatus.health === 'Degraded' && gitOpsStatus.message) {
    problems.push({ color: 'red', message: gitOpsStatus.message })
  }

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

      {/* Repository section */}
      <Section title="Repository" icon={Database}>
        <PropertyList>
          <Property label="URL" value={spec.url} />
          <Property
            label="Type"
            value={
              <span className={clsx(
                'px-2 py-0.5 rounded text-xs font-medium',
                isOCI ? 'bg-purple-500/20 text-purple-400' : 'bg-blue-500/20 text-blue-400'
              )}>
                {isOCI ? 'OCI' : 'HTTP'}
              </span>
            }
          />
          {spec.provider && <Property label="Provider" value={spec.provider} />}
        </PropertyList>
      </Section>

      {/* Authentication section */}
      {(spec.secretRef?.name || spec.certSecretRef?.name || spec.passCredentials) && (
        <Section title="Authentication" icon={Shield} defaultExpanded={false}>
          <PropertyList>
            {spec.secretRef?.name && (
              <Property label="Credentials Secret" value={spec.secretRef.name} />
            )}
            {spec.certSecretRef?.name && (
              <Property label="TLS Secret" value={spec.certSecretRef.name} />
            )}
            {spec.passCredentials && (
              <Property label="Pass Credentials" value="Yes" />
            )}
            {spec.insecure && (
              <Property label="Insecure" value="Yes (TLS verification disabled)" />
            )}
          </PropertyList>
        </Section>
      )}

      {/* Artifact/Index section */}
      {artifact.revision && (
        <Section title="Index" icon={CheckCircle2}>
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

      {/* Additional Info */}
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
