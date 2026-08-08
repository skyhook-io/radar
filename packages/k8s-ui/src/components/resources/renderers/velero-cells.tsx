// Velero cell components for ResourcesView table

import { clsx } from 'clsx'
import { Pause } from 'lucide-react'
import { Tooltip } from '../../ui/Tooltip'
import {
  getBackupStatus,
  getBackupStorageLocation,
  getBackupIncludedNamespaces,
  getBackupDuration,
  getBackupExpiry,
  getBackupErrors,
  getBackupWarnings,
  getRestoreStatus,
  getRestoreBackupName,
  getRestoreIncludedNamespaces,
  getRestoreDuration,
  getRestoreErrors,
  getScheduleStatus,
  getSchedulePaused,
  getScheduleCron,
  getScheduleLastBackup,
  getBSLStatus,
  getBSLProvider,
  getBSLBucket,
  getBSLDefault,
  getBSLLastValidation,
  getVSLProvider,
  getVSLConfig,
  getBackupRepositoryStatus,
  getBackupRepositoryType,
} from '../resource-utils-velero'

export function BackupCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'status': {
      const status = getBackupStatus(resource)
      return (
        <span className={clsx('badge', status.color)}>
          {status.text}
        </span>
      )
    }
    case 'storageLocation': {
      const loc = getBackupStorageLocation(resource)
      return <span className="text-sm text-theme-text-secondary truncate block">{loc}</span>
    }
    case 'namespaces': {
      const ns = getBackupIncludedNamespaces(resource)
      return <span className="text-sm text-theme-text-secondary">{ns.length > 0 ? ns.length : '*'}</span>
    }
    case 'duration': {
      const dur = getBackupDuration(resource)
      return <span className="text-sm text-theme-text-secondary">{dur}</span>
    }
    case 'expiry': {
      const exp = getBackupExpiry(resource)
      const isExpired = exp === 'Expired'
      return <span className={clsx('text-sm', isExpired ? 'text-red-400' : 'text-theme-text-secondary')}>{exp}</span>
    }
    case 'errors': {
      const errors = getBackupErrors(resource)
      const warnings = getBackupWarnings(resource)
      if (errors === 0 && warnings === 0) {
        return <span className="text-sm text-theme-text-tertiary">-</span>
      }
      return (
        <span className="text-sm">
          {errors > 0 && <span className="text-red-400">{errors}E</span>}
          {errors > 0 && warnings > 0 && <span className="text-theme-text-tertiary"> / </span>}
          {warnings > 0 && <span className="text-yellow-400">{warnings}W</span>}
        </span>
      )
    }
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}

export function RestoreCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'status': {
      const status = getRestoreStatus(resource)
      return (
        <span className={clsx('badge', status.color)}>
          {status.text}
        </span>
      )
    }
    case 'backupName': {
      const name = getRestoreBackupName(resource)
      return <span className="text-sm text-theme-text-secondary truncate block">{name}</span>
    }
    case 'namespaces': {
      const ns = getRestoreIncludedNamespaces(resource)
      return <span className="text-sm text-theme-text-secondary">{ns.length > 0 ? ns.length : '*'}</span>
    }
    case 'duration': {
      const dur = getRestoreDuration(resource)
      return <span className="text-sm text-theme-text-secondary">{dur}</span>
    }
    case 'errors': {
      const errors = getRestoreErrors(resource)
      if (errors === 0) {
        return <span className="text-sm text-theme-text-tertiary">-</span>
      }
      return <span className="text-sm text-red-400">{errors}</span>
    }
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}

export function ScheduleCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'status': {
      const status = getScheduleStatus(resource)
      // A paused schedule that is *also* rejected carries two independent facts
      // and the badge can only show one. The failure takes the badge because it
      // is the one to act on; paused rides alongside as a chip — the same
      // primary-plus-secondary shape CellContent uses for Terminating in the
      // name column. A chip rather than a second word is what keeps it inside
      // the column budget: "Rejected" plus a spelled-out "Paused" does not fit.
      const alsoPaused = getSchedulePaused(resource) && status.text !== 'Paused'
      return (
        <span className="inline-flex items-center gap-1 min-w-0">
          <span className={clsx('badge', status.color)}>
            {status.text}
          </span>
          {alsoPaused && (
            <Tooltip content="Also paused — this schedule will not run until it is resumed">
              <Pause className="w-3 h-3 shrink-0 text-amber-600 dark:text-amber-400" />
            </Tooltip>
          )}
        </span>
      )
    }
    case 'schedule': {
      const cron = getScheduleCron(resource)
      return <span className="text-sm text-theme-text-secondary font-mono">{cron}</span>
    }
    case 'lastBackup': {
      const last = getScheduleLastBackup(resource)
      return <span className="text-sm text-theme-text-secondary">{last}</span>
    }
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}

export function BackupRepositoryCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'status': {
      const status = getBackupRepositoryStatus(resource)
      return (
        <span className={clsx('badge', status.color)}>
          {status.text}
        </span>
      )
    }
    case 'repositoryType': {
      const type = getBackupRepositoryType(resource)
      // restic is on its way out (no new backups since v1.17, restore dropped in
      // v1.19), so flag it rather than rendering it as neutral configuration.
      const isLegacy = type === 'restic'
      return (
        <span className={clsx('text-sm', isLegacy ? 'text-amber-600 dark:text-amber-400 font-medium' : 'text-theme-text-secondary')}>
          {type}
        </span>
      )
    }
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}

// VolumeSnapshotLocation has no status column on purpose: the VSL controller
// does not populate status.phase, so a badge would read "Unknown" forever.
export function VolumeSnapshotLocationCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'provider': {
      const provider = getVSLProvider(resource)
      return <span className="text-sm text-theme-text-secondary truncate block">{provider}</span>
    }
    case 'config': {
      const config = getVSLConfig(resource)
      const keys = Object.keys(config)
      if (keys.length === 0) {
        return <span className="text-sm text-theme-text-tertiary">-</span>
      }
      const summary = keys.map((k) => `${k}=${config[k]}`).join(', ')
      return <span className="text-sm text-theme-text-secondary truncate block" title={summary}>{summary}</span>
    }
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}

export function BackupStorageLocationCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'status': {
      const status = getBSLStatus(resource)
      return (
        <span className={clsx('badge', status.color)}>
          {status.text}
        </span>
      )
    }
    case 'provider': {
      const provider = getBSLProvider(resource)
      return <span className="text-sm text-theme-text-secondary">{provider}</span>
    }
    case 'bucket': {
      const bucket = getBSLBucket(resource)
      return <span className="text-sm text-theme-text-secondary truncate block">{bucket}</span>
    }
    case 'default': {
      const isDefault = getBSLDefault(resource)
      return <span className={clsx('text-sm', isDefault ? 'text-blue-400' : 'text-theme-text-tertiary')}>{isDefault ? 'Yes' : '-'}</span>
    }
    case 'lastValidation': {
      const lastVal = getBSLLastValidation(resource)
      return <span className="text-sm text-theme-text-secondary">{lastVal}</span>
    }
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}
