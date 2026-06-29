import { Server, Settings } from 'lucide-react'
import { clsx } from 'clsx'
import { Section, PropertyList, Property, ConditionsSection, AlertBanner } from '../../ui/drawer-components'
import { getCAPIConditions } from '../resource-utils-capi'
import { getAWSMMPStatus, getAWSMMPInstanceType, getAWSMMPCapacityType, getAWSMMPAMIType, getAWSMMPNodegroupName, getAWSMMPScaling } from '../resource-utils-aws-capi'
import { CAPACITY_TYPE_BADGE } from '../../../utils/badge-colors'

interface Props {
  data: any
  onNavigate?: (ref: { kind: string; namespace: string; name: string; group?: string }) => void
}

export function AWSManagedMachinePoolRenderer({ data }: Props) {
  const spec = data.spec || {}
  const status = data.status || {}
  const conditions = getCAPIConditions(data)

  const mmpStatus = getAWSMMPStatus(data)
  const isFailed = mmpStatus.level === 'unhealthy'
  const readyCond = conditions.find((c: any) => c.type === 'Ready')

  const scaling = getAWSMMPScaling(data)
  const capacityType = getAWSMMPCapacityType(data)
  const labels = spec.labels || {}
  const subnetIDs = spec.subnetIDs || []

  return (
    <>
      {isFailed && (
        <AlertBanner
          variant="error"
          title="Managed Machine Pool Not Ready"
          message={readyCond?.message || 'AWSManagedMachinePool is not ready.'}
        />
      )}

      <Section title="Overview" icon={Server}>
        <PropertyList>
          <Property label="Node Group" value={getAWSMMPNodegroupName(data)} />
          <Property label="Instance Type" value={getAWSMMPInstanceType(data)} />
          <Property label="AMI Type" value={getAWSMMPAMIType(data)} />
          <Property label="Capacity Type" value={
            <span className={clsx('badge badge-sm', capacityType === 'spot' ? CAPACITY_TYPE_BADGE.spot : CAPACITY_TYPE_BADGE.onDemand)}>{capacityType === 'onDemand' ? 'On-Demand' : capacityType}</span>
          } />
          {spec.roleName && <Property label="IAM Role" value={spec.roleName} />}
        </PropertyList>
      </Section>

      <Section title="Scaling" icon={Settings}>
        <PropertyList>
          <Property label="Min Size" value={String(scaling.min)} />
          <Property label="Max Size" value={String(scaling.max)} />
          <Property label="Current Replicas" value={String(status.replicas ?? 0)} />
          {spec.updateConfig?.maxUnavailable != null && (
            <Property label="Max Unavailable" value={String(spec.updateConfig.maxUnavailable)} />
          )}
        </PropertyList>
      </Section>

      {/* Subnets */}
      {subnetIDs.length > 0 && (
        <Section title="Subnets" icon={Server}>
          <div className="flex flex-wrap gap-1">
            {subnetIDs.map((id: string) => (
              <span key={id} className="badge badge-sm bg-theme-elevated text-theme-text-secondary border-theme-border font-mono text-[10px]">{id}</span>
            ))}
          </div>
        </Section>
      )}

      {/* Labels */}
      {Object.keys(labels).length > 0 && (
        <Section title="Node Labels" icon={Settings}>
          <div className="flex flex-wrap gap-1">
            {Object.entries(labels).map(([k, v]) => (
              <span key={k} className="badge badge-sm bg-theme-elevated text-theme-text-secondary border-theme-border text-[10px]">
                {k}={v as string}
              </span>
            ))}
          </div>
        </Section>
      )}

      <ConditionsSection conditions={conditions} />
    </>
  )
}
