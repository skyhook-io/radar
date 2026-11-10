import { VeleroRunMessages, type VeleroRunMessagesFetch } from './VeleroRunMessages'
import { Archive, Clock, HardDrive, Filter } from 'lucide-react'
import { clsx } from 'clsx'
import { HEALTH_BADGE_COLORS } from '../../../utils/badge-colors'
import { Section, PropertyList, Property, ConditionsSection, AlertBanner, ResourceLink } from '../../ui/drawer-components'
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
  getBackupTTL,
  getBackupItemOperationTimeout,
  getBackupSnapshotVolumes,
  getBackupDefaultVolumesToFsBackup,
  getBackupVolumeSnapshotLocations,
  getBackupQueuePosition,
  isBackupActivePhase,
  isBackupPartialFailurePhase,
} from '../resource-utils-velero'
import { VeleroPhaseValue } from './velero-cells'
import { veleroPhaseLabel } from '../resource-utils-velero'
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
   * The location this backup is actually stored in, resolved by the host
   * against the location list. An unset `spec.storageLocation` means "whichever
   * location carries spec.default", which this component cannot see.
   *
   * Undefined means the lookup has not answered. A backup that names its own
   * location is still known by name in that state; one that does not is not
   * known at all, and this page says so rather than printing the conventional
   * name as though it had been resolved.
   */
  storageLocationName?: string
  /**
   * The lookup answered and no location by that name exists. Distinct from an
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

export function VeleroBackupRenderer({ data, storageLocationPhase, storageLocationName, storageLocationMissing, messages, onNavigate }: VeleroBackupRendererProps) {
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
      {isValidationFailure && (
        <AlertBanner
          variant="error"
          title="Backup Validation Failed"
          message={status.failureReason || 'Velero rejected this backup before it started — nothing was backed up.'}
          items={validationErrors.length > 0 ? validationErrors : undefined}
        />
      )}
      {isFailed && !isValidationFailure && (
        <AlertBanner
          variant="error"
          title="Backup Failed"
          message={status.failureReason || `${errors} error(s) occurred during backup.`}
          items={validationErrors.length > 0 ? validationErrors : undefined}
        />
      )}
      {isPartiallyFailed && (
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
          {/* A backup that names its location is known by name whatever the
              lookup is doing. One that does not is only known once the list
              answers, because "unset" means whichever location carries
              spec.default — so until then this says what the field says and
              claims nothing further. */}
          <Property
            label="Storage Location"
            value={(() => {
              const declared = data?.spec?.storageLocation as string | undefined
              const name = storageLocationName || declared
              if (!name) {
                return (
                  <span className="text-sm text-theme-text-tertiary">
                    Velero&rsquo;s default location, not yet resolved
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
                    <span className={clsx('badge', HEALTH_BADGE_COLORS.unhealthy)}>
                      {declared ? 'Not found' : 'No default location'}
                    </span>
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
              this the backup renders exactly as it would on a healthy bucket.
              Worded from where the name came from: a backup that names nothing
              has not "named a location that no longer exists", and saying so
              would attribute to the object a name the fallback invented. */}
          {storageLocationMissing && (
            <div className="text-xs text-warning-text">
              {data?.spec?.storageLocation
                ? 'This backup names a storage location that no longer exists in this namespace. Velero restores from the location recorded on the backup, so there is nothing here to restore from until it is recreated.'
                : 'This backup names no storage location, which Velero resolves to the one marked default — and this namespace has none. Which location holds it cannot be determined here.'}
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
          <Property label="TTL" value={getBackupTTL(data)} />
          <Property label="Item Operation Timeout" value={getBackupItemOperationTimeout(data)} />
          <Property label="Snapshot Volumes" value={getBackupSnapshotVolumes(data)} />
          <Property label="Default FS Backup" value={getBackupDefaultVolumesToFsBackup(data)} />
        </PropertyList>
      </Section>

      <ConditionsSection conditions={conditions} />
    </>
  )
}
