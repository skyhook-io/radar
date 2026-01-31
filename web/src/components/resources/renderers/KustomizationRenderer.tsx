import { Layers, FolderTree } from 'lucide-react'
import { Section, PropertyList, Property, ConditionsSection, ProblemAlerts } from '../drawer-components'
import { formatAge } from '../resource-utils'
import { GitOpsStatusBadge, ManagedResourcesList, SyncCountdown } from '../../gitops'
import { fluxConditionsToGitOpsStatus, parseFluxInventory, type FluxCondition } from '../../../types/gitops'

interface KustomizationRendererProps {
  data: any
}

export function KustomizationRenderer({ data }: KustomizationRendererProps) {
  const status = data.status || {}
  const spec = data.spec || {}
  const conditions = (status.conditions || []) as FluxCondition[]
  const inventoryEntries = status.inventory?.entries || []

  // Convert to unified GitOps status
  const gitOpsStatus = fluxConditionsToGitOpsStatus(conditions, spec.suspend === true)

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

      {/* Health checks */}
      {spec.healthChecks && spec.healthChecks.length > 0 && (
        <Section title={`Health Checks (${spec.healthChecks.length})`} defaultExpanded={false}>
          <div className="space-y-1">
            {spec.healthChecks.map((check: any, idx: number) => (
              <div
                key={idx}
                className="text-sm px-2 py-1 bg-theme-elevated/30 rounded"
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
