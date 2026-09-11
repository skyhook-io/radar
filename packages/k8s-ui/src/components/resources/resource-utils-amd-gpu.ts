// AMD GPU Operator CRD utility functions

import type { StatusBadge } from './resource-utils'
import { healthColors } from './resource-utils'

// ============================================================================
// AMD DEVICECONFIG UTILITIES
// ============================================================================

export function getAMDDeviceConfigStatus(resource: any): StatusBadge {
  const status = resource.status
  if (!status) {
    return { text: 'Unknown', color: healthColors.unknown, level: 'unknown' }
  }

  const ready = Array.isArray(status.conditions)
    ? status.conditions.find((condition: any) => condition?.type === 'Ready')
    : undefined
  if (ready?.status === 'False') {
    return { text: ready.reason || 'NotReady', color: healthColors.unhealthy, level: 'unhealthy' }
  }
  if (ready?.status === 'True') {
    return { text: 'Ready', color: healthColors.healthy, level: 'healthy' }
  }

  const components = [status.driver, status.devicePlugin, status.metricsExporter, status.configManager]
    .filter((c: any) => c && (c.desiredNumber ?? 0) > 0)

  if (components.length === 0) {
    return { text: 'Unknown', color: healthColors.unknown, level: 'unknown' }
  }

  const allReady = components.every((c: any) => (c.availableNumber ?? 0) >= (c.desiredNumber ?? 0))
  if (allReady) {
    return { text: 'Ready', color: healthColors.healthy, level: 'healthy' }
  }
  return { text: 'Degraded', color: healthColors.degraded, level: 'degraded' }
}

export function getAMDDeviceConfigDriver(resource: any): string {
  const driver = resource.spec?.driver
  if (!driver) return '-'
  if (driver.enable === true) {
    return driver.version ? `Enabled (${driver.version})` : 'Enabled'
  }
  if (driver.enable === false) return 'Disabled'
  return '-'
}

export function getAMDDeviceConfigDevicePluginImage(resource: any): string {
  return resource.spec?.devicePlugin?.devicePluginImage || '-'
}

export function getAMDDeviceConfigNodeCount(resource: any): string {
  const dp = resource.status?.devicePlugin || resource.status?.driver
  if (!dp) return '-'
  const available = dp.availableNumber ?? 0
  const desired = dp.desiredNumber ?? 0
  if (desired === 0) return '-'
  return `${available}/${desired}`
}
