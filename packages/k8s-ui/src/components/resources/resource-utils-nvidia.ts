// NVIDIA GPU Operator (nvidia.com) utility functions.
// ClusterPolicy here is nvidia.com's — distinct from Kyverno's ClusterPolicy;
// dispatch call sites guard on apiVersion before reaching these.

import type { StatusBadge } from './resource-utils'
import { healthColors } from './resource-utils'

function stateBadge(state: string | undefined): StatusBadge {
  switch (state) {
    case 'ready':
      return { text: 'Ready', color: healthColors.healthy, level: 'healthy' }
    case 'notReady':
      return { text: 'Not Ready', color: healthColors.alert, level: 'alert' }
    case 'disabled':
      return { text: 'Disabled', color: healthColors.neutral, level: 'neutral' }
    default:
      return { text: state || 'Unknown', color: healthColors.unknown, level: 'unknown' }
  }
}

export function getNvidiaClusterPolicyStatus(resource: any): StatusBadge {
  return stateBadge(resource?.status?.state)
}

export const NVIDIA_CLUSTER_POLICY_COMPONENTS = [
  { key: 'driver', label: 'Driver' },
  { key: 'toolkit', label: 'Container Toolkit' },
  { key: 'devicePlugin', label: 'Device Plugin' },
  { key: 'dcgmExporter', label: 'DCGM Exporter' },
  { key: 'dcgm', label: 'DCGM' },
  { key: 'gfd', label: 'GPU Feature Discovery' },
  { key: 'migManager', label: 'MIG Manager' },
  { key: 'nodeStatusExporter', label: 'Node Status Exporter' },
  { key: 'vgpuManager', label: 'vGPU Manager' },
  { key: 'gds', label: 'GPUDirect Storage' },
] as const

export function getNvidiaClusterPolicyEnabledComponents(resource: any): { label: string; enabled: boolean }[] {
  const spec = resource?.spec || {}
  return NVIDIA_CLUSTER_POLICY_COMPONENTS
    .filter(({ key }) => spec[key]?.enabled !== undefined)
    .map(({ key, label }) => ({ label, enabled: !!spec[key].enabled }))
}

export function getNvidiaClusterPolicyMigStrategy(resource: any): string {
  return resource?.spec?.mig?.strategy || '-'
}

export function getNvidiaDriverStatus(resource: any): StatusBadge {
  return stateBadge(resource?.status?.state)
}

export function getNvidiaDriverType(resource: any): string {
  return resource?.spec?.driverType || '-'
}

export function getNvidiaDriverVersion(resource: any): string {
  return resource?.spec?.version || '-'
}

export function isNvidiaResource(resource: any): boolean {
  return !!resource?.apiVersion?.startsWith('nvidia.com/')
}
