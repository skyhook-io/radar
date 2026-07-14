import { HeartPulse, Settings, Shield } from 'lucide-react'
import { Section, PropertyList, Property, ConditionsSection, AlertBanner, LabelSelectorDisplay, useOperationalIssuesShown} from '../../ui/drawer-components'
import { getMachineHealthCheckStatus, getMachineHealthCheckClusterName } from '../resource-utils-capi'

interface Props {
  data: any
  onNavigate?: (ref: { kind: string; namespace: string; name: string; group?: string }) => void
}

export function CAPIMachineHealthCheckRenderer({ data }: Props) {
  const status = data.status || {}
  const spec = data.spec || {}
  const conditions = status.v1beta2?.conditions || status.conditions || []

  const mhcStatus = getMachineHealthCheckStatus(data)
  const isFailed = mhcStatus.level === 'unhealthy'
  const operationalIssuesShown = useOperationalIssuesShown()
  const readyCond = conditions.find((c: any) => c.type === 'Ready')

  const clusterName = getMachineHealthCheckClusterName(data)
  const expectedMachines = status.expectedMachines ?? 0
  const currentHealthy = status.currentHealthy ?? 0
  const remediationsAllowed = status.remediationsAllowed ?? 0
  const unhealthyConditions = spec.unhealthyConditions || []
  const nodeStartupTimeout = spec.nodeStartupTimeout
  const selector = spec.selector || {}
  const remediationTemplate = spec.remediationTemplate || {}

  // v1beta2 fields
  const unhealthyNodeConditions = spec.unhealthyNodeConditions || []
  const unhealthyMachineConditions = spec.unhealthyMachineConditions || []

  return (
    <>
      {isFailed && !operationalIssuesShown && (
        <AlertBanner
          variant="error"
          title="MachineHealthCheck Issue"
          message={readyCond?.message || 'MachineHealthCheck has unhealthy machines.'}
        />
      )}

      <Section title="Overview" icon={HeartPulse}>
        <PropertyList>
          <Property label="Cluster" value={clusterName} />
          <Property label="Expected Machines" value={String(expectedMachines)} />
          <Property label="Current Healthy" value={`${currentHealthy}/${expectedMachines}`} />
          <Property label="Remediations Allowed" value={String(remediationsAllowed)} />
          {nodeStartupTimeout && <Property label="Node Startup Timeout" value={nodeStartupTimeout} />}
          {spec.maxUnhealthy != null && <Property label="Max Unhealthy" value={String(spec.maxUnhealthy)} />}
          {spec.unhealthyRange != null && <Property label="Unhealthy Range" value={spec.unhealthyRange} />}
        </PropertyList>
      </Section>

      {/* Selector */}
      {(selector.matchLabels || selector.matchExpressions) && (
        <Section title="Selector" icon={Settings}>
          <LabelSelectorDisplay selector={selector} />
        </Section>
      )}

      {/* Unhealthy Conditions (v1beta1) */}
      {unhealthyConditions.length > 0 && (
        <Section title="Unhealthy Conditions" icon={Shield}>
          <table className="w-full text-xs">
            <thead>
              <tr className="text-theme-text-tertiary">
                <th className="text-left font-medium py-1">Type</th>
                <th className="text-left font-medium py-1">Status</th>
                <th className="text-left font-medium py-1">Timeout</th>
              </tr>
            </thead>
            <tbody>
              {unhealthyConditions.map((uc: any, i: number) => (
                <tr key={i} className="border-t border-theme-border">
                  <td className="py-1 text-theme-text-secondary">{uc.type}</td>
                  <td className="py-1 text-theme-text-secondary">{uc.status}</td>
                  <td className="py-1 text-theme-text-secondary">{uc.timeout}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </Section>
      )}

      {/* v1beta2: unhealthy node/machine conditions */}
      {unhealthyNodeConditions.length > 0 && (
        <Section title="Unhealthy Node Conditions" icon={Shield}>
          <table className="w-full text-xs">
            <thead>
              <tr className="text-theme-text-tertiary">
                <th className="text-left font-medium py-1">Type</th>
                <th className="text-left font-medium py-1">Status</th>
                <th className="text-left font-medium py-1">Timeout</th>
              </tr>
            </thead>
            <tbody>
              {unhealthyNodeConditions.map((uc: any, i: number) => (
                <tr key={i} className="border-t border-theme-border">
                  <td className="py-1 text-theme-text-secondary">{uc.type}</td>
                  <td className="py-1 text-theme-text-secondary">{uc.status}</td>
                  <td className="py-1 text-theme-text-secondary">{uc.timeout}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </Section>
      )}

      {unhealthyMachineConditions.length > 0 && (
        <Section title="Unhealthy Machine Conditions" icon={Shield}>
          <table className="w-full text-xs">
            <thead>
              <tr className="text-theme-text-tertiary">
                <th className="text-left font-medium py-1">Type</th>
                <th className="text-left font-medium py-1">Status</th>
                <th className="text-left font-medium py-1">Timeout</th>
              </tr>
            </thead>
            <tbody>
              {unhealthyMachineConditions.map((uc: any, i: number) => (
                <tr key={i} className="border-t border-theme-border">
                  <td className="py-1 text-theme-text-secondary">{uc.type}</td>
                  <td className="py-1 text-theme-text-secondary">{uc.status}</td>
                  <td className="py-1 text-theme-text-secondary">{uc.timeout}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </Section>
      )}

      {/* Remediation Template */}
      {remediationTemplate.kind && (
        <Section title="Remediation Template" icon={Settings}>
          <PropertyList>
            <Property label="Kind" value={remediationTemplate.kind} />
            <Property label="Name" value={remediationTemplate.name || '-'} />
            {remediationTemplate.namespace && <Property label="Namespace" value={remediationTemplate.namespace} />}
          </PropertyList>
        </Section>
      )}

      <ConditionsSection conditions={conditions} />
    </>
  )
}
