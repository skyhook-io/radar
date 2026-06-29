// DRA (resource.k8s.io) cell components for ResourcesView table

import { clsx } from 'clsx'
import {
  getResourceClaimStatus,
  getResourceClaimDeviceClasses,
  getResourceClaimAllocation,
  getResourceClaimReservedFor,
  getResourceClaimTemplateDeviceClasses,
  getDeviceClassSelectorCount,
  getResourceSliceDriver,
  getResourceSlicePool,
  getResourceSliceNode,
  getResourceSliceDeviceCount,
} from '../resource-utils-dra'

export function ResourceClaimCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'status': {
      const status = getResourceClaimStatus(resource)
      return <span className={clsx('badge', status.color)}>{status.text}</span>
    }
    case 'deviceClass': {
      const classes = getResourceClaimDeviceClasses(resource)
      return <span className="text-sm text-theme-text-secondary truncate block">{classes.join(', ') || '-'}</span>
    }
    case 'allocated': {
      const results = getResourceClaimAllocation(resource)
      if (results.length === 0) return <span className="text-sm text-theme-text-tertiary">-</span>
      return <span className="text-sm text-theme-text-secondary truncate block">{results[0].driver}{results.length > 1 ? ` +${results.length - 1}` : ''}</span>
    }
    case 'reservedFor': {
      const reserved = getResourceClaimReservedFor(resource)
      if (reserved.length === 0) return <span className="text-sm text-theme-text-tertiary">-</span>
      return <span className="text-sm text-theme-text-secondary truncate block">{reserved.map(r => r.name).join(', ')}</span>
    }
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}

export function ResourceClaimTemplateCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'deviceClass': {
      const classes = getResourceClaimTemplateDeviceClasses(resource)
      return <span className="text-sm text-theme-text-secondary truncate block">{classes.join(', ') || '-'}</span>
    }
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}

export function DeviceClassCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'selectors': {
      // 0 is meaningful — a class with no selectors matches all devices
      const count = getDeviceClassSelectorCount(resource)
      return <span className="text-sm text-theme-text-secondary">{count}</span>
    }
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}

export function ResourceSliceCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'driver':
      return <span className="text-sm text-theme-text-secondary truncate block">{getResourceSliceDriver(resource)}</span>
    case 'pool':
      return <span className="text-sm text-theme-text-secondary truncate block">{getResourceSlicePool(resource)}</span>
    case 'node': {
      const node = getResourceSliceNode(resource)
      return <span className="text-sm text-theme-text-secondary truncate block">{node || '-'}</span>
    }
    case 'devices':
      return <span className="text-sm text-theme-text-secondary">{getResourceSliceDeviceCount(resource)}</span>
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}
