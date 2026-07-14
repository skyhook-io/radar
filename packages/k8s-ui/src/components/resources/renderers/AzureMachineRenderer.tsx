import { Cloud } from 'lucide-react'
import { Section, PropertyList, Property, ConditionsSection, AlertBanner, useOperationalIssuesShown} from '../../ui/drawer-components'
import { getCAPIConditions } from '../resource-utils-capi'
import { getAzureMachineStatus, getAzureMachineVMSize, getAzureMachineProviderID } from '../resource-utils-azure-capi'

interface Props {
  data: any
  onNavigate?: (ref: { kind: string; namespace: string; name: string; group?: string }) => void
}

export function AzureMachineRenderer({ data }: Props) {
  const spec = data.spec || {}
  const conditions = getCAPIConditions(data)
  const machineStatus = getAzureMachineStatus(data)
  const isFailed = machineStatus.level === 'unhealthy'
  const operationalIssuesShown = useOperationalIssuesShown()
  const readyCond = conditions.find((c: any) => c.type === 'Ready')
  const providerID = getAzureMachineProviderID(data)

  return (
    <>
      {isFailed && !operationalIssuesShown && (
        <AlertBanner variant="error" title="Azure Machine Not Ready" message={readyCond?.message || 'AzureMachine is not ready.'} />
      )}

      <Section title="Instance" icon={Cloud}>
        <PropertyList>
          <Property label="VM Size" value={getAzureMachineVMSize(data)} />
          {spec.failureDomain && <Property label="Availability Zone" value={spec.failureDomain} />}
          {spec.osDisk?.osType && <Property label="OS Type" value={spec.osDisk.osType} />}
          {spec.osDisk?.diskSizeGB && <Property label="OS Disk" value={`${spec.osDisk.diskSizeGB}GB`} />}
          {providerID !== '-' && (
            <Property label="Provider ID" value={<span className="font-mono text-[10px] break-all">{providerID}</span>} />
          )}
          {spec.subnetName && <Property label="Subnet" value={spec.subnetName} />}
        </PropertyList>
      </Section>

      <ConditionsSection conditions={conditions} />
    </>
  )
}
