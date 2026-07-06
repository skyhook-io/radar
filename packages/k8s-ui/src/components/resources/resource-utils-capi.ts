// Cluster API (CAPI) CRD utility functions

import type { StatusBadge } from './resource-utils'
import { healthColors } from './resource-utils'

// ============================================================================
// SHARED CAPI UTILITIES
// ============================================================================

// CAPI phase-to-health mapping
const PHASE_MAP: Record<string, { text: string; level: StatusBadge['level'] }> = {
  provisioned: { text: 'Provisioned', level: 'healthy' },
  running: { text: 'Running', level: 'healthy' },
  scaled: { text: 'Scaled', level: 'healthy' },
  provisioning: { text: 'Provisioning', level: 'degraded' },
  pending: { text: 'Pending', level: 'degraded' },
  scaling: { text: 'Scaling', level: 'degraded' },
  upgrading: { text: 'Upgrading', level: 'degraded' },
  deleting: { text: 'Deleting', level: 'degraded' },
  failed: { text: 'Failed', level: 'unhealthy' },
}

export function getCAPIConditions(resource: any): any[] {
  // v1beta2 uses status.v1beta2.conditions, v1beta1 uses status.conditions
  return resource.status?.v1beta2?.conditions || resource.status?.conditions || []
}

export function getCAPIReadyCondition(resource: any): any | undefined {
  const conditions = getCAPIConditions(resource)
  return conditions.find((c: any) => c.type === 'Ready') || conditions.find((c: any) => c.type === 'Available')
}

function getCAPIPhaseStatus(resource: any): StatusBadge {
  const phase = resource.status?.phase?.toLowerCase()
  if (phase && PHASE_MAP[phase]) {
    const m = PHASE_MAP[phase]
    return { text: m.text, color: healthColors[m.level], level: m.level }
  }

  // Fall back to Ready condition
  const readyCond = getCAPIReadyCondition(resource)
  if (readyCond?.status === 'True') {
    return { text: 'Ready', color: healthColors.healthy, level: 'healthy' }
  }
  if (readyCond?.status === 'False') {
    return { text: readyCond.reason || 'NotReady', color: healthColors.unhealthy, level: 'unhealthy' }
  }
  return { text: 'Unknown', color: healthColors.unknown, level: 'unknown' }
}

export function getCAPIReadyStatus(resource: any): StatusBadge {
  const readyCond = getCAPIReadyCondition(resource)
  if (readyCond?.status === 'True') {
    return { text: 'Ready', color: healthColors.healthy, level: 'healthy' }
  }
  if (readyCond?.status === 'False') {
    return { text: readyCond.reason || 'NotReady', color: healthColors.unhealthy, level: 'unhealthy' }
  }
  return { text: 'Unknown', color: healthColors.unknown, level: 'unknown' }
}

// Normalize v1beta1 updatedReplicas vs v1beta2 upToDateReplicas
function getUpToDateReplicas(resource: any): number {
  return resource.status?.upToDateReplicas ?? resource.status?.updatedReplicas ?? 0
}

// ============================================================================
// CAPI CLUSTER UTILITIES
// ============================================================================

export function getClusterStatus(resource: any): StatusBadge {
  return getCAPIPhaseStatus(resource)
}

export function getClusterClass(resource: any): string {
  return resource.spec?.topology?.class || '-'
}

export function getClusterVersion(resource: any): string {
  return resource.spec?.topology?.version || '-'
}

export function getClusterCPReplicas(resource: any): string {
  // v1beta2: status.controlPlane.readyReplicas / status.controlPlane.desiredReplicas
  const cpReady = resource.status?.controlPlane?.readyReplicas ?? resource.status?.controlPlaneReady
  const cpDesired = resource.status?.controlPlane?.desiredReplicas
  if (cpDesired != null) return `${cpReady ?? 0}/${cpDesired}`
  return typeof cpReady === 'boolean' ? (cpReady ? 'Ready' : 'NotReady') : '-'
}

export function getClusterWorkerReplicas(resource: any): string {
  // v1beta2: status.workers.readyReplicas / status.workers.desiredReplicas
  const wReady = resource.status?.workers?.readyReplicas ?? resource.status?.workersReady
  const wDesired = resource.status?.workers?.desiredReplicas
  if (wDesired != null) return `${wReady ?? 0}/${wDesired}`
  return typeof wReady === 'boolean' ? (wReady ? 'Ready' : 'NotReady') : '-'
}

export function getClusterEndpoint(resource: any): string {
  const host = resource.spec?.controlPlaneEndpoint?.host
  const port = resource.spec?.controlPlaneEndpoint?.port
  if (host) return port ? `${host}:${port}` : host
  return '-'
}

// ============================================================================
// CAPI MACHINE UTILITIES
// ============================================================================

export function getMachineStatus(resource: any): StatusBadge {
  return getCAPIPhaseStatus(resource)
}

export function getMachineRole(resource: any): string {
  const labels = resource.metadata?.labels || {}
  if (labels['cluster.x-k8s.io/control-plane'] !== undefined) return 'Control Plane'
  if (labels['cluster.x-k8s.io/control-plane-name']) return 'Control Plane'
  return 'Worker'
}

export function getMachineClusterName(resource: any): string {
  return resource.metadata?.labels?.['cluster.x-k8s.io/cluster-name'] || resource.spec?.clusterName || '-'
}

export function getMachineNodeRef(resource: any): string {
  return resource.status?.nodeRef?.name || '-'
}

export function getMachineVersion(resource: any): string {
  return resource.spec?.version || '-'
}

export function getMachineProviderID(resource: any): string {
  return resource.spec?.providerID || '-'
}

// ============================================================================
// PROVIDER DETECTION UTILITIES
// ============================================================================

export interface InfraProvider {
  provider: string        // 'AWS' | 'GCP' | 'Azure' | 'vSphere' | 'Docker' | unknown
  region?: string         // availability zone or region
  instanceId?: string     // cloud instance identifier
}

/** Parse spec.providerID to extract cloud provider, region, and instance ID */
export function parseProviderID(providerID: string): InfraProvider | null {
  if (!providerID || providerID === '-') return null

  // AWS: aws:///us-east-1a/i-0abcdef1234567890 (or aws://us-east-1a/...)
  if (providerID.startsWith('aws://')) {
    const parts = providerID.replace(/^aws:\/\/\/?/, '').split('/')
    return { provider: 'AWS', region: parts[0] || undefined, instanceId: parts[1] || undefined }
  }

  // GCP: gce:///my-project/us-central1-a/my-instance (or gce://...)
  if (providerID.startsWith('gce://')) {
    const parts = providerID.replace(/^gce:\/\/\/?/, '').split('/')
    return { provider: 'GCP', region: parts[1] || undefined, instanceId: parts[2] || undefined }
  }

  // Azure: azure:///subscriptions/{sub}/resourceGroups/{rg}/providers/Microsoft.Compute/virtualMachines/{name}
  if (providerID.startsWith('azure://')) {
    const vmMatch = providerID.match(/virtualMachines\/([^/]+)/)
    const rgMatch = providerID.match(/resourceGroups\/([^/]+)/)
    return { provider: 'Azure', region: rgMatch?.[1], instanceId: vmMatch?.[1] }
  }

  // vSphere: vsphere://42305a8e-...
  if (providerID.startsWith('vsphere://')) {
    return { provider: 'vSphere', instanceId: providerID.replace(/^vsphere:\/\/\/?/, '') }
  }

  // Docker (CAPD): docker:///container-name (or docker://...)
  if (providerID.startsWith('docker://')) {
    return { provider: 'Docker', instanceId: providerID.replace(/^docker:\/\/\/?/, '') }
  }

  return { provider: providerID.split(':')[0] || 'Unknown' }
}

/** Detect provider from an infrastructureRef kind name */
export function getProviderFromInfraKind(kind: string): string {
  const k = kind.toLowerCase()
  if (k.startsWith('aws') || k.startsWith('ec2')) return 'AWS'
  if (k.startsWith('gcp') || k.startsWith('gce')) return 'GCP'
  if (k.startsWith('azure') || k.startsWith('aks')) return 'Azure'
  if (k.startsWith('vsphere')) return 'vSphere'
  if (k.startsWith('docker')) return 'Docker'
  if (k.startsWith('metal') || k.startsWith('byoh')) return 'Bare Metal'
  return kind // Return raw kind as fallback
}

// ============================================================================
// CAPI MACHINEDEPLOYMENT UTILITIES
// ============================================================================

export function getMachineDeploymentStatus(resource: any): StatusBadge {
  return getCAPIPhaseStatus(resource)
}

export function getMachineDeploymentReplicas(resource: any): string {
  const desired = resource.spec?.replicas ?? 0
  const ready = resource.status?.readyReplicas ?? 0
  return `${ready}/${desired}`
}

export function getMachineDeploymentVersion(resource: any): string {
  return resource.spec?.template?.spec?.version || '-'
}

export function getMachineDeploymentUpToDate(resource: any): string {
  return String(getUpToDateReplicas(resource))
}

// ============================================================================
// CAPI KUBEADMCONTROLPLANE UTILITIES
// ============================================================================

export function getKCPStatus(resource: any): StatusBadge {
  return getCAPIReadyStatus(resource)
}

export function getKCPReplicas(resource: any): string {
  const desired = resource.spec?.replicas ?? 0
  const ready = resource.status?.readyReplicas ?? 0
  return `${ready}/${desired}`
}

export function getKCPVersion(resource: any): string {
  return resource.spec?.version || '-'
}

export function getKCPInitialized(resource: any): boolean {
  // v1beta2: status.initialization.controlPlaneInitialized; v1beta1: status.initialized
  return resource.status?.initialization?.controlPlaneInitialized ?? resource.status?.initialized ?? false
}

// ============================================================================
// CAPI MACHINESET UTILITIES
// ============================================================================

export function getMachineSetStatus(resource: any): StatusBadge {
  return getCAPIPhaseStatus(resource)
}

export function getMachineSetReplicas(resource: any): string {
  const desired = resource.spec?.replicas ?? 0
  const ready = resource.status?.readyReplicas ?? 0
  return `${ready}/${desired}`
}

// ============================================================================
// CAPI MACHINEPOOL UTILITIES
// ============================================================================

export function getMachinePoolStatus(resource: any): StatusBadge {
  return getCAPIPhaseStatus(resource)
}

export function getMachinePoolReplicas(resource: any): string {
  const desired = resource.spec?.replicas ?? 0
  const ready = resource.status?.readyReplicas ?? 0
  return `${ready}/${desired}`
}

// ============================================================================
// CAPI CLUSTERCLASS UTILITIES
// ============================================================================

export function getClusterClassStatus(resource: any): StatusBadge {
  return getCAPIReadyStatus(resource)
}

// ============================================================================
// CAPI MACHINEHEALTHCHECK UTILITIES
// ============================================================================

export function getMachineHealthCheckStatus(resource: any): StatusBadge {
  return getCAPIReadyStatus(resource)
}

export function getMachineHealthCheckClusterName(resource: any): string {
  return resource.spec?.clusterName || resource.metadata?.labels?.['cluster.x-k8s.io/cluster-name'] || '-'
}

export function getMachineHealthCheckHealthy(resource: any): string {
  const expected = resource.status?.expectedMachines ?? 0
  const healthy = resource.status?.currentHealthy ?? 0
  return `${healthy}/${expected}`
}

export function getClusterProvider(resource: any): string {
  const infraKind = resource.spec?.infrastructureRef?.kind || ''
  if (infraKind) return getProviderFromInfraKind(infraKind)
  return '-'
}

/** Parse CAPI compound condition messages ("* Foo: bar * Baz: qux") into a structured list */
export function parseCAPIConditionMessage(message: string): string[] | null {
  if (!message || !message.includes('*')) return null
  const items = message.split('*').map(s => s.trim()).filter(Boolean)
  return items.length > 1 ? items : null
}
