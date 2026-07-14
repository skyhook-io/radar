import { Cpu, Cloud } from 'lucide-react'
import { Section, PropertyList, Property, ConditionsSection, AlertBanner, useOperationalIssuesShown} from '../../ui/drawer-components'
import { getCAPIConditions } from '../resource-utils-capi'
import { getGCPMachineStatus, getGCPMachineInstanceType, getGCPMachineZone, getGCPMachineInstanceID } from '../resource-utils-gcp-capi'

interface Props {
  data: any
  onNavigate?: (ref: { kind: string; namespace: string; name: string; group?: string }) => void
}

export function GCPMachineRenderer({ data }: Props) {
  const spec = data.spec || {}
  const conditions = getCAPIConditions(data)
  const machineStatus = getGCPMachineStatus(data)
  const isFailed = machineStatus.level === 'unhealthy'
  const operationalIssuesShown = useOperationalIssuesShown()
  const readyCond = conditions.find((c: any) => c.type === 'Ready')

  return (
    <>
      {isFailed && !operationalIssuesShown && (
        <AlertBanner variant="error" title="GCP Machine Not Ready" message={readyCond?.message || 'GCPMachine is not ready.'} />
      )}

      <Section title="Instance" icon={Cloud}>
        <PropertyList>
          <Property label="Instance Type" value={getGCPMachineInstanceType(data)} />
          <Property label="Zone" value={getGCPMachineZone(data)} />
          <Property label="Instance ID" value={<span className="font-mono text-[11px]">{getGCPMachineInstanceID(data)}</span>} />
          {spec.image && <Property label="Image" value={spec.image} />}
        </PropertyList>
      </Section>

      {/* Disks */}
      {spec.additionalDisks?.length > 0 && (
        <Section title="Additional Disks" icon={Cpu}>
          <table className="w-full text-xs">
            <thead><tr className="text-theme-text-tertiary"><th className="text-left font-medium py-1">Size</th><th className="text-left font-medium py-1">Type</th></tr></thead>
            <tbody>
              {spec.additionalDisks.map((d: any, i: number) => (
                <tr key={i} className="border-t border-theme-border">
                  <td className="py-1 text-theme-text-secondary">{d.deviceType || '-'}</td>
                  <td className="py-1 text-theme-text-secondary">{d.size ? `${d.size}GB` : '-'}</td>
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
