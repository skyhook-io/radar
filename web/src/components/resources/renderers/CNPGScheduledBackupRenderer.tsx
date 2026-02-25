import { Clock, Database } from 'lucide-react'
import { Section, PropertyList, Property, AlertBanner, ResourceLink } from '../drawer-components'
import {
  getCNPGScheduledBackupCluster,
  getCNPGScheduleCron,
  getCNPGScheduledBackupMethod,
  getCNPGScheduledBackupLastSchedule,
  getCNPGScheduledBackupNextSchedule,
  getCNPGScheduledBackupIsSuspended,
  getCNPGScheduledBackupIsImmediate,
  getCNPGScheduledBackupOwnerRef,
} from '../resource-utils-cnpg'

interface CNPGScheduledBackupRendererProps {
  data: any
  onNavigate?: (ref: { kind: string; namespace: string; name: string }) => void
}

export function CNPGScheduledBackupRenderer({ data, onNavigate }: CNPGScheduledBackupRendererProps) {
  const isSuspended = getCNPGScheduledBackupIsSuspended(data)
  const clusterName = getCNPGScheduledBackupCluster(data)

  return (
    <>
      {/* Suspended alert */}
      {isSuspended && (
        <AlertBanner
          variant="warning"
          title="Schedule Suspended"
          message="This scheduled backup is currently suspended. No new backups will be created."
        />
      )}

      {/* Schedule */}
      <Section title="Schedule" icon={Clock} defaultExpanded>
        <PropertyList>
          <Property label="Cron Expression" value={getCNPGScheduleCron(data)} />
          <Property label="Last Schedule" value={getCNPGScheduledBackupLastSchedule(data)} />
          <Property label="Next Schedule" value={getCNPGScheduledBackupNextSchedule(data)} />
          <Property label="Suspended" value={isSuspended ? 'Yes' : 'No'} />
          <Property label="Immediate" value={getCNPGScheduledBackupIsImmediate(data) ? 'Yes' : 'No'} />
        </PropertyList>
      </Section>

      {/* Backup Configuration */}
      <Section title="Backup Configuration" icon={Database} defaultExpanded>
        <PropertyList>
          <Property label="Cluster" value={(() => {
            if (clusterName && clusterName !== '-') {
              return (
                <ResourceLink
                  name={clusterName}
                  kind="clusters"
                  namespace={data.metadata?.namespace || ''}
                  group="postgresql.cnpg.io"
                  onNavigate={onNavigate}
                />
              )
            }
            return clusterName
          })()} />
          <Property label="Method" value={getCNPGScheduledBackupMethod(data)} />
          <Property label="Owner Reference" value={getCNPGScheduledBackupOwnerRef(data)} />
        </PropertyList>
      </Section>
    </>
  )
}
