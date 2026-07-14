import { Globe, Server } from 'lucide-react'
import { Section, PropertyList, Property, ConditionsSection, AlertBanner, useOperationalIssuesShown} from '../../ui/drawer-components'
import { getCAPIConditions } from '../resource-utils-capi'
import { getAWSManagedClusterStatus, getAWSManagedClusterEndpoint, getAWSManagedClusterFailureDomains } from '../resource-utils-aws-capi'

interface Props {
  data: any
}

export function AWSManagedClusterRenderer({ data }: Props) {
  const conditions = getCAPIConditions(data)
  const clusterStatus = getAWSManagedClusterStatus(data)
  const isFailed = clusterStatus.level === 'unhealthy'
  const operationalIssuesShown = useOperationalIssuesShown()
  const readyCond = conditions.find((c: any) => c.type === 'Ready')
  const failureDomains = getAWSManagedClusterFailureDomains(data)
  const endpoint = getAWSManagedClusterEndpoint(data)

  return (
    <>
      {isFailed && !operationalIssuesShown && (
        <AlertBanner
          variant="error"
          title="Managed Cluster Not Ready"
          message={readyCond?.message || 'AWSManagedCluster is not ready.'}
        />
      )}

      <Section title="Overview" icon={Globe}>
        <PropertyList>
          {endpoint !== '-' && (
            <Property label="Endpoint" value={<span className="font-mono text-[10px] break-all">{endpoint}</span>} />
          )}
        </PropertyList>
      </Section>

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
