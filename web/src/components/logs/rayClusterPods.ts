import { isKubeRayClusterIdentity } from '@skyhook-io/k8s-ui/components/resources/resource-utils-ray'
import { ApiError, useCapabilities } from '../../api/client'
import { useNamespacedCapabilities } from '../../contexts/CapabilitiesContext'
import type { RadarFeatureSupport } from '../../api/radarFeatures'

export function isKubeRayCluster(kind: string, group: string | undefined, apiVersion: string | undefined): boolean {
  return kind === 'RayCluster' && isKubeRayClusterIdentity(group, apiVersion)
}

export function useSidecarsMarked(): boolean {
  return useCapabilities().data?.features?.podSidecarMarks === true
}

// What a failed read of a RayCluster's Pods means. Radars before v1.15 reject
// the kind with "only deployments, …"; when the version can't place the Radar,
// say which Radar lists RayCluster Pods instead of echoing that. A current
// Radar's own 400s pass through.
export function rayClusterPodsError(error: unknown, support: RadarFeatureSupport): string | undefined {
  if (!error) return undefined
  if (error instanceof ApiError && error.status === 400 && support !== 'supported' && /^only deployments/.test(error.message)) return 'This Radar may not list a RayCluster\'s Pods; Radar v1.15 and later do.'
  return error instanceof Error ? error.message : 'The Pod list could not be read.'
}

// Whether this viewer can read Pod logs in a namespace. Unknown while the
// namespace answer is pending or failed, or when no capabilities answer ever
// arrived (a failed refetch keeps the last answer).
export function useLogsReadability(namespace: string): 'allowed' | 'denied' | 'unknown' {
  const { canViewLogs, logsUnknown } = useNamespacedCapabilities(namespace)
  const capabilities = useCapabilities()
  if (canViewLogs) return 'allowed'
  return logsUnknown || (capabilities.isError && !capabilities.data) ? 'unknown' : 'denied'
}
