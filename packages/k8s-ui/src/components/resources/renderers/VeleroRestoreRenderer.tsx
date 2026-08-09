import { ArchiveRestore, Filter } from 'lucide-react'
import { Section, PropertyList, Property, ConditionsSection, AlertBanner } from '../../ui/drawer-components'
import {
  getRestoreStatus,
  getRestoreBackupName,
  getRestoreIncludedNamespaces,
  getRestoreExcludedNamespaces,
  getRestoreIncludedResources,
  getRestoreExcludedResources,
  getRestoreDuration,
  getRestoreErrors,
  getRestoreWarnings,
  getRestorePVs,
  getRestoreExistingResourcePolicy,
  getRestoreValidationErrors,
  isBackupActivePhase,
  isBackupPartialFailurePhase,
} from '../resource-utils-velero'
import { VeleroPhaseValue } from './velero-cells'
import { formatAge } from '../resource-utils'

interface VeleroRestoreRendererProps {
  data: any
}

export function VeleroRestoreRenderer({ data }: VeleroRestoreRendererProps) {
  const status = data.status || {}
  const conditions = status.conditions || []

  const restoreStatus = getRestoreStatus(data)
  const errors = getRestoreErrors(data)
  const warnings = getRestoreWarnings(data)
  const includedNamespaces = getRestoreIncludedNamespaces(data)
  const excludedNamespaces = getRestoreExcludedNamespaces(data)
  const includedResources = getRestoreIncludedResources(data)
  const excludedResources = getRestoreExcludedResources(data)

  const phase = status.phase || ''
  const validationErrors = getRestoreValidationErrors(data)
  const isFailed = restoreStatus.level === 'unhealthy'
  const isValidationFailure = phase === 'FailedValidation'
  const isPartiallyFailed = isBackupPartialFailurePhase(phase)
  const isInProgress = isBackupActivePhase(phase)

  // Progress data
  const progress = status.progress
  const itemsRestored = progress?.itemsRestored ?? 0
  const totalItems = progress?.totalItems ?? 0
  const progressPercent = totalItems > 0 ? Math.round((itemsRestored / totalItems) * 100) : 0

  return (
    <>
      {/* Problem alerts */}
      {isValidationFailure && (
        <AlertBanner
          variant="error"
          title="Restore Validation Failed"
          message={status.failureReason || 'Velero rejected this restore before it started — nothing was restored.'}
          items={validationErrors.length > 0 ? validationErrors : undefined}
        />
      )}
      {isFailed && !isValidationFailure && (
        <AlertBanner
          variant="error"
          title="Restore Failed"
          message={status.failureReason || `${errors} error(s) occurred during restore.`}
          items={validationErrors.length > 0 ? validationErrors : undefined}
        />
      )}
      {isPartiallyFailed && (
        <AlertBanner
          variant="warning"
          title="Restore Partially Failed"
          message={status.failureReason || `${errors} error(s) occurred — some items were not restored.`}
          items={validationErrors.length > 0 ? validationErrors : undefined}
        />
      )}
      {warnings > 0 && !isFailed && !isPartiallyFailed && (
        <AlertBanner
          variant="warning"
          title={`${warnings} Warning(s)`}
          message={`Restore completed with ${warnings} warning(s).`}
        />
      )}

      {/* Status section */}
      <Section title="Status" icon={ArchiveRestore} defaultExpanded>
        <PropertyList>
          <Property label="Phase" value={
            <VeleroPhaseValue status={restoreStatus} phase={phase} />
          } />
          <Property label="Backup" value={getRestoreBackupName(data)} />
          {status.startTimestamp && (
            <Property label="Started" value={formatAge(status.startTimestamp) + ' ago'} />
          )}
          {status.completionTimestamp && (
            <Property label="Completed" value={formatAge(status.completionTimestamp) + ' ago'} />
          )}
          <Property label="Duration" value={getRestoreDuration(data)} />
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
      </Section>

      {/* Progress section (if in progress) */}
      {isInProgress && progress && (
        <Section title="Progress" defaultExpanded>
          <div className="space-y-2">
            <div className="flex items-center justify-between text-sm">
              <span className="text-theme-text-secondary">Items restored</span>
              <span className="text-theme-text-primary font-medium">{itemsRestored}/{totalItems}</span>
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
      {(includedNamespaces.length > 0 || excludedNamespaces.length > 0 || includedResources.length > 0 || excludedResources.length > 0) && (
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
          </PropertyList>
        </Section>
      )}

      {/* Options section */}
      <Section title="Options">
        <PropertyList>
          <Property label="Restore PVs" value={getRestorePVs(data)} />
          <Property label="Existing Resource Policy" value={getRestoreExistingResourcePolicy(data)} />
        </PropertyList>
      </Section>

      <ConditionsSection conditions={conditions} />
    </>
  )
}
