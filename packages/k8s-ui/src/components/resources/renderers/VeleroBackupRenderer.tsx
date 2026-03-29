import { Archive, Clock, HardDrive, Filter } from 'lucide-react'
import { clsx } from 'clsx'
import { Section, PropertyList, Property, ConditionsSection, AlertBanner } from '../../ui/drawer-components'
import {
  getBackupStatus,
  getBackupStorageLocation,
  getBackupIncludedNamespaces,
  getBackupExcludedNamespaces,
  getBackupIncludedResources,
  getBackupExcludedResources,
  getBackupDuration,
  getBackupItemCount,
  getBackupExpiry,
  getBackupErrors,
  getBackupWarnings,
  getBackupValidationErrors,
  getBackupTTL,
  getBackupSnapshotVolumes,
  getBackupDefaultVolumesToFsBackup,
  getBackupVolumeSnapshotLocations,
} from '../resource-utils-velero'
import { formatAge } from '../resource-utils'

interface VeleroBackupRendererProps {
  data: any
}

export function VeleroBackupRenderer({ data }: VeleroBackupRendererProps) {
  const status = data.status || {}
  const conditions = status.conditions || []

  const backupStatus = getBackupStatus(data)
  const errors = getBackupErrors(data)
  const warnings = getBackupWarnings(data)
  const validationErrors = getBackupValidationErrors(data)
  const includedNamespaces = getBackupIncludedNamespaces(data)
  const excludedNamespaces = getBackupExcludedNamespaces(data)
  const includedResources = getBackupIncludedResources(data)
  const excludedResources = getBackupExcludedResources(data)
  const vslLocations = getBackupVolumeSnapshotLocations(data)

  const isFailed = backupStatus.level === 'unhealthy'
  const isPartiallyFailed = backupStatus.text === 'PartiallyFailed'
  const isInProgress = backupStatus.text === 'InProgress' || backupStatus.text === 'Uploading'

  // Progress data
  const progress = status.progress
  const itemsBacked = progress?.itemsBackedUp ?? 0
  const totalItems = progress?.totalItems ?? 0
  const progressPercent = totalItems > 0 ? Math.round((itemsBacked / totalItems) * 100) : 0

  return (
    <>
      {/* Problem alerts */}
      {(isFailed || isPartiallyFailed) && (
        <AlertBanner
          variant="error"
          title={isFailed ? 'Backup Failed' : 'Backup Partially Failed'}
          message={`${errors} error(s) occurred during backup.`}
          items={validationErrors.length > 0 ? validationErrors : undefined}
        />
      )}
      {warnings > 0 && !isFailed && (
        <AlertBanner
          variant="warning"
          title={`${warnings} Warning(s)`}
          message={`Backup completed with ${warnings} warning(s).`}
        />
      )}

      {/* Status section */}
      <Section title="Status" icon={Archive} defaultExpanded>
        <PropertyList>
          <Property label="Phase" value={
            <span className={clsx('badge', backupStatus.color)}>
              {backupStatus.text}
            </span>
          } />
          {status.startTimestamp && (
            <Property label="Started" value={formatAge(status.startTimestamp) + ' ago'} />
          )}
          {status.completionTimestamp && (
            <Property label="Completed" value={formatAge(status.completionTimestamp) + ' ago'} />
          )}
          <Property label="Duration" value={getBackupDuration(data)} />
          <Property label="Expiration" value={getBackupExpiry(data)} />
          {errors > 0 && <Property label="Errors" value={String(errors)} />}
          {warnings > 0 && <Property label="Warnings" value={String(warnings)} />}
        </PropertyList>
      </Section>

      {/* Progress section (if in progress) */}
      {isInProgress && progress && (
        <Section title="Progress" defaultExpanded>
          <div className="space-y-2">
            <div className="flex items-center justify-between text-sm">
              <span className="text-theme-text-secondary">Items backed up</span>
              <span className="text-theme-text-primary font-medium">{getBackupItemCount(data)}</span>
            </div>
            <div className="w-full bg-theme-elevated rounded-full h-2">
              <div
                className="bg-blue-500 h-2 rounded-full transition-all"
                style={{ width: `${progressPercent}%` }}
              />
            </div>
            <div className="text-xs text-theme-text-tertiary text-right">{progressPercent}%</div>
          </div>
        </Section>
      )}

      {/* Scope section */}
      {(includedNamespaces.length > 0 || excludedNamespaces.length > 0 || includedResources.length > 0 || excludedResources.length > 0 || data.spec?.labelSelector) && (
        <Section title="Scope" icon={Filter} defaultExpanded>
          <PropertyList>
            {includedNamespaces.length > 0 && (
              <Property label="Included Namespaces" value={
                <div className="flex flex-wrap gap-1">
                  {includedNamespaces.map((ns: string) => (
                    <span key={ns} className="badge-sm bg-theme-hover text-theme-text-secondary">{ns}</span>
                  ))}
                </div>
              } />
            )}
            {includedNamespaces.length === 0 && (
              <Property label="Included Namespaces" value="* (all)" />
            )}
            {excludedNamespaces.length > 0 && (
              <Property label="Excluded Namespaces" value={
                <div className="flex flex-wrap gap-1">
                  {excludedNamespaces.map((ns: string) => (
                    <span key={ns} className="badge-sm bg-red-500/10 text-red-400">{ns}</span>
                  ))}
                </div>
              } />
            )}
            {includedResources.length > 0 && (
              <Property label="Included Resources" value={
                <div className="flex flex-wrap gap-1">
                  {includedResources.map((r: string) => (
                    <span key={r} className="badge-sm bg-theme-hover text-theme-text-secondary">{r}</span>
                  ))}
                </div>
              } />
            )}
            {excludedResources.length > 0 && (
              <Property label="Excluded Resources" value={
                <div className="flex flex-wrap gap-1">
                  {excludedResources.map((r: string) => (
                    <span key={r} className="badge-sm bg-red-500/10 text-red-400">{r}</span>
                  ))}
                </div>
              } />
            )}
            {data.spec?.labelSelector && (
              <Property label="Label Selector" value={
                <span className="text-sm font-mono text-theme-text-secondary break-all">
                  {JSON.stringify(data.spec.labelSelector)}
                </span>
              } />
            )}
          </PropertyList>
        </Section>
      )}

      {/* Storage section */}
      <Section title="Storage" icon={HardDrive} defaultExpanded>
        <PropertyList>
          <Property label="Storage Location" value={getBackupStorageLocation(data)} />
          {vslLocations.length > 0 && (
            <Property label="Volume Snapshot Locations" value={
              <div className="flex flex-wrap gap-1">
                {vslLocations.map((loc: string) => (
                  <span key={loc} className="badge-sm bg-theme-hover text-theme-text-secondary">{loc}</span>
                ))}
              </div>
            } />
          )}
        </PropertyList>
      </Section>

      {/* Options section */}
      <Section title="Options" icon={Clock}>
        <PropertyList>
          <Property label="TTL" value={getBackupTTL(data)} />
          <Property label="Snapshot Volumes" value={getBackupSnapshotVolumes(data)} />
          <Property label="Default FS Backup" value={getBackupDefaultVolumesToFsBackup(data)} />
        </PropertyList>
      </Section>

      <ConditionsSection conditions={conditions} />
    </>
  )
}
