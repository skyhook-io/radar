import { Server, Settings } from 'lucide-react'
import { Section, PropertyList, Property, ConditionsSection, AlertBanner, useOperationalIssuesShown} from '../../ui/drawer-components'
import { getCAPIConditions } from '../resource-utils-capi'
import { getGCPMMPStatus, getGCPMMPNodePoolName, getGCPMMPMachineType, getGCPMMPDiskInfo, getGCPMMPScaling, getGCPMMPImageType } from '../resource-utils-gcp-capi'

interface Props {
  data: any
  onNavigate?: (ref: { kind: string; namespace: string; name: string; group?: string }) => void
}

export function GCPManagedMachinePoolRenderer({ data }: Props) {
  const spec = data.spec || {}
  const status = data.status || {}
  const conditions = getCAPIConditions(data)
  const mmpStatus = getGCPMMPStatus(data)
  const isFailed = mmpStatus.level === 'unhealthy'
  const operationalIssuesShown = useOperationalIssuesShown()
  const readyCond = conditions.find((c: any) => c.type === 'Ready')
  const scaling = getGCPMMPScaling(data)
  const management = spec.management || {}
  const labels = spec.kubernetesLabels || {}
  const taints = spec.kubernetesTaints || []
  const locations = spec.nodeLocations || []

  return (
    <>
      {isFailed && !operationalIssuesShown && (
        <AlertBanner variant="error" title="GKE Node Pool Not Ready" message={readyCond?.message || 'GCPManagedMachinePool is not ready.'} />
      )}

      <Section title="Overview" icon={Server}>
        <PropertyList>
          <Property label="Node Pool" value={getGCPMMPNodePoolName(data)} />
          <Property label="Machine Type" value={getGCPMMPMachineType(data)} />
          <Property label="Disk" value={getGCPMMPDiskInfo(data)} />
          <Property label="Image Type" value={getGCPMMPImageType(data)} />
          {spec.maxPodsPerNode != null && <Property label="Max Pods/Node" value={String(spec.maxPodsPerNode)} />}
        </PropertyList>
      </Section>

      <Section title="Scaling" icon={Settings}>
        <PropertyList>
          <Property label="Autoscaling" value={scaling.autoscaling ? 'Enabled' : 'Disabled'} />
          {scaling.autoscaling && <Property label="Min Nodes" value={String(scaling.min)} />}
          {scaling.autoscaling && <Property label="Max Nodes" value={String(scaling.max)} />}
          <Property label="Current Replicas" value={String(status.replicas ?? 0)} />
        </PropertyList>
      </Section>

      {/* Management */}
      {(management.autoRepair != null || management.autoUpgrade != null) && (
        <Section title="Management" icon={Settings}>
          <PropertyList>
            {management.autoRepair != null && <Property label="Auto Repair" value={management.autoRepair ? 'Enabled' : 'Disabled'} />}
            {management.autoUpgrade != null && <Property label="Auto Upgrade" value={management.autoUpgrade ? 'Enabled' : 'Disabled'} />}
          </PropertyList>
        </Section>
      )}

      {/* Locations */}
      {locations.length > 0 && (
        <Section title="Node Locations" icon={Server}>
          <div className="flex flex-wrap gap-1">
            {locations.map((loc: string) => (
              <span key={loc} className="badge badge-sm bg-theme-elevated text-theme-text-secondary border-theme-border">{loc}</span>
            ))}
          </div>
        </Section>
      )}

      {/* Labels */}
      {Object.keys(labels).length > 0 && (
        <Section title="Node Labels" icon={Settings}>
          <div className="flex flex-wrap gap-1">
            {Object.entries(labels).map(([k, v]) => (
              <span key={k} className="badge badge-sm bg-theme-elevated text-theme-text-secondary border-theme-border text-[10px]">{k}={v as string}</span>
            ))}
          </div>
        </Section>
      )}

      {/* Taints */}
      {taints.length > 0 && (
        <Section title="Taints" icon={Settings}>
          <table className="w-full text-xs">
            <thead><tr className="text-theme-text-tertiary"><th className="text-left font-medium py-1">Key</th><th className="text-left font-medium py-1">Value</th><th className="text-left font-medium py-1">Effect</th></tr></thead>
            <tbody>
              {taints.map((t: any, i: number) => (
                <tr key={i} className="border-t border-theme-border">
                  <td className="py-1 text-theme-text-secondary">{t.key}</td>
                  <td className="py-1 text-theme-text-secondary">{t.value}</td>
                  <td className="py-1 text-theme-text-secondary">{t.effect}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </Section>
      )}

      <ConditionsSection conditions={conditions} />
    </>
  )
}
