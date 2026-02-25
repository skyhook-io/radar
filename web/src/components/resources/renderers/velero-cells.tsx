// Velero cell components for ResourcesView table

import { clsx } from 'clsx'
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
  getScheduleCron,
  getScheduleLastBackup,
  getSchedulePaused,
  getBSLStatus,
  getBSLProvider,
  getBSLBucket,
  getBSLDefault,
  getBSLLastValidation,
} from '../resource-utils-velero'

export function BackupCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'status': {
      const status = getBackupStatus(resource)
      return (
        <span className={clsx('inline-flex items-center px-2 py-0.5 rounded text-xs font-medium', status.color)}>
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
        <span className={clsx('inline-flex items-center px-2 py-0.5 rounded text-xs font-medium', status.color)}>
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
      return (
        <span className={clsx('inline-flex items-center px-2 py-0.5 rounded text-xs font-medium', status.color)}>
          {status.text}
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
    case 'paused': {
      const paused = getSchedulePaused(resource)
      return <span className={clsx('text-sm', paused ? 'text-yellow-400' : 'text-theme-text-tertiary')}>{paused ? 'Yes' : '-'}</span>
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
        <span className={clsx('inline-flex items-center px-2 py-0.5 rounded text-xs font-medium', status.color)}>
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
