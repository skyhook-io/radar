import { Globe, Shield, Settings } from 'lucide-react'
import { Section, PropertyList, Property, ConditionsSection, AlertBanner, useOperationalIssuesShown} from '../../ui/drawer-components'
import { getCAPIConditions } from '../resource-utils-capi'
import { getGCPMCPStatus, getGCPMCPClusterName, getGCPMCPProject, getGCPMCPLocation, getGCPMCPVersion, getGCPMCPReleaseChannel, getGCPMCPAutopilot, getGCPMCPEndpoint } from '../resource-utils-gcp-capi'

interface Props {
  data: any
  onNavigate?: (ref: { kind: string; namespace: string; name: string; group?: string }) => void
}

export function GCPManagedControlPlaneRenderer({ data }: Props) {
  const spec = data.spec || {}
  const conditions = getCAPIConditions(data)
  const mcpStatus = getGCPMCPStatus(data)
  const isFailed = mcpStatus.level === 'unhealthy'
  const operationalIssuesShown = useOperationalIssuesShown()
  const readyCond = conditions.find((c: any) => c.type === 'Ready')
  const endpoint = getGCPMCPEndpoint(data)
  const masterAuth = spec.master_authorized_networks_config

  return (
    <>
      {isFailed && !operationalIssuesShown && (
        <AlertBanner variant="error" title="GKE Control Plane Not Ready" message={readyCond?.message || 'GCPManagedControlPlane is not ready.'} />
      )}

      <Section title="Overview" icon={Globe}>
        <PropertyList>
          <Property label="GKE Cluster" value={getGCPMCPClusterName(data)} />
          <Property label="Project" value={getGCPMCPProject(data)} />
          <Property label="Location" value={getGCPMCPLocation(data)} />
          <Property label="Version" value={getGCPMCPVersion(data)} />
          <Property label="Release Channel" value={getGCPMCPReleaseChannel(data)} />
          {getGCPMCPAutopilot(data) && <Property label="Autopilot" value="Enabled" />}
          {endpoint !== '-' && <Property label="Endpoint" value={<span className="font-mono text-[10px] break-all">{endpoint}</span>} />}
        </PropertyList>
      </Section>

      {/* Cluster Network */}
      {spec.clusterNetwork && (
        <Section title="Network" icon={Shield}>
          <PropertyList>
            {spec.clusterNetwork?.pod?.cidrBlock && <Property label="Pod CIDR" value={spec.clusterNetwork.pod.cidrBlock} />}
            {spec.clusterNetwork?.service?.cidrBlock && <Property label="Service CIDR" value={spec.clusterNetwork.service.cidrBlock} />}
            {spec.clusterNetwork?.useIPAliases != null && <Property label="IP Aliases" value={spec.clusterNetwork.useIPAliases ? 'Enabled' : 'Disabled'} />}
          </PropertyList>
        </Section>
      )}

      {/* Services */}
      {(spec.loggingService || spec.monitoringService) && (
        <Section title="Services" icon={Settings}>
          <PropertyList>
            {spec.loggingService && <Property label="Logging" value={spec.loggingService} />}
            {spec.monitoringService && <Property label="Monitoring" value={spec.monitoringService} />}
          </PropertyList>
        </Section>
      )}

      {/* Master Authorized Networks */}
      {masterAuth?.cidr_blocks?.length > 0 && (
        <Section title="Authorized Networks" icon={Shield}>
          <div className="flex flex-wrap gap-1">
            {masterAuth.cidr_blocks.map((block: any, i: number) => (
              <span key={i} className="badge badge-sm bg-theme-elevated text-theme-text-secondary border-theme-border font-mono text-[10px]">
                {block.display_name ? `${block.display_name}: ` : ''}{block.cidr_block}
              </span>
            ))}
          </div>
        </Section>
      )}

      <ConditionsSection conditions={conditions} />
    </>
  )
}
