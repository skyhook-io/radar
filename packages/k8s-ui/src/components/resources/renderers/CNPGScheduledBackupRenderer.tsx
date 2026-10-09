import { Clock, Database } from 'lucide-react'
import { Section, PropertyList, Property, AlertBanner, ResourceLink } from '../../ui/drawer-components'
import { CronValue } from '../../ui/ScheduleValue'
import {
  getCNPGScheduledBackupCluster,
  getCNPGScheduleCron,
  getCNPGScheduledBackupMethod,
  getCNPGScheduledBackupLastSchedule,
  getCNPGScheduledBackupNextSchedule,
  getCNPGScheduledBackupIsSuspended,
  getCNPGScheduledBackupIsImmediate,
  getCNPGBackupPlugin,
  getCNPGScheduledBackupOwnerRef,
  CNPG_BARMAN_PLUGIN_NAME,
} from '../resource-utils-cnpg'

interface CNPGScheduledBackupRendererProps {
  data: any
  onNavigate?: (ref: { kind: string; namespace: string; name: string }) => void
}

export function CNPGScheduledBackupRenderer({ data, onNavigate }: CNPGScheduledBackupRendererProps) {
  const isSuspended = getCNPGScheduledBackupIsSuspended(data)
  const clusterName = getCNPGScheduledBackupCluster(data)
  const schedulePlugin = getCNPGBackupPlugin(data)

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
          <Property label="Cron Expression" value={<CronValue cron={getCNPGScheduleCron(data)} dialect="seconds" />} />
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
          {schedulePlugin && <Property label="Plugin" value={schedulePlugin.name} />}
          {schedulePlugin && (
            <Property
              label="Destination"
              value={schedulePlugin.name === CNPG_BARMAN_PLUGIN_NAME ? "From the Cluster's barman-cloud plugin" : 'Unknown: Radar does not model this plugin’s destination'}
            />
          )}
          {schedulePlugin?.name === CNPG_BARMAN_PLUGIN_NAME && schedulePlugin.parameters && Object.keys(schedulePlugin.parameters).length > 0 && (
            <Property label="Plugin parameters" value="Ignored by the barman-cloud plugin; the destination comes from the Cluster" />
          )}
          <Property label="Owner Reference" value={getCNPGScheduledBackupOwnerRef(data)} />
        </PropertyList>
      </Section>
    </>
  )
}
