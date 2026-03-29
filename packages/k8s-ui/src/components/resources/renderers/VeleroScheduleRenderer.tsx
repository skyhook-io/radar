import { Clock, Archive } from 'lucide-react'
import { clsx } from 'clsx'
import { Section, PropertyList, Property, ConditionsSection, AlertBanner } from '../../ui/drawer-components'
import {
  getScheduleStatus,
  getScheduleCron,
  getScheduleLastBackup,
  getSchedulePaused,
  getScheduleTemplate,
  getScheduleUseOwnerReferences,
} from '../resource-utils-velero'

interface VeleroScheduleRendererProps {
  data: any
}

export function VeleroScheduleRenderer({ data }: VeleroScheduleRendererProps) {
  const status = data.status || {}
  const conditions = status.conditions || []

  const scheduleStatus = getScheduleStatus(data)
  const isPaused = getSchedulePaused(data)
  const template = getScheduleTemplate(data)

  const templateIncludedNs = template.includedNamespaces || []
  const templateExcludedNs = template.excludedNamespaces || []
  const templateIncludedResources = template.includedResources || []
  const templateExcludedResources = template.excludedResources || []

  return (
    <>
      {/* Problem alerts */}
      {isPaused && (
        <AlertBanner
          variant="warning"
          title="Schedule Paused"
          message="This backup schedule is currently paused. No new backups will be created."
        />
      )}
      {scheduleStatus.text === 'FailedValidation' && (
        <AlertBanner
          variant="error"
          title="Validation Failed"
          message="The schedule spec failed validation and is not active."
        />
      )}

      {/* Schedule section */}
      <Section title="Schedule" icon={Clock} defaultExpanded>
        <PropertyList>
          <Property label="Status" value={
            <span className={clsx('badge', scheduleStatus.color)}>
              {scheduleStatus.text}
            </span>
          } />
          <Property label="Cron Schedule" value={
            <span className="font-mono text-sm">{getScheduleCron(data)}</span>
          } />
          <Property label="Last Backup" value={getScheduleLastBackup(data)} />
          <Property label="Paused" value={isPaused ? 'Yes' : 'No'} />
          <Property label="Use Owner References" value={getScheduleUseOwnerReferences(data) ? 'Yes' : 'No'} />
        </PropertyList>
      </Section>

      {/* Backup Template section */}
      <Section title="Backup Template" icon={Archive} defaultExpanded>
        <PropertyList>
          {template.storageLocation && (
            <Property label="Storage Location" value={template.storageLocation} />
          )}
          {template.ttl && (
            <Property label="TTL" value={template.ttl} />
          )}
          {templateIncludedNs.length > 0 && (
            <Property label="Included Namespaces" value={
              <div className="flex flex-wrap gap-1">
                {templateIncludedNs.map((ns: string) => (
                  <span key={ns} className="badge-sm bg-theme-hover text-theme-text-secondary">{ns}</span>
                ))}
              </div>
            } />
          )}
          {templateIncludedNs.length === 0 && (
            <Property label="Included Namespaces" value="* (all)" />
          )}
          {templateExcludedNs.length > 0 && (
            <Property label="Excluded Namespaces" value={
              <div className="flex flex-wrap gap-1">
                {templateExcludedNs.map((ns: string) => (
                  <span key={ns} className="badge-sm bg-red-500/10 text-red-400">{ns}</span>
                ))}
              </div>
            } />
          )}
          {templateIncludedResources.length > 0 && (
            <Property label="Included Resources" value={
              <div className="flex flex-wrap gap-1">
                {templateIncludedResources.map((r: string) => (
                  <span key={r} className="badge-sm bg-theme-hover text-theme-text-secondary">{r}</span>
                ))}
              </div>
            } />
          )}
          {templateExcludedResources.length > 0 && (
            <Property label="Excluded Resources" value={
              <div className="flex flex-wrap gap-1">
                {templateExcludedResources.map((r: string) => (
                  <span key={r} className="badge-sm bg-red-500/10 text-red-400">{r}</span>
                ))}
              </div>
            } />
          )}
          {template.snapshotVolumes !== undefined && (
            <Property label="Snapshot Volumes" value={template.snapshotVolumes ? 'Yes' : 'No'} />
          )}
          {template.defaultVolumesToFsBackup !== undefined && (
            <Property label="Default FS Backup" value={template.defaultVolumesToFsBackup ? 'Yes' : 'No'} />
          )}
          {(template.volumeSnapshotLocations || []).length > 0 && (
            <Property label="Volume Snapshot Locations" value={
              <div className="flex flex-wrap gap-1">
                {template.volumeSnapshotLocations.map((loc: string) => (
                  <span key={loc} className="badge-sm bg-theme-hover text-theme-text-secondary">{loc}</span>
                ))}
              </div>
            } />
          )}
        </PropertyList>
      </Section>

      <ConditionsSection conditions={conditions} />
    </>
  )
}
