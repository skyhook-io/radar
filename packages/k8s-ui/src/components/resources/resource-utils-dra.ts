// DRA (Dynamic Resource Allocation, resource.k8s.io) utility functions.
// Accessors tolerate all served shapes: v1/v1beta2 nest device-class refs
// under requests[].exactly (or firstAvailable subrequests); v1beta1 had
// deviceClassName directly on the request.

import type { StatusBadge } from './resource-utils'
import { healthColors } from './resource-utils'

function requestDeviceClasses(requests: any[]): string[] {
  const seen = new Set<string>()
  for (const req of requests || []) {
    const direct = req?.exactly?.deviceClassName || req?.deviceClassName
    if (direct) seen.add(direct)
    for (const sub of req?.firstAvailable || []) {
      if (sub?.deviceClassName) seen.add(sub.deviceClassName)
    }
  }
  return [...seen]
}

export function getResourceClaimDeviceClasses(resource: any): string[] {
  return requestDeviceClasses(resource?.spec?.devices?.requests)
}

export function getResourceClaimTemplateDeviceClasses(resource: any): string[] {
  return requestDeviceClasses(resource?.spec?.spec?.devices?.requests)
}

export function getResourceClaimAllocation(resource: any): { driver: string; pool: string; device: string }[] {
  const results = resource?.status?.allocation?.devices?.results || []
  return results.map((r: any) => ({
    driver: r?.driver || '',
    pool: r?.pool || '',
    device: r?.device || '',
  }))
}

export function getResourceClaimReservedFor(resource: any): { resource: string; name: string }[] {
  return (resource?.status?.reservedFor || []).map((r: any) => ({
    resource: r?.resource || r?.apiGroup || '',
    name: r?.name || '',
  }))
}

export function getResourceClaimStatus(resource: any): StatusBadge {
  // Allocated means devices were actually assigned — keyed to devices.results
  // so the badge always agrees with the Allocation section.
  const allocated = (resource?.status?.allocation?.devices?.results || []).length > 0
  const reserved = (resource?.status?.reservedFor || []).length > 0
  if (allocated && reserved) {
    return { text: 'Allocated', color: healthColors.healthy, level: 'healthy' }
  }
  if (allocated) {
    // Allocated-but-unreserved is normal transiently; long-lived it leaks a device
    return { text: 'Unreserved', color: healthColors.degraded, level: 'degraded' }
  }
  return { text: 'Pending', color: healthColors.unknown, level: 'unknown' }
}

export function getResourceClaimTemplateStatus(_resource: any): StatusBadge {
  return { text: 'Template', color: healthColors.neutral, level: 'neutral' }
}

export function getDeviceClassStatus(_resource: any): StatusBadge {
  return { text: 'Available', color: healthColors.neutral, level: 'neutral' }
}

export function getDeviceClassSelectorCount(resource: any): number {
  return (resource?.spec?.selectors || []).length
}

export function getResourceSliceStatus(_resource: any): StatusBadge {
  return { text: 'Published', color: healthColors.neutral, level: 'neutral' }
}

export function getResourceSliceDriver(resource: any): string {
  return resource?.spec?.driver || '-'
}

export function getResourceSlicePool(resource: any): string {
  return resource?.spec?.pool?.name || '-'
}

export function getResourceSliceNode(resource: any): string {
  return resource?.spec?.nodeName || ''
}

export function getResourceSliceDeviceCount(resource: any): number {
  return (resource?.spec?.devices || []).length
}
