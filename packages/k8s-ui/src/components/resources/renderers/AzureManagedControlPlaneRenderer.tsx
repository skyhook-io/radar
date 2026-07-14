import { Globe, Shield, Settings } from 'lucide-react'
import { Section, PropertyList, Property, ConditionsSection, AlertBanner, useOperationalIssuesShown} from '../../ui/drawer-components'
import { getCAPIConditions } from '../resource-utils-capi'
import { getAzureMCPStatus, getAzureMCPLocation, getAzureMCPVersion, getAzureMCPResourceGroup, getAzureMCPSKUTier, getAzureMCPNetworkPlugin, getAzureMCPNetworkPolicy, getAzureMCPDNSPrefix, getAzureMCPUpgradeChannel, getAzureMCPPrivateCluster } from '../resource-utils-azure-capi'

interface Props {
  data: any
  onNavigate?: (ref: { kind: string; namespace: string; name: string; group?: string }) => void
}

export function AzureManagedControlPlaneRenderer({ data }: Props) {
  const spec = data.spec || {}
  const conditions = getCAPIConditions(data)
  const mcpStatus = getAzureMCPStatus(data)
  const isFailed = mcpStatus.level === 'unhealthy'
  const operationalIssuesShown = useOperationalIssuesShown()
  const readyCond = conditions.find((c: any) => c.type === 'Ready')
  const apiAccess = spec.apiServerAccessProfile || {}
  const authorizedRanges = apiAccess.authorizedIPRanges || []

  return (
    <>
      {isFailed && !operationalIssuesShown && (
        <AlertBanner variant="error" title="AKS Control Plane Not Ready" message={readyCond?.message || 'AzureManagedControlPlane is not ready.'} />
      )}

      <Section title="Overview" icon={Globe}>
        <PropertyList>
          <Property label="Location" value={getAzureMCPLocation(data)} />
          <Property label="Resource Group" value={getAzureMCPResourceGroup(data)} />
          <Property label="Version" value={getAzureMCPVersion(data)} />
          <Property label="SKU Tier" value={getAzureMCPSKUTier(data)} />
          {spec.dnsPrefix && <Property label="DNS Prefix" value={getAzureMCPDNSPrefix(data)} />}
          {spec.subscriptionID && <Property label="Subscription" value={<span className="font-mono text-[10px]">{spec.subscriptionID}</span>} />}
        </PropertyList>
      </Section>

      {/* Networking */}
      <Section title="Networking" icon={Shield}>
        <PropertyList>
          <Property label="Network Plugin" value={getAzureMCPNetworkPlugin(data)} />
          <Property label="Network Policy" value={getAzureMCPNetworkPolicy(data)} />
          <Property label="Private Cluster" value={getAzureMCPPrivateCluster(data) ? 'Yes' : 'No'} />
          {spec.dnsServiceIP && <Property label="DNS Service IP" value={spec.dnsServiceIP} />}
          {spec.loadBalancerSKU && <Property label="Load Balancer SKU" value={spec.loadBalancerSKU} />}
        </PropertyList>
      </Section>

      {/* Upgrade */}
      {spec.autoUpgradeProfile && (
        <Section title="Upgrade" icon={Settings}>
          <PropertyList>
            <Property label="Channel" value={getAzureMCPUpgradeChannel(data)} />
          </PropertyList>
        </Section>
      )}

      {/* Authorized IP Ranges */}
      {authorizedRanges.length > 0 && (
        <Section title="Authorized IP Ranges" icon={Shield}>
          <div className="flex flex-wrap gap-1">
            {authorizedRanges.map((range: string, i: number) => (
              <span key={i} className="badge badge-sm bg-theme-elevated text-theme-text-secondary border-theme-border font-mono text-[10px]">{range}</span>
            ))}
          </div>
        </Section>
      )}

      <ConditionsSection conditions={conditions} />
    </>
  )
}
