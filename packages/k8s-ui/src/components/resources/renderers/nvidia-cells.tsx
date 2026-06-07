// NVIDIA GPU Operator (nvidia.com) cell components for ResourcesView table

import { clsx } from 'clsx'
import {
  getNvidiaClusterPolicyStatus,
  getNvidiaClusterPolicyEnabledComponents,
  getNvidiaClusterPolicyMigStrategy,
  getNvidiaDriverStatus,
  getNvidiaDriverType,
  getNvidiaDriverVersion,
} from '../resource-utils-nvidia'

export function NvidiaClusterPolicyCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'status': {
      const status = getNvidiaClusterPolicyStatus(resource)
      return <span className={clsx('badge', status.color)}>{status.text}</span>
    }
    case 'components': {
      const enabled = getNvidiaClusterPolicyEnabledComponents(resource).filter(c => c.enabled)
      return <span className="text-sm text-theme-text-secondary truncate block">{enabled.length ? enabled.map(c => c.label).join(', ') : '-'}</span>
    }
    case 'mig':
      return <span className="text-sm text-theme-text-secondary">{getNvidiaClusterPolicyMigStrategy(resource)}</span>
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}

export function NvidiaDriverCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'status': {
      const status = getNvidiaDriverStatus(resource)
      return <span className={clsx('badge', status.color)}>{status.text}</span>
    }
    case 'driverType':
      return <span className="text-sm text-theme-text-secondary">{getNvidiaDriverType(resource)}</span>
    case 'version':
      return <span className="text-sm text-theme-text-secondary">{getNvidiaDriverVersion(resource)}</span>
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}
