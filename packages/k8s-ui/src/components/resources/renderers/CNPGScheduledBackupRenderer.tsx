import { Clock, Database } from 'lucide-react'
import { Section, PropertyList, Property, AlertBanner, ResourceLink } from '../../ui/drawer-components'
import { CronValue } from '../../ui/ScheduleValue'
import { TimeValue } from '../../ui/ScheduleValue'
import {
  getCNPGScheduledBackupCluster,
  getCNPGScheduleCron,
  getCNPGScheduledBackupMethod,
  getCNPGScheduledBackupNextSchedule,
  getCNPGScheduledBackupIsSuspended,
  getCNPGScheduledBackupIsImmediate,
  getCNPGBackupPlugin,
  getCNPGScheduledBackupOwnerRef,
  CNPG_BARMAN_OBJECTSTORE_GROUP,
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
          <Property label="Last Schedule" value={<TimeValue timestamp={data.status?.lastScheduleTime} />} />
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
          {/* Under the plugin method the destination lives on an ObjectStore in
              another API group, so naming it is the only route from here to
              where these backups will actually land. */}
          {schedulePlugin && <Property label="Plugin" value={schedulePlugin.name} />}
          {schedulePlugin?.parameters?.barmanObjectName && (
            <Property
              label="Object Store"
              value={
                <ResourceLink
                  name={schedulePlugin.parameters.barmanObjectName}
                  kind="objectstores"
                  group={CNPG_BARMAN_OBJECTSTORE_GROUP}
                  namespace={data.metadata?.namespace || ''}
                  onNavigate={onNavigate}
                />
              }
            />
          )}
          <Property label="Owner Reference" value={getCNPGScheduledBackupOwnerRef(data)} />
        </PropertyList>
      </Section>
    </>
  )
}
