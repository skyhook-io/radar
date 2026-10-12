import { Package, CheckCircle2 } from 'lucide-react'
import { Section, PropertyList, Property, ConditionsSection, ProblemAlerts } from '../../ui/drawer-components'
import { formatBytes } from '../../../utils/format'
import { GitOpsStatusBadge, SyncCountdown } from '../../gitops'
import { fluxConditionsToGitOpsStatus, type FluxCondition } from '../../../types/gitops'
import { TimeValue } from '../../ui/ScheduleValue'

interface OCIRepositoryRendererProps {
  data: any
}

export function OCIRepositoryRenderer({ data }: OCIRepositoryRendererProps) {
  const status = data.status || {}
  const spec = data.spec || {}
  const conditions = (status.conditions || []) as FluxCondition[]
  const artifact = status.artifact || {}

  // Convert to unified GitOps status
  const gitOpsStatus = fluxConditionsToGitOpsStatus(conditions, spec.suspend === true, {
    generation: data.metadata?.generation,
    observedGeneration: status.observedGeneration,
  })

  // Problem detection
  const problems: Array<{ color: 'red' | 'yellow'; message: string }> = []

  if (gitOpsStatus.suspended) {
    problems.push({ color: 'yellow', message: 'OCIRepository is suspended' })
  }

  if (gitOpsStatus.health === 'Degraded' && gitOpsStatus.message) {
    problems.push({ color: 'red', message: gitOpsStatus.message })
  }

  // Extract repository info
  const url = spec.url || ''
  const ref = spec.ref || {}

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

      {/* Source section */}
      <Section title="Source" icon={Package}>
        <PropertyList>
          <Property label="URL" value={url} />
          {ref.tag && <Property label="Tag" value={ref.tag} />}
          {ref.semver && <Property label="Semver" value={ref.semver} />}
          {ref.digest && <Property label="Digest" value={ref.digest} />}
          {spec.provider && <Property label="Provider" value={spec.provider} />}
          {spec.secretRef?.name && (
            <Property label="Secret" value={spec.secretRef.name} />
          )}
          {spec.serviceAccountName && (
            <Property label="Service Account" value={spec.serviceAccountName} />
          )}
          {spec.insecure && <Property label="Insecure" value="Yes" />}
        </PropertyList>
      </Section>

      {/* Artifact section */}
      {artifact.revision && (
        <Section title="Latest Artifact" icon={CheckCircle2}>
          <PropertyList>
            <Property label="Revision" value={artifact.revision} />
            <Property label="Digest" value={artifact.digest} />
            <Property
              label="Last Updated"
              value={<TimeValue timestamp={artifact.lastUpdateTime} />}
            />
            {artifact.size && (
              <Property label="Size" value={formatBytes(artifact.size)} />
            )}
            {artifact.metadata && Object.keys(artifact.metadata).length > 0 && (
              <Property
                label="Metadata"
                value={Object.entries(artifact.metadata).map(([k, v]) => `${k}=${v}`).join(', ')}
              />
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
                value={<TimeValue timestamp={status.lastHandledReconcileAt} />}
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
