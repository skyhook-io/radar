import { Database, CheckCircle2, Shield } from 'lucide-react'
import { Section, PropertyList, Property, ConditionsSection, ProblemAlerts } from '../../ui/drawer-components'
import { Badge } from '../../ui/Badge'
import { formatAge } from '../resource-utils'
import { formatBytes } from '../../../utils/format'
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
      <ProblemAlerts problems={problems} />

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
              <Badge tone={isOCI ? 'accent2' : 'accent1'}>
                {isOCI ? 'OCI' : 'HTTP'}
              </Badge>
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
