import { Layers, FolderTree, GitBranch } from 'lucide-react'
import { Section, PropertyList, Property, ConditionsSection, ProblemAlerts, ResourceLink } from '../../ui/drawer-components'
import { GitOpsStatusBadge, ManagedResourcesList, SyncCountdown } from '../../gitops'
import { fluxConditionsToGitOpsStatus, parseFluxInventory, type FluxCondition } from '../../../types/gitops'
import { TimeValue } from '../../ui/ScheduleValue'

interface KustomizationRendererProps {
  data: any
  onNavigate?: (ref: { kind: string; namespace: string; name: string }) => void
}

export function KustomizationRenderer({ data, onNavigate }: KustomizationRendererProps) {
  const status = data.status || {}
  const spec = data.spec || {}
  const conditions = (status.conditions || []) as FluxCondition[]
  const inventoryEntries = status.inventory?.entries || []

  // Convert to unified GitOps status
  const gitOpsStatus = fluxConditionsToGitOpsStatus(conditions, spec.suspend === true, {
    generation: data.metadata?.generation,
    observedGeneration: status.observedGeneration,
  })

  // Parse inventory to managed resources
  const managedResources = parseFluxInventory(inventoryEntries)

  // Problem detection for alerts
  const problems: Array<{ color: 'red' | 'yellow'; message: string }> = []

  if (gitOpsStatus.suspended) {
    problems.push({ color: 'yellow', message: 'Kustomization is suspended' })
  }

  if (gitOpsStatus.health === 'Degraded' && gitOpsStatus.message) {
    problems.push({ color: 'red', message: gitOpsStatus.message })
  }

  const healthyCondition = conditions.find(c => c.type === 'Healthy')
  if (healthyCondition?.status === 'False' && healthyCondition.message) {
    problems.push({
      color: 'yellow',
      message: healthyCondition.message || 'Deployed resources are not healthy',
    })
  }

  // Revision mismatch detection — only flag when Ready=False to avoid false positives during active reconciliation
  const isReadyFalse = conditions.find((c: any) => c.type === 'Ready')?.status === 'False'
  if (
    isReadyFalse &&
    status.lastAppliedRevision &&
    status.lastAttemptedRevision &&
    status.lastAppliedRevision !== status.lastAttemptedRevision
  ) {
    problems.push({
      color: 'red',
      message: `Revision mismatch: attempted ${status.lastAttemptedRevision} but applied ${status.lastAppliedRevision} — the latest reconciliation failed.`,
    })
  }

  // Dependencies
  const dependsOn: Array<{ name: string; namespace?: string }> = spec.dependsOn || []

  // Source reference
  const sourceRef = spec.sourceRef || {}

  return (
    <>
      <ProblemAlerts problems={problems} />

      {/* Status section */}
      <Section title="Status">
        <div className="space-y-3">
          <GitOpsStatusBadge status={gitOpsStatus} />
          {spec.interval && (
            <SyncCountdown
              interval={spec.interval}
              lastSyncTime={status.lastHandledReconcileAt}
              suspended={gitOpsStatus.suspended}
            />
          )}
        </div>
      </Section>

      {/* Source Reference section */}
      <Section title="Source" icon={FolderTree}>
        <PropertyList>
          <Property label="Kind" value={sourceRef.kind} />
          <Property label="Name" value={sourceRef.name} />
          {sourceRef.namespace && (
            <Property label="Namespace" value={sourceRef.namespace} />
          )}
          <Property label="Path" value={spec.path || './'} />
        </PropertyList>
      </Section>

      {/* Configuration section */}
      <Section title="Configuration" icon={Layers}>
        <PropertyList>
          <Property
            label="Prune"
            value={spec.prune ? 'Enabled' : 'Disabled'}
          />
          <Property label="Target Namespace" value={spec.targetNamespace} />
          <Property label="Service Account" value={spec.serviceAccountName} />
          <Property label="Timeout" value={spec.timeout} />
          {spec.retryInterval && (
            <Property label="Retry Interval" value={spec.retryInterval} />
          )}
          {spec.force !== undefined && (
            <Property label="Force" value={spec.force ? 'Yes' : 'No'} />
          )}
          {spec.wait !== undefined && (
            <Property label="Wait" value={spec.wait ? 'Yes' : 'No'} />
          )}
        </PropertyList>
      </Section>

      {/* Dependencies */}
      {dependsOn.length > 0 && (
        <Section title={`Dependencies (${dependsOn.length})`} icon={GitBranch}>
          <div className="flex flex-wrap gap-1">
            {dependsOn.map((dep, idx) => (
              <ResourceLink
                key={idx}
                name={dep.name}
                kind="kustomizations"
                namespace={dep.namespace || data.metadata?.namespace || ''}
                onNavigate={onNavigate}
              />
            ))}
          </div>
        </Section>
      )}

      {/* Health checks */}
      {spec.healthChecks && spec.healthChecks.length > 0 && (
        <Section title={`Health Checks (${spec.healthChecks.length})`} defaultExpanded={false}>
          <div className="space-y-1">
            {spec.healthChecks.map((check: any, idx: number) => (
              <div
                key={idx}
                className="text-sm card-inner"
              >
                <span className="text-theme-text-tertiary">{check.kind}/</span>
                <span className="text-theme-text-primary">{check.name}</span>
                {check.namespace && (
                  <span className="text-theme-text-tertiary ml-1">
                    ({check.namespace})
                  </span>
                )}
              </div>
            ))}
          </div>
        </Section>
      )}

      {/* Managed Resources (Inventory) */}
      {managedResources.length > 0 && (
        <ManagedResourcesList
          resources={managedResources}
          title={`Inventory (${managedResources.length} resources)`}
        />
      )}

      {/* Revision info */}
      {(status.lastAppliedRevision || status.lastAttemptedRevision) && (
        <Section title="Revision" defaultExpanded={false}>
          <PropertyList>
            {status.lastAppliedRevision && (
              <Property label="Applied" value={status.lastAppliedRevision} />
            )}
            {status.lastAttemptedRevision && status.lastAttemptedRevision !== status.lastAppliedRevision && (
              <Property label="Attempted" value={status.lastAttemptedRevision} />
            )}
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
