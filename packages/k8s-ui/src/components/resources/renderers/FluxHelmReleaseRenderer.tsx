import type { ReactNode } from 'react'
import { Package, Settings, CheckCircle2, History, FileCode2, GitBranch } from 'lucide-react'
import { clsx } from 'clsx'
import { Section, PropertyList, Property, ConditionsSection, ProblemAlerts, ResourceLink } from '../../ui/drawer-components'
import { GitOpsStatusBadge, SyncCountdown } from '../../gitops'
import { fluxConditionsToGitOpsStatus, type FluxCondition } from '../../../types/gitops'
import { TimeValue } from '../../ui/ScheduleValue'

export interface FluxValuesReference {
  kind?: 'ConfigMap' | 'Secret'
  name: string
  optional?: boolean
  valuesKey?: string
  targetPath?: string
}

interface FluxHelmReleaseRendererProps {
  data: any
  onNavigate?: (ref: { kind: string; namespace: string; name: string }) => void
  renderValuesReference?: (reference: FluxValuesReference) => ReactNode
}

export function FluxHelmReleaseRenderer({ data, onNavigate, renderValuesReference }: FluxHelmReleaseRendererProps) {
  const status = data.status || {}
  const spec = data.spec || {}
  const conditions = (status.conditions || []) as FluxCondition[]
  const history = status.history || []

  // Convert to unified GitOps status
  const gitOpsStatus = fluxConditionsToGitOpsStatus(conditions, spec.suspend === true, {
    generation: data.metadata?.generation,
    observedGeneration: status.observedGeneration,
  })

  // Problem detection
  const problems: Array<{ color: 'red' | 'yellow'; message: string }> = []

  if (gitOpsStatus.suspended) {
    problems.push({ color: 'yellow', message: 'HelmRelease is suspended' })
  }

  if (gitOpsStatus.health === 'Degraded' && gitOpsStatus.message) {
    problems.push({ color: 'red', message: gitOpsStatus.message })
  }

  // Check for test failures
  const testFailCondition = conditions.find(
    (c) => c.type === 'TestSuccess' && c.status === 'False'
  )
  if (testFailCondition) {
    problems.push({
      color: 'yellow',
      message: testFailCondition.message || 'Helm tests failed',
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

  // Values info
  const valuesFrom: FluxValuesReference[] = spec.valuesFrom || []
  const values = spec.values || {}
  const valueKeys = Object.keys(values)

  // Dependencies
  const dependsOn: Array<{ name: string; namespace?: string }> = spec.dependsOn || []

  // Chart reference
  const chartRef = spec.chart?.spec || {}
  const sourceRef = chartRef.sourceRef || {}

  // Current release info
  const lastRelease = history.length > 0 ? history[0] : null

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

      {/* Chart section */}
      <Section title="Chart" icon={Package}>
        <PropertyList>
          <Property label="Chart" value={chartRef.chart} />
          <Property label="Version" value={chartRef.version || '*'} />
          <Property label="Source Kind" value={sourceRef.kind} />
          <Property label="Source Name" value={sourceRef.name} />
          {sourceRef.namespace && (
            <Property label="Source Namespace" value={sourceRef.namespace} />
          )}
          <Property
            label="Reconcile Strategy"
            value={chartRef.reconcileStrategy || 'ChartVersion'}
          />
        </PropertyList>
      </Section>

      {/* Release Configuration */}
      <Section title="Configuration" icon={Settings}>
        <PropertyList>
          <Property label="Release Name" value={spec.releaseName || data.metadata?.name} />
          <Property label="Target Namespace" value={spec.targetNamespace || data.metadata?.namespace} />
          {spec.timeout && <Property label="Timeout" value={spec.timeout} />}
          {spec.serviceAccountName && (
            <Property label="Service Account" value={spec.serviceAccountName} />
          )}
          {spec.maxHistory !== undefined && (
            <Property label="Max History" value={spec.maxHistory} />
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
                kind="helmreleases"
                namespace={dep.namespace || data.metadata?.namespace || ''}
                onNavigate={onNavigate}
              />
            ))}
          </div>
        </Section>
      )}

      {/* Values */}
      {(valuesFrom.length > 0 || valueKeys.length > 0) && (
        <Section title="Values" icon={FileCode2} defaultExpanded={valuesFrom.length > 0}>
          {valuesFrom.length > 0 && (
            <div className="mb-3">
              <div className="text-xs font-medium text-theme-text-secondary uppercase tracking-wider mb-1.5">
                Values From
              </div>
              <div className="space-y-1">
                {valuesFrom.map((ref, idx) => (
                  <div key={idx} className="card-inner text-sm flex items-center gap-2 flex-wrap">
                    <span className="badge-sm status-neutral">{ref.kind || 'ConfigMap'}</span>
                    {ref.optional && <span className="badge-sm status-neutral">Optional</span>}
                    {renderValuesReference ? renderValuesReference(ref) : (
                      <ResourceLink
                        name={ref.name}
                        kind={ref.kind === 'Secret' ? 'secrets' : 'configmaps'}
                        namespace={data.metadata?.namespace || ''}
                        onNavigate={onNavigate}
                      />
                    )}
                    {ref.valuesKey && (
                      <span className="text-theme-text-tertiary text-xs">
                        key: {ref.valuesKey}
                      </span>
                    )}
                    {ref.targetPath && (
                      <span className="text-theme-text-tertiary text-xs">
                        path: {ref.targetPath}
                      </span>
                    )}
                  </div>
                ))}
              </div>
            </div>
          )}
          {valueKeys.length > 0 && (
            <div>
              <div className="text-xs font-medium text-theme-text-secondary uppercase tracking-wider mb-1.5">
                Inline Overrides ({valueKeys.length} {valueKeys.length === 1 ? 'key' : 'keys'})
              </div>
              <Section title={`${valueKeys.length} value ${valueKeys.length === 1 ? 'override' : 'overrides'}`} defaultExpanded={false}>
                <pre className="text-xs text-theme-text-secondary font-mono bg-theme-elevated rounded-md p-2 overflow-x-auto max-h-48">
                  {JSON.stringify(values, null, 2)}
                </pre>
              </Section>
            </div>
          )}
        </Section>
      )}

      {/* Install/Upgrade settings */}
      {(spec.install || spec.upgrade) && (
        <Section title="Install/Upgrade Settings" defaultExpanded={false}>
          <PropertyList>
            {spec.install && (
              <>
                <Property
                  label="Create Namespace"
                  value={spec.install.createNamespace ? 'Yes' : 'No'}
                />
                <Property
                  label="Replace CRDs"
                  value={spec.install.crds || 'Skip'}
                />
                {spec.install.remediation && (
                  <Property
                    label="Install Retries"
                    value={spec.install.remediation.retries}
                  />
                )}
              </>
            )}
            {spec.upgrade && (
              <>
                <Property
                  label="Cleanup on Fail"
                  value={spec.upgrade.cleanupOnFail ? 'Yes' : 'No'}
                />
                <Property
                  label="Force Upgrade"
                  value={spec.upgrade.force ? 'Yes' : 'No'}
                />
                {spec.upgrade.remediation && (
                  <>
                    <Property
                      label="Upgrade Retries"
                      value={spec.upgrade.remediation.retries}
                    />
                    <Property
                      label="Rollback on Fail"
                      value={spec.upgrade.remediation.remediateLastFailure ? 'Yes' : 'No'}
                    />
                  </>
                )}
              </>
            )}
          </PropertyList>
        </Section>
      )}

      {/* Current Release */}
      {lastRelease && (
        <Section title="Current Release" icon={CheckCircle2}>
          <PropertyList>
            <Property label="Revision" value={lastRelease.revision} />
            <Property label="Chart Version" value={lastRelease.chartVersion} />
            <Property label="App Version" value={lastRelease.appVersion} />
            <Property label="Status" value={lastRelease.status} />
            {lastRelease.firstDeployed && (
              <Property
                label="First Deployed"
                value={<TimeValue timestamp={lastRelease.firstDeployed} />}
              />
            )}
            {lastRelease.lastDeployed && (
              <Property
                label="Last Deployed"
                value={<TimeValue timestamp={lastRelease.lastDeployed} />}
              />
            )}
          </PropertyList>
        </Section>
      )}

      {/* Release History */}
      {history.length > 1 && (
        <Section title={`History (${history.length} revisions)`} icon={History} defaultExpanded={false}>
          <div className="space-y-2 max-h-48 overflow-y-auto">
            {history.map((release: any, idx: number) => (
              <div
                key={idx}
                className={clsx(
                  'p-2 rounded text-sm',
                  idx === 0
                    ? 'bg-green-500/10 border border-green-500/30'
                    : 'bg-theme-elevated/30'
                )}
              >
                <div className="flex items-center justify-between">
                  <span className="font-medium text-theme-text-primary">
                    Rev {release.revision}
                  </span>
                  <span
                    className={clsx(
                      'badge-sm',
                      release.status === 'deployed'
                        ? 'status-healthy'
                        : release.status === 'failed'
                        ? 'status-unhealthy'
                        : 'status-neutral'
                    )}
                  >
                    {release.status}
                  </span>
                </div>
                <div className="text-xs text-theme-text-secondary mt-1">
                  {release.chartVersion}
                  {release.appVersion && ` (app: ${release.appVersion})`}
                </div>
              </div>
            ))}
          </div>
        </Section>
      )}

      {/* Revision info */}
      {(status.helmChart || status.lastAppliedRevision || status.lastAttemptedRevision) && (
        <Section title="Revision Info" defaultExpanded={false}>
          <PropertyList>
            {status.helmChart && (
              <Property label="Helm Chart" value={status.helmChart} />
            )}
            {status.lastAppliedRevision && (
              <Property label="Applied Revision" value={status.lastAppliedRevision} />
            )}
            {status.lastAttemptedRevision && (
              <Property label="Attempted Revision" value={status.lastAttemptedRevision} />
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
