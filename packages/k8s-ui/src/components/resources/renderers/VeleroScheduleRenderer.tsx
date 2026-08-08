import { Clock, Archive } from 'lucide-react'
import { Section, PropertyList, Property, ConditionsSection, AlertBanner } from '../../ui/drawer-components'
import {
  getScheduleStatus,
  getScheduleCron,
  getScheduleLastBackup,
  getSchedulePaused,
  getScheduleTemplate,
  getScheduleUseOwnerReferences,
  getScheduleValidationErrors,
} from '../resource-utils-velero'
import { VeleroPhaseValue } from './velero-cells'

interface VeleroScheduleRendererProps {
  data: any
}

export function VeleroScheduleRenderer({ data }: VeleroScheduleRendererProps) {
  const status = data.status || {}
  const conditions = status.conditions || []

  const scheduleStatus = getScheduleStatus(data)
  const isPaused = getSchedulePaused(data)
  const template = getScheduleTemplate(data)
  const validationErrors = getScheduleValidationErrors(data)

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
      {/* Gated on the errors themselves, not on the badge: a paused schedule
          badges as "Paused", which would otherwise hide the validation errors
          the user needs to fix before resuming. */}
      {(validationErrors.length > 0 || status.phase === 'FailedValidation') && (
        <AlertBanner
          variant="error"
          title="Validation Failed"
          message={isPaused
            ? 'The schedule spec failed validation. It will not create backups when resumed until this is fixed.'
            : 'The schedule spec failed validation and is not active — no backups are being created.'}
          items={validationErrors.length > 0 ? validationErrors : undefined}
        />
      )}

      {/* Schedule section */}
      <Section title="Schedule" icon={Clock} defaultExpanded>
        <PropertyList>
          <Property label="Status" value={
            <VeleroPhaseValue status={scheduleStatus} phase={status.phase || ''} />
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
