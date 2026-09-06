// CloudNativePG cell components for ResourcesView table

import { clsx } from 'clsx'
import { Tooltip } from '../../ui/Tooltip'
import {
  getCNPGClusterStatus,
  getCNPGClusterAvailability,
  classifyCNPGClusterPhase,
  getCNPGClusterInstances,
  getCNPGClusterPrimary,
  getCNPGClusterImageTag,
  getCNPGClusterStorage,
  getCNPGBackupStatus,
  getCNPGBackupCluster,
  getCNPGBackupMethod,
  getCNPGBackupDuration,
  getCNPGBackupStartedAt,
  getCNPGScheduledBackupStatus,
  getCNPGScheduledBackupCluster,
  getCNPGScheduleCron,
  getCNPGScheduledBackupLastSchedule,
  getCNPGScheduledBackupIsSuspended,
  getCNPGPoolerStatus,
  getCNPGPoolerCluster,
  getCNPGPoolerType,
  getCNPGPoolerMode,
  getCNPGPoolerInstances,
  getCNPGObjectStoreStatus,
  getCNPGObjectStoreDestination,
  getCNPGObjectStoreRetention,
  getCNPGObjectStoreRecoveryWindows,
  getCNPGDeclarativeStatus,
  getCNPGDeclarativeCluster,
  getCNPGDeclarativeMessage,
  getCNPGImageCatalogEntries,
} from '../resource-utils-cnpg'

/** Terminal = the operator has stopped reconciling; neighbouring cells are stale. */
function isTerminalPhase(resource: any): boolean {
  return classifyCNPGClusterPhase(resource?.status?.phase || '') === 'terminal'
}

export function CNPGClusterCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'status': {
      const status = getCNPGClusterStatus(resource)
      return (
        <span className={clsx('badge', status.color)}>
          {status.text}
        </span>
      )
    }
    case 'instances': {
      const instances = getCNPGClusterInstances(resource)
      const desired = resource.spec?.instances ?? 0
      const readyKnown = typeof resource.status?.readyInstances === 'number'
      const ready = readyKnown ? resource.status.readyInstances : 0
      // A terminal cluster still reports a ready count, and the count is true —
      // unrecoverable at 2/2 means the data is probably intact, at 0/2 it is down
      // too. That distinction is worth keeping, so the cell is muted rather than
      // hidden. Muting alone reads as "less important"; the tooltip is what says
      // "this does not mean what you think".
      if (isTerminalPhase(resource)) {
        return (
          <Tooltip content="Reconciliation has stopped — this count reflects the pods still running, not a working cluster.">
            <span className="text-sm text-theme-text-tertiary">{instances}</span>
          </Tooltip>
        )
      }
      // Colour tracks the badge: a was-up total outage (readyInstances omitted,
      // so readyKnown is false) reads red like its "Not Ready" badge rather than
      // muted grey, and a partial shortfall stays yellow. Without the
      // availability check the most severe count was the least emphasised.
      const down = getCNPGClusterAvailability(resource) === 'down'
      return (
        <span
          className={clsx(
            'text-sm',
            down ? 'text-red-400' : readyKnown && ready < desired ? 'text-yellow-400' : 'text-theme-text-secondary',
          )}
        >
          {instances}
        </span>
      )
    }
    case 'primary': {
      const primary = getCNPGClusterPrimary(resource)
      // status.currentPrimary is LAST KNOWN — CNPG never clears it. Naming a
      // primary on a cluster that cannot elect one is an assertion Radar makes,
      // not one CNPG makes, so say so rather than presenting it as current.
      if (isTerminalPhase(resource)) {
        return (
          <Tooltip content="Last known primary — this cluster cannot currently elect one.">
            <span className="text-sm text-theme-text-tertiary truncate block">{primary}</span>
          </Tooltip>
        )
      }
      return <span className="text-sm text-theme-text-secondary truncate block">{primary}</span>
    }
    case 'image': {
      const tag = getCNPGClusterImageTag(resource)
      return <span className="text-sm text-theme-text-secondary font-mono">{tag}</span>
    }
    case 'storage': {
      const storage = getCNPGClusterStorage(resource)
      return <span className="text-sm text-theme-text-secondary">{storage}</span>
    }
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}

export function CNPGBackupCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'status': {
      const status = getCNPGBackupStatus(resource)
      return (
        <span className={clsx('badge', status.color)}>
          {status.text}
        </span>
      )
    }
    case 'cluster': {
      const cluster = getCNPGBackupCluster(resource)
      return <span className="text-sm text-theme-text-secondary">{cluster}</span>
    }
    case 'method': {
      const method = getCNPGBackupMethod(resource)
      return <span className="text-sm text-theme-text-secondary">{method}</span>
    }
    case 'started': {
      const started = getCNPGBackupStartedAt(resource)
      return <span className="text-sm text-theme-text-secondary">{started}</span>
    }
    case 'duration': {
      const duration = getCNPGBackupDuration(resource)
      return <span className="text-sm text-theme-text-secondary">{duration}</span>
    }
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}

export function CNPGDeclarativeCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'status': {
      const status = getCNPGDeclarativeStatus(resource)
      return <span className={clsx('badge', status.color)}>{status.text}</span>
    }
    case 'cluster':
      return (
        <span className="text-sm text-theme-text-secondary truncate block">
          {getCNPGDeclarativeCluster(resource) || '-'}
        </span>
      )
    case 'target':
      return (
        <span className="text-sm text-theme-text-secondary truncate block">
          {resource?.spec?.name || '-'}
        </span>
      )
    case 'dbname':
      return (
        <span className="text-sm text-theme-text-secondary truncate block">
          {resource?.spec?.dbname || resource?.spec?.owner || '-'}
        </span>
      )
    case 'message':
      return (
        <span
          className="text-sm text-theme-text-secondary truncate block"
          title={getCNPGDeclarativeMessage(resource) || ''}
        >
          {getCNPGDeclarativeMessage(resource) || '-'}
        </span>
      )
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}

export function CNPGImageCatalogCell({ resource, column }: { resource: any; column: string }) {
  const entries = getCNPGImageCatalogEntries(resource)
  switch (column) {
    case 'majors':
      return (
        <span className="text-sm text-theme-text-secondary">
          {entries.length === 0 ? 'none' : entries.map((e) => e.major).join(', ')}
        </span>
      )
    case 'images':
      return <span className="text-sm text-theme-text-secondary">{String(entries.length)}</span>
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}

export function CNPGObjectStoreCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'status': {
      const status = getCNPGObjectStoreStatus(resource)
      return <span className={clsx('badge', status.color)}>{status.text}</span>
    }
    case 'destination':
      // `block` is load-bearing: an inline span with `truncate` establishes no
      // box, so the column collapses to almost nothing regardless of its width.
      // Every sibling cell that renders a long value pairs the two.
      return (
        <span className="text-sm text-theme-text-secondary truncate block">
          {getCNPGObjectStoreDestination(resource)}
        </span>
      )
    case 'retention':
      return (
        <span className="text-sm text-theme-text-secondary">
          {getCNPGObjectStoreRetention(resource) || '-'}
        </span>
      )
    case 'servers': {
      const windows = getCNPGObjectStoreRecoveryWindows(resource)
      if (windows.length === 0) {
        return <span className="text-sm text-theme-text-tertiary">none</span>
      }
      const failing = windows.filter((w) => w.failingSinceLastSuccess).length
      return (
        <span className="text-sm text-theme-text-secondary">
          {failing > 0 ? `${windows.length} (${failing} failing)` : String(windows.length)}
        </span>
      )
    }
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}

export function CNPGScheduledBackupCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'status': {
      const status = getCNPGScheduledBackupStatus(resource)
      return (
        <span className={clsx('badge', status.color)}>
          {status.text}
        </span>
      )
    }
    case 'cluster': {
      const cluster = getCNPGScheduledBackupCluster(resource)
      return <span className="text-sm text-theme-text-secondary">{cluster}</span>
    }
    case 'schedule': {
      const cron = getCNPGScheduleCron(resource)
      return <span className="text-sm text-theme-text-secondary font-mono">{cron}</span>
    }
    case 'lastSchedule': {
      const last = getCNPGScheduledBackupLastSchedule(resource)
      return <span className="text-sm text-theme-text-secondary">{last}</span>
    }
    case 'suspended': {
      const suspended = getCNPGScheduledBackupIsSuspended(resource)
      return (
        <span className={clsx('text-sm', suspended ? 'text-yellow-400' : 'text-theme-text-tertiary')}>
          {suspended ? 'Yes' : '-'}
        </span>
      )
    }
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}

export function CNPGPoolerCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'status': {
      const status = getCNPGPoolerStatus(resource)
      return (
        <span className={clsx('badge', status.color)}>
          {status.text}
        </span>
      )
    }
    case 'cluster': {
      const cluster = getCNPGPoolerCluster(resource)
      return <span className="text-sm text-theme-text-secondary">{cluster}</span>
    }
    case 'type': {
      const type = getCNPGPoolerType(resource)
      return (
        <span className={clsx(
          'badge-sm',
          type === 'rw' ? 'bg-blue-500/20 text-blue-400' : 'bg-purple-500/20 text-purple-400'
        )}>
          {type}
        </span>
      )
    }
    case 'poolMode': {
      const mode = getCNPGPoolerMode(resource)
      return <span className="text-sm text-theme-text-secondary">{mode}</span>
    }
    case 'instances': {
      const instances = getCNPGPoolerInstances(resource)
      return <span className="text-sm text-theme-text-secondary">{instances}</span>
    }
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}
