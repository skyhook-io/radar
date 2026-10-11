import { GitBranch, FolderGit, CheckCircle2 } from 'lucide-react'
import { Section, PropertyList, Property, ConditionsSection, ProblemAlerts } from '../../ui/drawer-components'
import { formatBytes } from '../../../utils/format'
import { GitOpsStatusBadge, SyncCountdown } from '../../gitops'
import { fluxConditionsToGitOpsStatus, type FluxCondition } from '../../../types/gitops'
import { TimeValue } from '../../ui/ScheduleValue'

interface GitRepositoryRendererProps {
  data: any
}

export function GitRepositoryRenderer({ data }: GitRepositoryRendererProps) {
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
              value={<TimeValue timestamp={artifact.lastUpdateTime} />}
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
