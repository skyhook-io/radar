import { Globe, Network, Shield, Server, Package } from 'lucide-react'
import { Section, PropertyList, Property, ConditionsSection, AlertBanner, useOperationalIssuesShown} from '../../ui/drawer-components'
import { getCAPIConditions } from '../resource-utils-capi'
import {
  getAWSMCPStatus, getAWSMCPEKSClusterName, getAWSMCPRegion, getAWSMCPVersion,
  getAWSMCPEndpointAccess, getAWSMCPAddons, getAWSMCPSubnets, getAWSMCPSecurityGroups,
  getAWSMCPNATGatewayIPs, getAWSMCPFailureDomains, getAWSMCPVPC,
} from '../resource-utils-aws-capi'

interface Props {
  data: any
  onNavigate?: (ref: { kind: string; namespace: string; name: string; group?: string }) => void
}

export function AWSManagedControlPlaneRenderer({ data }: Props) {
  const spec = data.spec || {}
  const conditions = getCAPIConditions(data)

  const mcpStatus = getAWSMCPStatus(data)
  const isFailed = mcpStatus.level === 'unhealthy'
  const operationalIssuesShown = useOperationalIssuesShown()
  const readyCond = conditions.find((c: any) => c.type === 'Ready')

  const vpc = getAWSMCPVPC(data)
  const subnets = getAWSMCPSubnets(data)
  const securityGroups = getAWSMCPSecurityGroups(data)
  const natIPs = getAWSMCPNATGatewayIPs(data)
  const failureDomains = getAWSMCPFailureDomains(data)
  const addons = getAWSMCPAddons(data)

  return (
    <>
      {isFailed && !operationalIssuesShown && (
        <AlertBanner
          variant="error"
          title="EKS Control Plane Not Ready"
          message={readyCond?.message || 'AWSManagedControlPlane is not ready.'}
        />
      )}

      <Section title="Overview" icon={Globe}>
        <PropertyList>
          <Property label="EKS Cluster" value={getAWSMCPEKSClusterName(data)} />
          <Property label="Region" value={getAWSMCPRegion(data)} />
          <Property label="Version" value={getAWSMCPVersion(data)} />
          <Property label="Endpoint Access" value={getAWSMCPEndpointAccess(data)} />
          {spec.roleName && <Property label="IAM Role" value={spec.roleName} />}
          {spec.identityRef?.name && (
            <Property label="Identity" value={`${spec.identityRef.kind}/${spec.identityRef.name}`} />
          )}
        </PropertyList>
      </Section>

      {/* Network - VPC */}
      {vpc.id !== '-' && (
        <Section title="VPC" icon={Network}>
          <PropertyList>
            <Property label="VPC ID" value={<span className="font-mono text-[11px]">{vpc.id}</span>} />
            {vpc.cidrBlock !== '-' && <Property label="CIDR" value={vpc.cidrBlock} />}
          </PropertyList>
        </Section>
      )}

      {/* Subnets */}
      {subnets.length > 0 && (
        <Section title={`Subnets (${subnets.length})`} icon={Network}>
          <table className="w-full text-xs">
            <thead>
              <tr className="text-theme-text-tertiary">
                <th className="text-left font-medium py-1">ID</th>
                <th className="text-left font-medium py-1">AZ</th>
                <th className="text-left font-medium py-1">Type</th>
                <th className="text-left font-medium py-1">CIDR</th>
              </tr>
            </thead>
            <tbody>
              {subnets.map((s, i) => (
                <tr key={i} className="border-t border-theme-border">
                  <td className="py-1 text-theme-text-secondary font-mono text-[10px]">{s.id}</td>
                  <td className="py-1 text-theme-text-secondary">{s.az}</td>
                  <td className="py-1">
                    <span className={`badge badge-sm ${s.isPublic
                      ? 'bg-sky-100 text-sky-700 border-sky-300 dark:bg-sky-950/50 dark:text-sky-400 dark:border-sky-700/40'
                      : 'bg-slate-100 text-slate-600 border-slate-300 dark:bg-slate-900/50 dark:text-slate-400 dark:border-slate-700/40'
                    }`}>{s.isPublic ? 'Public' : 'Private'}</span>
                  </td>
                  <td className="py-1 text-theme-text-secondary font-mono text-[10px]">{s.cidrBlock}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </Section>
      )}

      {/* Security Groups */}
      {securityGroups.length > 0 && (
        <Section title="Security Groups" icon={Shield}>
          <table className="w-full text-xs">
            <thead>
              <tr className="text-theme-text-tertiary">
                <th className="text-left font-medium py-1">Role</th>
                <th className="text-left font-medium py-1">ID</th>
                <th className="text-left font-medium py-1">Name</th>
              </tr>
            </thead>
            <tbody>
              {securityGroups.map((sg, i) => (
                <tr key={i} className="border-t border-theme-border">
                  <td className="py-1 text-theme-text-secondary font-medium">{sg.role}</td>
                  <td className="py-1 text-theme-text-secondary font-mono text-[10px]">{sg.id}</td>
                  <td className="py-1 text-theme-text-secondary text-[10px] break-all">{sg.name}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </Section>
      )}

      {/* NAT Gateways */}
      {natIPs.length > 0 && (
        <Section title="NAT Gateways" icon={Network}>
          <div className="flex flex-wrap gap-1">
            {natIPs.map((ip, i) => (
              <span key={i} className="badge badge-sm bg-theme-elevated text-theme-text-secondary border-theme-border font-mono text-[10px]">{ip}</span>
            ))}
          </div>
        </Section>
      )}

      {/* Addons */}
      {addons.length > 0 && (
        <Section title="EKS Addons" icon={Package}>
          <table className="w-full text-xs">
            <thead>
              <tr className="text-theme-text-tertiary">
                <th className="text-left font-medium py-1">Name</th>
                <th className="text-left font-medium py-1">Version</th>
                <th className="text-left font-medium py-1">Status</th>
              </tr>
            </thead>
            <tbody>
              {addons.map((a, i) => (
                <tr key={i} className="border-t border-theme-border">
                  <td className="py-1 text-theme-text-secondary font-medium">{a.name}</td>
                  <td className="py-1 text-theme-text-secondary font-mono text-[10px]">{a.statusVersion !== '-' ? a.statusVersion : a.specVersion}</td>
                  <td className="py-1">
                    <span className={`badge badge-sm ${a.status === 'ACTIVE'
                      ? 'status-healthy'
                      : a.status === 'DEGRADED'
                      ? 'status-unhealthy'
                      : 'status-neutral'
                    }`}>{a.status}</span>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </Section>
      )}

      {/* Failure Domains */}
      {failureDomains.length > 0 && (
        <Section title="Failure Domains" icon={Server}>
          <div className="flex flex-wrap gap-1">
            {failureDomains.map((az) => (
              <span key={az} className="badge badge-sm bg-theme-elevated text-theme-text-secondary border-theme-border">{az}</span>
            ))}
          </div>
        </Section>
      )}

      <ConditionsSection conditions={conditions} />
    </>
  )
}
