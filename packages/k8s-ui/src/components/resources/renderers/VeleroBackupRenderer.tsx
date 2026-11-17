import { VeleroRunMessages, type VeleroRunMessagesFetch } from './VeleroRunMessages'
import { Archive, Clock, HardDrive, Filter } from 'lucide-react'
import { clsx } from 'clsx'
import { HEALTH_BADGE_COLORS } from '../../../utils/badge-colors'
import { Section, PropertyList, Property, ConditionsSection, AlertBanner, ResourceLink, useOperationalIssuesShown } from '../../ui/drawer-components'
import {
  getBackupStatus,
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
  getBackupItemOperationTimeout,
  getBackupSnapshotVolumes,
  getBackupDefaultVolumesToFsBackup,
  getBackupVolumeSnapshotLocations,
  getBackupQueuePosition,
  isBackupActivePhase,
  isBackupPartialFailurePhase,
} from '../resource-utils-velero'
import { VeleroPhaseValue } from './velero-cells'
import { VELERO_SERVER_DEFAULT_RETENTION, veleroDeclaredRetention } from '../../velero/reading'
import { getBackupStorageLocation, veleroPhaseLabel } from '../resource-utils-velero'
import { TimeValue } from '../../ui/ScheduleValue'

interface VeleroBackupRendererProps {
  data: any
  /**
   * The phase of the storage location this backup was written to, when the host
   * looked it up. Undefined means unresolved — not "healthy".
   *
   * A backup cannot observe the health of the bucket holding it. Its own status
   * keeps saying Completed, accurately, while the location goes Unavailable and
   * there is nothing to restore from. This page is where someone checks whether
   * they can go back to this point, so the fact belongs here rather than one
   * screen away.
   */
  storageLocationPhase?: string
  /**
   * The lookup answered and no location by the backup's name exists. Distinct from an
   * unresolved lookup: a backup whose storage location has been deleted cannot
   * be restored from, and without this it renders exactly like a healthy one.
   */
  storageLocationMissing?: boolean
  /** Wired by the host, which owns the fetch. Optional: a consumer that does
   *  not wire it gets the counts on their own, with no button. */
  messages?: VeleroRunMessagesFetch
  /** Without it the location renders as plain text: the page names a location as
   *  the reason nothing can be restored and gives no way to go look at it. */
  onNavigate?: (ref: { kind: string; namespace: string; name: string; group?: string }) => void
}

export function VeleroBackupRenderer({ data, storageLocationPhase, storageLocationMissing, messages, onNavigate }: VeleroBackupRendererProps) {
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

  const phase = status.phase || ''
  const isFailed = backupStatus.level === 'unhealthy'
  const isValidationFailure = phase === 'FailedValidation'
  const isPartiallyFailed = isBackupPartialFailurePhase(phase)
  // A failed, rejected or partially failed run that raises an issue is
  // described by the Operational issues section above it. Warnings raise none,
  // so their box always shows.
  const issueShown = useOperationalIssuesShown()
  const isInProgress = isBackupActivePhase(phase)
  const queuePosition = getBackupQueuePosition(data)

  // Progress data
  const progress = status.progress
  const itemsBacked = progress?.itemsBackedUp ?? 0
  const totalItems = progress?.totalItems ?? 0
  const progressPercent = totalItems > 0 ? Math.round((itemsBacked / totalItems) * 100) : 0

  return (
    <>
      {/* Problem alerts */}
      {isValidationFailure && !issueShown && (
        <AlertBanner
          variant="error"
          title="Backup Validation Failed"
          message={status.failureReason || 'Velero rejected this backup before it started — nothing was backed up.'}
          items={validationErrors.length > 0 ? validationErrors : undefined}
        />
      )}
      {isFailed && !isValidationFailure && !issueShown && (
        <AlertBanner
          variant="error"
          title="Backup Failed"
          message={status.failureReason || `${errors} error(s) occurred during backup.`}
          items={validationErrors.length > 0 ? validationErrors : undefined}
        />
      )}
      {isPartiallyFailed && !issueShown && (
        <AlertBanner
          variant="warning"
          title="Backup Partially Failed"
          message={status.failureReason || `${errors} error(s) occurred — some items were not backed up. Do not assume this backup is complete.`}
          items={validationErrors.length > 0 ? validationErrors : undefined}
        />
      )}
      {warnings > 0 && !isFailed && !isPartiallyFailed && (
        <AlertBanner
          variant="warning"
          title={`${warnings} Warning(s)`}
          message={phase === 'Completed'
            ? `This backup completed, with ${warnings} warning(s).`
            : `This backup has reported ${warnings} warning(s) so far. It is ${veleroPhaseLabel(phase).toLowerCase()}, so this is not a final count.`}
        />
      )}

      {/* Status section */}
      <Section title="Status" icon={Archive} defaultExpanded>
        <PropertyList>
          <Property label="Phase" value={
            <VeleroPhaseValue status={backupStatus} phase={phase} />
          } />
          {queuePosition !== null && (
            <Property label="Queue Position" value={String(queuePosition)} />
          )}
          {status.startTimestamp && (
            <Property label="Started" value={<TimeValue timestamp={status.startTimestamp} />} />
          )}
          {status.completionTimestamp && (
            <Property label="Completed" value={<TimeValue timestamp={status.completionTimestamp} />} />
          )}
          <Property label="Duration" value={getBackupDuration(data)} />
          <Property label="Expiration" value={getBackupExpiry(data)} />
          <Property label="Errors" value={
            errors > 0
              ? <span className="text-red-500 dark:text-red-400 font-medium">{errors}</span>
              : '0'
          } />
          <Property label="Warnings" value={
            warnings > 0
              ? <span className="text-amber-500 dark:text-amber-400 font-medium">{warnings}</span>
              : '0'
          } />
        </PropertyList>
        {messages && <VeleroRunMessages {...messages} errors={errors} warnings={warnings} />}
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
                <span className="inline-code break-all">
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
          {/* Velero writes the location into spec.storageLocation when it
              first processes the backup. Until then the default it will pick
              may still change, so naming today's default would be a guess. */}
          <Property
            label="Storage Location"
            value={(() => {
              const name = getBackupStorageLocation(data)
              if (!name) {
                return (
                  <span className="text-sm text-theme-text-tertiary">
                    Not assigned yet
                  </span>
                )
              }
              return (
                <span className="flex items-center gap-2">
                  {/* Not a link when the object is not there: a link to nothing
                      is worse than a name. */}
                  {storageLocationMissing ? (
                    <span className="text-sm font-mono text-theme-text-primary">{name}</span>
                  ) : (
                    <ResourceLink
                      name={name}
                      kind="BackupStorageLocation"
                      namespace={data?.metadata?.namespace ?? ''}
                      group="velero.io"
                      onNavigate={onNavigate}
                    />
                  )}
                  {storageLocationMissing && (
                    <span className={clsx('badge', HEALTH_BADGE_COLORS.unhealthy)}>Not found</span>
                  )}
                  {storageLocationPhase && storageLocationPhase !== 'Available' && (
                    <span className={clsx('badge', HEALTH_BADGE_COLORS.unhealthy)}>{storageLocationPhase}</span>
                  )}
                </span>
              )
            })()}
          />
          {/* A location that is gone is not a location that is unhealthy, and
              Velero writes no phase for one that does not exist — so without
              this the backup renders exactly as it would on a healthy bucket. */}
          {storageLocationMissing && (
            <div className="text-xs text-warning-text">
              This backup names a storage location that no longer exists in this namespace. Velero restores from the location recorded on the backup, so there is nothing here to restore from until it is recreated.
            </div>
          )}
          {/* Only when it changes the answer. On a healthy location this would be
              a warning about nothing, on every backup in the cluster. */}
          {storageLocationPhase && storageLocationPhase !== 'Available' && (
            <div className="text-xs text-warning-text">
              {`This backup is stored in a location Velero reports as ${storageLocationPhase}. Its own phase does not change when that happens — until the location recovers, there is nothing here to restore from.`}
            </div>
          )}
          {vslLocations.length > 0 && (
            <Property label="Volume Snapshot Locations" value={
              <div className="flex flex-wrap gap-1">
                {vslLocations.map((loc: string) => (
                  <span key={loc} className="badge-sm bg-theme-hover text-theme-text-secondary">
                    <ResourceLink
                      name={loc}
                      kind="VolumeSnapshotLocation"
                      namespace={data?.metadata?.namespace ?? ''}
                      group="velero.io"
                      onNavigate={onNavigate}
                    />
                  </span>
                ))}
              </div>
            } />
          )}
        </PropertyList>
      </Section>

      {/* Options section */}
      <Section title="Options" icon={Clock}>
        <PropertyList>
          <Property label="Kept for" value={veleroDeclaredRetention(data?.spec?.ttl) ?? VELERO_SERVER_DEFAULT_RETENTION} />
          <Property label="Item Operation Timeout" value={getBackupItemOperationTimeout(data)} />
          <Property label="Snapshot Volumes" value={getBackupSnapshotVolumes(data)} />
          <Property label="Default FS Backup" value={getBackupDefaultVolumesToFsBackup(data)} />
        </PropertyList>
      </Section>

      <ConditionsSection conditions={conditions} />
    </>
  )
}
