// AMD GPU Operator cell components for ResourcesView table

import { clsx } from 'clsx'
import {
  getAMDDeviceConfigStatus,
  getAMDDeviceConfigDriver,
  getAMDDeviceConfigDevicePluginImage,
  getAMDDeviceConfigNodeCount,
} from '../resource-utils-amd-gpu'

export function AMDDeviceConfigCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'status': {
      const status = getAMDDeviceConfigStatus(resource)
      return (
        <span className={clsx('badge', status.color)}>
          {status.text}
        </span>
      )
    }
    case 'driver': {
      const driver = getAMDDeviceConfigDriver(resource)
      return <span className="text-sm text-theme-text-secondary">{driver}</span>
    }
    case 'devicePluginImage': {
      const image = getAMDDeviceConfigDevicePluginImage(resource)
      return <span className="text-sm text-theme-text-secondary truncate block">{image}</span>
    }
    case 'nodes': {
      const nodes = getAMDDeviceConfigNodeCount(resource)
      return <span className="text-sm text-theme-text-secondary">{nodes}</span>
    }
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}
