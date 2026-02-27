// Utility functions for resource display in tables

import { formatCPUString, formatMemoryString } from '../../utils/format'

// Import functions from sub-modules used internally by getCellFilterValue
import { getCertificateStatus, getCertificateRequestStatus, getClusterIssuerStatus, getClusterIssuerType, getOrderState, getChallengeState, getChallengeType } from './resource-utils-certmanager'
import { getNodePoolStatus, getNodeClaimStatus } from './resource-utils-karpenter'
import { getScaledObjectStatus, getScaledJobStatus } from './resource-utils-keda'
import { getGitRepositoryStatus, getOCIRepositoryStatus, getHelmRepositoryStatus, getHelmRepositoryType, getKustomizationStatus, getFluxHelmReleaseStatus, getFluxAlertStatus } from './resource-utils-flux'
import { getArgoApplicationStatus, getArgoApplicationSetStatus } from './resource-utils-argo'
import { getPolicyReportStatus as _getPolicyReportStatus, getKyvernoPolicyStatus as _getKyvernoPolicyStatus } from './resource-utils-kyverno'
import { getBackupStatus as _getBackupStatus, getRestoreStatus as _getRestoreStatus, getScheduleStatus as _getScheduleStatus, getBSLStatus as _getBSLStatus } from './resource-utils-velero'
import { getExternalSecretStatus as _getExternalSecretStatus, getClusterExternalSecretStatus as _getClusterExternalSecretStatus, getSecretStoreStatus as _getSecretStoreStatus, getClusterSecretStoreStatus as _getClusterSecretStoreStatus } from './resource-utils-eso'

// ============================================================================
// STATUS & HEALTH UTILITIES
// ============================================================================

export type HealthLevel = 'healthy' | 'degraded' | 'unhealthy' | 'unknown' | 'neutral'

export interface StatusBadge {
  text: string
  color: string
  level: HealthLevel
}

// Color classes for different health levels (theme-aware)
export const healthColors: Record<HealthLevel, string> = {
  healthy: 'status-healthy',
  degraded: 'status-degraded',
  unhealthy: 'status-unhealthy',
  unknown: 'status-unknown',
  neutral: 'status-neutral',
}

// ============================================================================
// POD UTILITIES
// ============================================================================

export interface PodProblem {
  severity: 'critical' | 'high' | 'medium'
  message: string
}

export function getPodStatus(pod: any): StatusBadge {
  const phase = pod.status?.phase || 'Unknown'
  const containerStatuses = pod.status?.containerStatuses || []

  // Check for terminating
  if (pod.metadata?.deletionTimestamp) {
    return { text: 'Terminating', color: healthColors.degraded, level: 'degraded' }
  }

  // Check container states for issues
  for (const cs of containerStatuses) {
    if (cs.state?.waiting?.reason) {
      const reason = cs.state.waiting.reason
      if (['CrashLoopBackOff', 'ImagePullBackOff', 'ErrImagePull', 'CreateContainerConfigError'].includes(reason)) {
        return { text: reason, color: healthColors.unhealthy, level: 'unhealthy' }
      }
    }
    if (cs.state?.terminated?.reason === 'OOMKilled') {
      return { text: 'OOMKilled', color: healthColors.unhealthy, level: 'unhealthy' }
    }
  }

  switch (phase) {
    case 'Running':
      // Check if all containers are ready
      const ready = containerStatuses.filter((c: any) => c.ready).length
      const total = containerStatuses.length
      if (total > 0 && ready < total) {
        return { text: `Running (${ready}/${total})`, color: healthColors.degraded, level: 'degraded' }
      }
      return { text: 'Running', color: healthColors.healthy, level: 'healthy' }
    case 'Succeeded':
      return { text: 'Completed', color: healthColors.neutral, level: 'neutral' }
    case 'Pending':
      return { text: 'Pending', color: healthColors.degraded, level: 'degraded' }
    case 'Failed':
      return { text: 'Failed', color: healthColors.unhealthy, level: 'unhealthy' }
    default:
      return { text: phase, color: healthColors.unknown, level: 'unknown' }
  }
}

export function getPodProblems(pod: any): PodProblem[] {
  const problems: PodProblem[] = []
  const containerStatuses = pod.status?.containerStatuses || []
  const conditions = pod.status?.conditions || []

  for (const cs of containerStatuses) {
    // Check waiting state
    if (cs.state?.waiting?.reason) {
      const reason = cs.state.waiting.reason
      if (['CrashLoopBackOff', 'ImagePullBackOff', 'ErrImagePull'].includes(reason)) {
        problems.push({ severity: 'critical', message: reason })
      } else if (reason === 'CreateContainerConfigError') {
        problems.push({ severity: 'critical', message: 'Config Error' })
      } else if (reason === 'ContainerCannotRun') {
        problems.push({ severity: 'critical', message: 'Cannot Run' })
      }
    }
    // Check terminated state
    if (cs.state?.terminated?.reason === 'OOMKilled') {
      problems.push({ severity: 'critical', message: 'OOMKilled' })
    }
    // High restart count
    if (cs.restartCount > 5) {
      problems.push({ severity: 'medium', message: `${cs.restartCount} restarts` })
    }
    // Volume mount issues from last state
    const lastMsg = cs.lastState?.terminated?.message?.toLowerCase() || ''
    if (lastMsg.includes('failed to mount') || lastMsg.includes('failedattachvolume')) {
      problems.push({ severity: 'high', message: 'Volume Mount Failed' })
    }
  }

  // Check conditions
  for (const cond of conditions) {
    if (cond.type === 'PodScheduled' && cond.status === 'False') {
      if (cond.reason === 'Unschedulable') {
        problems.push({ severity: 'high', message: 'Unschedulable' })
      }
    }
    // Readiness/Liveness probe failures
    if (cond.type === 'ContainersReady' && cond.status === 'False') {
      const msg = (cond.message || '').toLowerCase()
      if (msg.includes('readiness')) {
        problems.push({ severity: 'medium', message: 'Readiness Probe Failing' })
      } else if (msg.includes('liveness')) {
        problems.push({ severity: 'high', message: 'Liveness Probe Failing' })
      }
    }
    // IP allocation failures (subnet exhaustion)
    if (cond.type === 'PodReadyToStartContainers' && cond.status === 'False') {
      const msg = (cond.message || '').toLowerCase()
      if (msg.includes('failed to assign an ip') || msg.includes('pod sandbox')) {
        problems.push({ severity: 'critical', message: 'IP Allocation Failed' })
      }
    }
  }

  // Evicted pods
  if (pod.status?.phase === 'Failed' && pod.status?.reason === 'Evicted') {
    problems.push({ severity: 'high', message: 'Evicted' })
  }

  // Stuck terminating (zombie pod)
  if (pod.metadata?.deletionTimestamp) {
    const deleteTime = new Date(pod.metadata.deletionTimestamp).getTime()
    const ageSeconds = (Date.now() - deleteTime) / 1000
    if (ageSeconds > 60) {
      problems.push({ severity: 'medium', message: 'Stuck Terminating' })
    }
  }

  // Not ready (Running but containers not ready)
  const phase = pod.status?.phase
  if (phase === 'Running') {
    const readyContainers = containerStatuses.filter((c: any) => c.ready).length
    const totalContainers = containerStatuses.length
    if (totalContainers > 0 && readyContainers < totalContainers) {
      // Only add if we haven't already flagged a more specific issue
      const hasSpecificIssue = problems.some(p =>
        p.message.includes('Probe') || p.message.includes('CrashLoop') || p.message.includes('OOM')
      )
      if (!hasSpecificIssue) {
        problems.push({ severity: 'medium', message: 'Not Ready' })
      }
    }
  }

  return problems
}

export function getPodReadiness(pod: any): { ready: number; total: number } {
  const containerStatuses = pod.status?.containerStatuses || []
  const initContainerStatuses = pod.status?.initContainerStatuses || []

  // Check init containers first
  const initNotComplete = initContainerStatuses.filter((c: any) =>
    !c.state?.terminated || c.state.terminated.exitCode !== 0
  ).length

  if (initNotComplete > 0 && initContainerStatuses.length > 0) {
    const initComplete = initContainerStatuses.length - initNotComplete
    return { ready: initComplete, total: initContainerStatuses.length }
  }

  const ready = containerStatuses.filter((c: any) => c.ready).length
  return { ready, total: containerStatuses.length || pod.spec?.containers?.length || 0 }
}

export function getPodRestarts(pod: any): number {
  const containerStatuses = pod.status?.containerStatuses || []
  return containerStatuses.reduce((sum: number, c: any) => sum + (c.restartCount || 0), 0)
}

// ============================================================================
// WORKLOAD UTILITIES (Deployment, StatefulSet, DaemonSet, ReplicaSet)
// ============================================================================

export function getWorkloadStatus(resource: any, kind: string): StatusBadge {
  const status = resource.status || {}
  const spec = resource.spec || {}

  if (kind === 'daemonsets') {
    const desired = status.desiredNumberScheduled || 0
    const ready = status.numberReady || 0
    const updated = status.updatedNumberScheduled || 0

    if (desired === 0) return { text: '0 nodes', color: healthColors.unknown, level: 'unknown' }
    if (ready === desired && updated === desired) {
      return { text: `${ready}/${desired}`, color: healthColors.healthy, level: 'healthy' }
    }
    if (ready > 0) {
      return { text: `${ready}/${desired}`, color: healthColors.degraded, level: 'degraded' }
    }
    return { text: `${ready}/${desired}`, color: healthColors.unhealthy, level: 'unhealthy' }
  }

  // Deployment, StatefulSet, ReplicaSet
  const desired = spec.replicas ?? status.replicas ?? 0
  const ready = status.readyReplicas || 0
  const updated = status.updatedReplicas || 0
  const available = status.availableReplicas || 0

  if (desired === 0) {
    return { text: 'Scaled to 0', color: healthColors.neutral, level: 'neutral' }
  }

  // Check if updating
  if (updated < desired && updated > 0) {
    return { text: `Updating ${updated}/${desired}`, color: healthColors.degraded, level: 'degraded' }
  }

  if (ready === desired && available === desired) {
    return { text: `${ready}/${desired}`, color: healthColors.healthy, level: 'healthy' }
  }
  if (ready > 0) {
    return { text: `${ready}/${desired}`, color: healthColors.degraded, level: 'degraded' }
  }
  return { text: `${ready}/${desired}`, color: healthColors.unhealthy, level: 'unhealthy' }
}

export function getWorkloadImages(resource: any): string[] {
  const containers = resource.spec?.template?.spec?.containers || []
  return containers.map((c: any) => {
    const image = c.image || ''
    // Extract just image:tag, remove registry prefix
    const parts = image.split('/')
    return parts[parts.length - 1] || image
  })
}

export function getWorkloadConditions(resource: any): { conditions: string[]; hasIssues: boolean } {
  const conditions = resource.status?.conditions || []
  const activeConditions: string[] = []
  let hasIssues = false

  for (const cond of conditions) {
    if (cond.status === 'True') {
      activeConditions.push(cond.type)
      // Progressing and Available are good, others might indicate issues
      if (!['Progressing', 'Available'].includes(cond.type)) {
        hasIssues = true
      }
    } else if (cond.status === 'False') {
      // Available=False is an issue
      if (cond.type === 'Available') {
        hasIssues = true
      }
    }
  }

  return { conditions: activeConditions, hasIssues }
}

export function getReplicaSetOwner(rs: any): string | null {
  const ownerRefs = rs.metadata?.ownerReferences || []
  const owner = ownerRefs[0]
  if (owner) {
    return `${owner.kind}/${owner.name}`
  }
  return null
}

export function isReplicaSetActive(rs: any): boolean {
  const replicas = rs.spec?.replicas || 0
  return replicas > 0
}

// ============================================================================
// SERVICE UTILITIES
// ============================================================================

export function getServiceStatus(service: any): StatusBadge {
  const type = service.spec?.type || 'ClusterIP'
  const color = type === 'LoadBalancer' || type === 'NodePort'
    ? 'status-violet'
    : healthColors.neutral
  return { text: type, color, level: 'neutral' }
}

export function getServicePorts(service: any): string {
  const ports = service.spec?.ports || []
  if (ports.length === 0) return '-'
  if (ports.length <= 2) {
    return ports.map((p: any) => {
      const port = p.port
      const target = p.targetPort !== p.port ? `:${p.targetPort}` : ''
      const proto = p.protocol !== 'TCP' ? `/${p.protocol}` : ''
      return `${port}${target}${proto}`
    }).join(', ')
  }
  return `${ports.length} ports`
}

export function getServiceExternalIP(service: any): string | null {
  const type = service.spec?.type
  if (type === 'LoadBalancer') {
    const ingress = service.status?.loadBalancer?.ingress || []
    // Show all IPs, not just first
    if (ingress.length > 0) {
      const ips = ingress.map((i: any) => i.ip || i.hostname).filter(Boolean)
      if (ips.length > 2) return `${ips[0]} +${ips.length - 1}`
      return ips.join(', ') || null
    }
    return 'Pending'
  }
  if (type === 'NodePort') {
    const ports = service.spec?.ports || []
    const nodePorts = ports.map((p: any) => p.nodePort).filter(Boolean)
    if (nodePorts.length > 0) {
      return `NodePort: ${nodePorts.join(', ')}`
    }
  }
  // Also check spec.externalIPs (can be set on any service type)
  if (service.spec?.externalIPs?.length > 0) {
    const ips = service.spec.externalIPs
    if (ips.length > 2) return `${ips[0]} +${ips.length - 1}`
    return ips.join(', ')
  }
  return null
}

export function getServiceSelector(service: any): string {
  // For ExternalName services, show the external DNS name
  if (service.spec?.type === 'ExternalName') {
    return service.spec.externalName || '-'
  }
  const selector = service.spec?.selector || {}
  const pairs = Object.entries(selector).map(([k, v]) => `${k}=${v}`)
  if (pairs.length === 0) return 'None'
  if (pairs.length <= 2) return pairs.join(', ')
  return `${pairs.slice(0, 2).join(', ')} +${pairs.length - 2}`
}

export function getServiceEndpointsStatus(service: any): { status: string; color: string } {
  const type = service.spec?.type
  if (type === 'ExternalName') {
    return { status: 'External', color: 'status-violet' }
  }
  const selector = service.spec?.selector || {}
  const hasSelector = Object.keys(selector).length > 0
  if (!hasSelector) {
    return { status: 'None', color: 'status-unknown' }
  }
  // If it has a selector, it should have endpoints (we assume active since we can't check endpoints from service alone)
  return { status: 'Active', color: 'status-healthy' }
}

// ============================================================================
// INGRESS UTILITIES
// ============================================================================

export function getIngressStatus(ingress: any): StatusBadge {
  const lbIngress = ingress.status?.loadBalancer?.ingress || []
  if (lbIngress.length > 0) {
    return { text: 'Active', color: healthColors.healthy, level: 'healthy' }
  }
  return { text: 'Pending', color: healthColors.degraded, level: 'degraded' }
}

export function getIngressHosts(ingress: any): string {
  const rules = ingress.spec?.rules || []
  const hosts = rules.map((r: any) => r.host).filter(Boolean)
  if (hosts.length === 0) return '*'
  if (hosts.length <= 2) return hosts.join(', ')
  return `${hosts[0]} +${hosts.length - 1} more`
}

export function getIngressClass(ingress: any): string | null {
  return ingress.spec?.ingressClassName ||
    ingress.metadata?.annotations?.['kubernetes.io/ingress.class'] ||
    null
}

export function hasIngressTLS(ingress: any): boolean {
  return (ingress.spec?.tls?.length || 0) > 0
}

export function getIngressAddress(ingress: any): string | null {
  const lbIngress = ingress.status?.loadBalancer?.ingress || []
  if (lbIngress.length > 0) {
    return lbIngress[0].ip || lbIngress[0].hostname || null
  }
  return null
}

export function getIngressRules(ingress: any): string {
  const rules = ingress.spec?.rules || []
  if (rules.length === 0) return 'No rules'

  const formattedRules = rules.map((rule: any) => {
    const host = rule.host || '*'
    const paths = rule.http?.paths || []
    if (paths.length === 0) return host

    const pathMappings = paths.map((p: any) => {
      const path = p.path || '/'
      // Support both new (service.name) and legacy (serviceName) formats
      const backend = p.backend?.service?.name || (p.backend as any)?.serviceName || 'unknown'
      return `${path}→${backend}`
    })

    if (pathMappings.length === 1) {
      return `${host}: ${pathMappings[0]}`
    }
    return `${host}: ${pathMappings.join(', ')}`
  })

  if (formattedRules.length === 1) return formattedRules[0]
  if (formattedRules.length === 2) return formattedRules.join('; ')
  return `${formattedRules[0]}; +${formattedRules.length - 1} more`
}

// ============================================================================
// CONFIGMAP / SECRET UTILITIES
// ============================================================================

export function getConfigMapKeys(cm: any): { count: number; preview: string } {
  const data = cm.data || {}
  const binaryData = cm.binaryData || {}
  const keys = [...Object.keys(data), ...Object.keys(binaryData)]
  const count = keys.length

  if (count === 0) return { count: 0, preview: 'Empty' }
  if (count <= 3) return { count, preview: keys.join(', ') }
  return { count, preview: `${keys.slice(0, 2).join(', ')} +${count - 2}` }
}

export function getConfigMapSize(cm: any): string {
  const data = cm.data || {}
  const binaryData = cm.binaryData || {}

  let totalBytes = 0
  for (const value of Object.values(data)) {
    totalBytes += (value as string).length
  }
  for (const value of Object.values(binaryData)) {
    // Base64 encoded, actual size is ~75% of encoded
    totalBytes += Math.floor((value as string).length * 0.75)
  }

  return formatBytes(totalBytes)
}

export function getSecretType(secret: any): { type: string; color: string } {
  const type = secret.type || 'Opaque'
  const typeMap: Record<string, { type: string; color: string }> = {
    'Opaque': { type: 'Opaque', color: 'status-unknown' },
    'kubernetes.io/tls': { type: 'TLS', color: 'status-neutral' },
    'kubernetes.io/dockercfg': { type: 'Docker', color: 'status-purple' },
    'kubernetes.io/dockerconfigjson': { type: 'Docker', color: 'status-purple' },
    'kubernetes.io/basic-auth': { type: 'Basic Auth', color: 'status-orange' },
    'kubernetes.io/ssh-auth': { type: 'SSH', color: 'status-cyan' },
    'kubernetes.io/service-account-token': { type: 'SA Token', color: 'status-healthy' },
    'bootstrap.kubernetes.io/token': { type: 'Bootstrap', color: 'status-healthy' },
  }
  return typeMap[type] || { type: type.split('/').pop() || type, color: 'status-unknown' }
}

export function getSecretKeyCount(secret: any): number {
  const data = secret.data || {}
  return Object.keys(data).length
}

// ============================================================================
// JOB / CRONJOB UTILITIES
// ============================================================================

export function getJobStatus(job: any): StatusBadge {
  const status = job.status || {}
  const conditions = status.conditions || []

  // Check conditions first
  const failedCond = conditions.find((c: any) => c.type === 'Failed' && c.status === 'True')
  if (failedCond) {
    return { text: 'Failed', color: healthColors.unhealthy, level: 'unhealthy' }
  }

  const completeCond = conditions.find((c: any) => c.type === 'Complete' && c.status === 'True')
  if (completeCond) {
    return { text: 'Complete', color: healthColors.healthy, level: 'healthy' }
  }

  if (job.spec?.suspend) {
    return { text: 'Suspended', color: healthColors.degraded, level: 'degraded' }
  }

  if (status.active > 0) {
    return { text: 'Running', color: healthColors.neutral, level: 'neutral' }
  }

  return { text: 'Pending', color: healthColors.degraded, level: 'degraded' }
}

export function getJobCompletions(job: any): { succeeded: number; total: number } {
  const completions = job.spec?.completions || 1
  const succeeded = job.status?.succeeded || 0
  return { succeeded, total: completions }
}

export function getJobDuration(job: any): string | null {
  const startTime = job.status?.startTime
  const completionTime = job.status?.completionTime

  if (!startTime) return null

  const start = new Date(startTime)
  const end = completionTime ? new Date(completionTime) : new Date()
  const durationMs = end.getTime() - start.getTime()

  return formatDuration(durationMs)
}

export function getCronJobStatus(cj: any): StatusBadge {
  if (cj.spec?.suspend) {
    return { text: 'Suspended', color: healthColors.degraded, level: 'degraded' }
  }
  const activeJobs = cj.status?.active?.length || 0
  if (activeJobs > 0) {
    return { text: `Active (${activeJobs})`, color: healthColors.neutral, level: 'neutral' }
  }
  return { text: 'Scheduled', color: healthColors.healthy, level: 'healthy' }
}

export function getCronJobSchedule(cj: any): { cron: string; readable: string } {
  const schedule = cj.spec?.schedule || ''
  return { cron: schedule, readable: cronToHuman(schedule) }
}

export function getCronJobLastRun(cj: any): string | null {
  const lastSchedule = cj.status?.lastScheduleTime
  if (!lastSchedule) return null
  return formatAge(lastSchedule)
}

// ============================================================================
// HPA UTILITIES
// ============================================================================

export function getHPAStatus(hpa: any): StatusBadge {
  const current = hpa.status?.currentReplicas || 0
  const desired = hpa.status?.desiredReplicas || 0

  if (current === desired) {
    return { text: 'Stable', color: healthColors.healthy, level: 'healthy' }
  }
  if (current < desired) {
    return { text: 'Scaling Up', color: healthColors.degraded, level: 'degraded' }
  }
  return { text: 'Scaling Down', color: healthColors.degraded, level: 'degraded' }
}

export function getHPAReplicas(hpa: any): { current: number; min: number; max: number } {
  return {
    current: hpa.status?.currentReplicas || 0,
    min: hpa.spec?.minReplicas || 1,
    max: hpa.spec?.maxReplicas || 0,
  }
}

export function getHPATarget(hpa: any): string {
  const ref = hpa.spec?.scaleTargetRef
  if (!ref) return '-'
  return `${ref.kind}/${ref.name}`
}

export function getHPAMetrics(hpa: any): { cpu?: number; memory?: number; custom: number } {
  const currentMetrics = hpa.status?.currentMetrics || []
  const result: { cpu?: number; memory?: number; custom: number } = { custom: 0 }

  for (const metric of currentMetrics) {
    if (metric.type === 'Resource') {
      const current = metric.resource?.current?.averageUtilization
      if (metric.resource?.name === 'cpu' && current !== undefined) {
        result.cpu = current
      } else if (metric.resource?.name === 'memory' && current !== undefined) {
        result.memory = current
      }
    } else {
      result.custom++
    }
  }

  return result
}

// ============================================================================
// NODE UTILITIES
// ============================================================================

export function getNodeStatus(node: any): StatusBadge {
  const conditions = node.status?.conditions || []
  const readyCondition = conditions.find((c: any) => c.type === 'Ready')

  const isReady = readyCondition?.status === 'True'
  const isUnschedulable = node.spec?.unschedulable === true

  if (isReady && isUnschedulable) {
    return { text: 'Ready,SchedulingDisabled', color: healthColors.degraded, level: 'degraded' }
  }
  if (isReady) {
    return { text: 'Ready', color: healthColors.healthy, level: 'healthy' }
  }
  if (readyCondition?.status === 'False') {
    return { text: 'NotReady', color: healthColors.unhealthy, level: 'unhealthy' }
  }
  return { text: 'Unknown', color: healthColors.unknown, level: 'unknown' }
}

export function getNodeRoles(node: any): string {
  const labels = node.metadata?.labels || {}
  const roles: string[] = []

  for (const [key, value] of Object.entries(labels)) {
    if (key.startsWith('node-role.kubernetes.io/')) {
      let role = key.replace('node-role.kubernetes.io/', '')
      // Normalize master to control-plane
      if (role === 'master') role = 'control-plane'
      if (role && value !== 'false') {
        roles.push(role)
      }
    }
  }

  if (roles.length === 0) return 'worker'
  return roles.join(', ')
}

export interface NodeCondition {
  type: string
  status: 'True' | 'False' | 'Unknown'
  message?: string
}

// Problem conditions that indicate node issues
const NODE_PROBLEM_CONDITIONS = ['DiskPressure', 'MemoryPressure', 'PIDPressure', 'NetworkUnavailable']

export function getNodeConditions(node: any): { problems: string[]; healthy: boolean } {
  const conditions = node.status?.conditions || []
  const problems: string[] = []

  for (const cond of conditions) {
    // Ready=False is a problem
    if (cond.type === 'Ready' && cond.status !== 'True') {
      problems.push('NotReady')
    }
    // Other conditions are problems when True
    if (NODE_PROBLEM_CONDITIONS.includes(cond.type) && cond.status === 'True') {
      // Format: "DiskPressure" -> "Disk Pressure"
      const formatted = cond.type.replace(/([A-Z])/g, ' $1').trim()
      problems.push(formatted)
    }
  }

  return { problems, healthy: problems.length === 0 }
}

export function getNodeTaints(node: any): { count: number; text: string } {
  const taints = node.spec?.taints || []
  const count = taints.length
  if (count === 0) return { count: 0, text: 'None' }
  return { count, text: count === 1 ? '1 taint' : `${count} taints` }
}

export function getNodeVersion(node: any): string {
  return node.status?.nodeInfo?.kubeletVersion || '-'
}

// ============================================================================
// FORMATTING UTILITIES
// ============================================================================

export function formatAge(timestamp: string): string {
  if (!timestamp) return '-'
  const created = new Date(timestamp)
  const now = new Date()
  const diffMs = now.getTime() - created.getTime()
  return formatDuration(diffMs)
}

export function formatDuration(ms: number, detailed: boolean = false): string {
  const seconds = Math.floor(ms / 1000)
  const minutes = Math.floor(seconds / 60)
  const hours = Math.floor(minutes / 60)
  const days = Math.floor(hours / 24)

  if (detailed) {
    // Detailed format: "2d 5h", "5h 30m", "30m 15s"
    if (days > 0) return `${days}d ${hours % 24}h`
    if (hours > 0) return `${hours}h ${minutes % 60}m`
    if (minutes > 0) return `${minutes}m ${seconds % 60}s`
    return `${seconds}s`
  }

  // Short format: "2d", "5h", "30m"
  if (days > 0) return `${days}d`
  if (hours > 0) return `${hours}h`
  if (minutes > 0) return `${minutes}m`
  if (seconds > 0) return `${seconds}s`
  return '<1s'
}

export function formatBytes(bytes: number): string {
  if (bytes === 0) return '0 B'
  const k = 1024
  const sizes = ['B', 'KB', 'MB', 'GB']
  const i = Math.floor(Math.log(bytes) / Math.log(k))
  return `${parseFloat((bytes / Math.pow(k, i)).toFixed(1))} ${sizes[i]}`
}

export function cronToHuman(cron: string): string {
  if (!cron) return '-'
  const parts = cron.split(' ')
  if (parts.length !== 5) return cron

  const [minute, hour, dayOfMonth, month, dayOfWeek] = parts

  // Common patterns
  if (minute === '0' && hour === '0' && dayOfMonth === '*' && month === '*' && dayOfWeek === '*') {
    return 'Daily at midnight'
  }
  if (minute === '0' && hour !== '*' && dayOfMonth === '*' && month === '*' && dayOfWeek === '*') {
    return `Daily at ${hour}:00`
  }
  if (minute !== '*' && hour === '*' && dayOfMonth === '*' && month === '*' && dayOfWeek === '*') {
    return `Every hour at :${minute.padStart(2, '0')}`
  }
  if (minute === '*' && hour === '*' && dayOfMonth === '*' && month === '*' && dayOfWeek === '*') {
    return 'Every minute'
  }
  if (minute.startsWith('*/')) {
    const interval = minute.slice(2)
    return `Every ${interval} minutes`
  }
  if (dayOfWeek === '1-5' || dayOfWeek === 'MON-FRI') {
    return `Weekdays at ${hour}:${minute.padStart(2, '0')}`
  }

  return cron
}

// ============================================================================
// PVC UTILITIES
// ============================================================================

export function getPVCStatus(pvc: any): StatusBadge {
  const phase = pvc.status?.phase || 'Unknown'
  switch (phase) {
    case 'Bound':
      return { text: 'Bound', color: healthColors.healthy, level: 'healthy' }
    case 'Pending':
      return { text: 'Pending', color: healthColors.degraded, level: 'degraded' }
    case 'Lost':
      return { text: 'Lost', color: healthColors.unhealthy, level: 'unhealthy' }
    default:
      return { text: phase, color: healthColors.unknown, level: 'unknown' }
  }
}

export function getPVCCapacity(pvc: any): string {
  return pvc.status?.capacity?.storage || pvc.spec?.resources?.requests?.storage || '-'
}

export function getPVCAccessModes(pvc: any): string {
  const modes = pvc.status?.accessModes || pvc.spec?.accessModes || []
  const shortModes = modes.map((m: string) => {
    switch (m) {
      case 'ReadWriteOnce': return 'RWO'
      case 'ReadOnlyMany': return 'ROX'
      case 'ReadWriteMany': return 'RWX'
      case 'ReadWriteOncePod': return 'RWOP'
      default: return m
    }
  })
  return shortModes.join(', ') || '-'
}

// ============================================================================
// ROLLOUT UTILITIES (Argo Rollouts CRD)
// ============================================================================

export function getRolloutStatus(rollout: any): StatusBadge {
  const phase = rollout.status?.phase || 'Unknown'
  switch (phase) {
    case 'Healthy':
      return { text: 'Healthy', color: healthColors.healthy, level: 'healthy' }
    case 'Paused':
      return { text: 'Paused', color: healthColors.degraded, level: 'degraded' }
    case 'Progressing':
      return { text: 'Progressing', color: healthColors.degraded, level: 'degraded' }
    case 'Degraded':
      return { text: 'Degraded', color: healthColors.unhealthy, level: 'unhealthy' }
    default:
      return { text: phase, color: healthColors.unknown, level: 'unknown' }
  }
}

export function getRolloutStrategy(rollout: any): string {
  if (rollout.spec?.strategy?.canary) return 'Canary'
  if (rollout.spec?.strategy?.blueGreen) return 'BlueGreen'
  return 'Unknown'
}

export function getRolloutReady(rollout: any): string {
  const ready = rollout.status?.availableReplicas || 0
  const desired = rollout.spec?.replicas || 0
  return `${ready}/${desired}`
}

export function getRolloutStep(rollout: any): string | null {
  const steps = rollout.spec?.strategy?.canary?.steps || []
  const currentIndex = rollout.status?.currentStepIndex
  if (steps.length === 0 || currentIndex === undefined) return null
  return `${currentIndex}/${steps.length}`
}

// ============================================================================
// WORKFLOW UTILITIES (Argo Workflows CRD)
// ============================================================================

export function getWorkflowStatus(workflow: any): StatusBadge {
  const phase = workflow.status?.phase || 'Unknown'
  switch (phase) {
    case 'Succeeded':
      return { text: 'Succeeded', color: healthColors.healthy, level: 'healthy' }
    case 'Running':
      return { text: 'Running', color: healthColors.degraded, level: 'degraded' }
    case 'Failed':
      return { text: 'Failed', color: healthColors.unhealthy, level: 'unhealthy' }
    case 'Error':
      return { text: 'Error', color: healthColors.unhealthy, level: 'unhealthy' }
    case 'Pending':
      return { text: 'Pending', color: healthColors.degraded, level: 'degraded' }
    default:
      return { text: phase, color: healthColors.unknown, level: 'unknown' }
  }
}

export function getWorkflowDuration(workflow: any): string | null {
  const startedAt = workflow.status?.startedAt
  const finishedAt = workflow.status?.finishedAt
  if (!startedAt) return null
  const start = new Date(startedAt)
  const end = finishedAt ? new Date(finishedAt) : new Date()
  return formatDuration(end.getTime() - start.getTime())
}

export function getWorkflowProgress(workflow: any): string | null {
  const nodes = workflow.status?.nodes
  if (!nodes) return null
  const nodeList = Object.values(nodes) as any[]
  const podNodes = nodeList.filter((n: any) => n.type === 'Pod')
  if (podNodes.length === 0) return null
  const succeeded = podNodes.filter((n: any) => n.phase === 'Succeeded').length
  return `${succeeded}/${podNodes.length}`
}

export function getWorkflowTemplate(workflow: any): string | null {
  return workflow.spec?.workflowTemplateRef?.name || null
}

// ============================================================================
// CERT-MANAGER UTILITIES — re-exported from resource-utils-certmanager.ts
// ============================================================================
export {
  getCertificateStatus,
  getCertificateDomains,
  getCertificateIssuer,
  getCertificateExpiry,
  getCertificateRequestStatus,
  getCertificateRequestIssuer,
  getCertificateRequestApproved,
  getClusterIssuerStatus,
  getClusterIssuerType,
  getIssuerStatus,
  getIssuerType,
  getOrderState,
  getOrderDomains,
  getOrderIssuer,
  getChallengeState,
  getChallengeType,
  getChallengeDomain,
  getChallengePresented,
} from './resource-utils-certmanager'

// ============================================================================
// FLUXCD UTILITIES — re-exported from resource-utils-flux.ts
// ============================================================================
export {
  getFluxResourceStatus,
  getGitRepositoryStatus,
  getOCIRepositoryStatus,
  getHelmRepositoryStatus,
  getFluxAlertStatus,
  getKustomizationStatus,
  getFluxHelmReleaseStatus,
  getGitRepositoryUrl,
  getGitRepositoryRef,
  getGitRepositoryRevision,
  getOCIRepositoryUrl,
  getOCIRepositoryRef,
  getOCIRepositoryRevision,
  getHelmRepositoryUrl,
  getHelmRepositoryType,
  getKustomizationSource,
  getKustomizationPath,
  getKustomizationInventory,
  getFluxHelmReleaseChart,
  getFluxHelmReleaseVersion,
  getFluxHelmReleaseRevision,
  getFluxAlertProvider,
  getFluxAlertEventCount,
} from './resource-utils-flux'

// ============================================================================
// ARGOCD UTILITIES — re-exported from resource-utils-argo.ts
// ============================================================================
export {
  getArgoApplicationStatus,
  getArgoApplicationProject,
  getArgoApplicationSync,
  getArgoApplicationHealth,
  getArgoApplicationRepo,
  getArgoApplicationSetGenerators,
  getArgoApplicationSetTemplate,
  getArgoApplicationSetAppCount,
  getArgoApplicationSetStatus,
  getArgoAppProjectDescription,
  getArgoAppProjectDestinations,
  getArgoAppProjectSources,
} from './resource-utils-argo'

// ============================================================================
// PERSISTENT VOLUME UTILITIES
// ============================================================================

export function getPVStatus(pv: any): StatusBadge {
  const phase = pv.status?.phase
  switch (phase) {
    case 'Bound':
      return { text: 'Bound', color: healthColors.healthy, level: 'healthy' }
    case 'Available':
      return { text: 'Available', color: healthColors.healthy, level: 'healthy' }
    case 'Released':
      return { text: 'Released', color: healthColors.degraded, level: 'degraded' }
    case 'Failed':
      return { text: 'Failed', color: healthColors.unhealthy, level: 'unhealthy' }
    default:
      return { text: phase || 'Unknown', color: healthColors.unknown, level: 'unknown' }
  }
}

export function getPVAccessModes(pv: any): string {
  const shorthand: Record<string, string> = {
    ReadWriteOnce: 'RWO', ReadOnlyMany: 'ROX', ReadWriteMany: 'RWX', ReadWriteOncePod: 'RWOP',
  }
  const modes = pv.spec?.accessModes || []
  return modes.map((m: string) => shorthand[m] || m).join(', ') || '-'
}

export function getPVClaim(pv: any): string {
  const ref = pv.spec?.claimRef
  if (!ref) return '-'
  return ref.namespace ? `${ref.namespace}/${ref.name}` : ref.name || '-'
}

// ============================================================================
// STORAGE CLASS UTILITIES
// ============================================================================

export function getStorageClassProvisioner(sc: any): string {
  return sc.provisioner || '-'
}

export function getStorageClassReclaimPolicy(sc: any): string {
  return sc.reclaimPolicy || '-'
}

export function getStorageClassBindingMode(sc: any): string {
  const mode = sc.volumeBindingMode
  if (mode === 'WaitForFirstConsumer') return 'WaitForConsumer'
  return mode || '-'
}

export function getStorageClassExpansion(sc: any): string {
  return sc.allowVolumeExpansion ? 'Yes' : 'No'
}



// ============================================================================
// GATEWAY UTILITIES (Gateway API)
// ============================================================================

export function getGatewayStatus(gw: any): StatusBadge {
  const conditions = gw.status?.conditions || []
  const programmed = conditions.find((c: any) => c.type === 'Programmed')
  const accepted = conditions.find((c: any) => c.type === 'Accepted')
  if (programmed?.status === 'True') return { text: 'Programmed', color: healthColors.healthy, level: 'healthy' }
  if (accepted?.status === 'True') return { text: 'Accepted', color: healthColors.degraded, level: 'degraded' }
  if (accepted?.status === 'False') return { text: 'Not Accepted', color: healthColors.unhealthy, level: 'unhealthy' }
  return { text: 'Unknown', color: healthColors.unknown, level: 'unknown' }
}

export function getGatewayClass(gw: any): string {
  return gw.spec?.gatewayClassName || '-'
}

export function getGatewayListeners(gw: any): string {
  const listeners = gw.spec?.listeners || []
  if (listeners.length === 0) return '-'
  return listeners.map((l: any) => `${l.protocol}:${l.port}`).join(', ')
}

export function getGatewayAttachedRoutes(gw: any): number {
  const statusListeners = gw.status?.listeners || []
  return statusListeners.reduce((sum: number, l: any) => sum + (l.attachedRoutes ?? 0), 0)
}

export function getGatewayAddresses(gw: any): string {
  const addrs = gw.status?.addresses || gw.spec?.addresses || []
  return addrs.map((a: any) => a.value).join(', ') || '-'
}

// ============================================================================
// GATEWAYCLASS UTILITIES (Gateway API)
// ============================================================================

export function getGatewayClassStatus(gc: any): StatusBadge {
  const conditions = gc.status?.conditions || []
  const accepted = conditions.find((c: any) => c.type === 'Accepted')
  if (accepted?.status === 'True') return { text: 'Accepted', color: healthColors.healthy, level: 'healthy' }
  if (accepted?.status === 'False') return { text: accepted.reason || 'Not Accepted', color: healthColors.unhealthy, level: 'unhealthy' }
  return { text: 'Pending', color: healthColors.degraded, level: 'degraded' }
}

export function getGatewayClassController(gc: any): string {
  return gc.spec?.controllerName || '-'
}

export function getGatewayClassDescription(gc: any): string {
  return gc.spec?.description || '-'
}

// ============================================================================
// GATEWAY API ROUTE UTILITIES (shared by HTTPRoute, GRPCRoute, TCPRoute, TLSRoute)
// ============================================================================

// All Gateway API route types share the same status/parents/rules/hostnames structure
export function getRouteStatus(route: any): StatusBadge {
  const parents = route.status?.parents || []
  if (parents.length === 0) return { text: 'Unknown', color: healthColors.unknown, level: 'unknown' }
  const allAccepted = parents.every((p: any) =>
    (p.conditions || []).some((c: any) => c.type === 'Accepted' && c.status === 'True')
  )
  const anyRejected = parents.some((p: any) =>
    (p.conditions || []).some((c: any) => c.type === 'Accepted' && c.status === 'False')
  )
  if (allAccepted) return { text: 'Accepted', color: healthColors.healthy, level: 'healthy' }
  if (anyRejected) return { text: 'Not Accepted', color: healthColors.unhealthy, level: 'unhealthy' }
  return { text: 'Pending', color: healthColors.degraded, level: 'degraded' }
}

export function getRouteParents(route: any): string {
  const refs = route.spec?.parentRefs || []
  return refs.map((r: any) => r.name).join(', ') || '-'
}

export function getRouteHostnames(route: any): string {
  const hostnames = route.spec?.hostnames || []
  return hostnames.join(', ') || 'Any'
}

export function getRouteRulesCount(route: any): number {
  return (route.spec?.rules || []).length
}

export function getRouteBackends(route: any): string {
  const rules = route.spec?.rules || []
  // Collect unique backend name:port pairs across all rules
  const seen = new Set<string>()
  for (const rule of rules) {
    for (const ref of rule.backendRefs || []) {
      const key = ref.port ? `${ref.name}:${ref.port}` : ref.name
      seen.add(key)
    }
  }
  if (seen.size === 0) return '-'
  return Array.from(seen).join(', ')
}

// Legacy aliases for HTTPRoute (used by HTTPRouteRenderer)
export const getHTTPRouteStatus = getRouteStatus
export const getHTTPRouteParents = getRouteParents
export const getHTTPRouteHostnames = getRouteHostnames
export const getHTTPRouteRulesCount = getRouteRulesCount

// ============================================================================
// SEALED SECRET UTILITIES (Bitnami)
// ============================================================================

export function getSealedSecretStatus(ss: any): StatusBadge {
  const conditions = ss.status?.conditions || []
  const synced = conditions.find((c: any) => c.type === 'Synced')
  if (synced?.status === 'True') return { text: 'Synced', color: healthColors.healthy, level: 'healthy' }
  if (synced?.status === 'False') return { text: 'Not Synced', color: healthColors.unhealthy, level: 'unhealthy' }
  return { text: 'Unknown', color: healthColors.unknown, level: 'unknown' }
}

export function getSealedSecretKeyCount(ss: any): number {
  return Object.keys(ss.spec?.encryptedData || {}).length
}

// ============================================================================
// WORKFLOW TEMPLATE UTILITIES (Argo)
// ============================================================================

export function getWorkflowTemplateCount(wt: any): number {
  return (wt.spec?.templates || []).length
}

export function getWorkflowTemplateEntrypoint(wt: any): string {
  return wt.spec?.entrypoint || '-'
}

// ============================================================================
// NETWORK POLICY UTILITIES
// ============================================================================

export function getNetworkPolicyTypes(np: any): string {
  const types = np.spec?.policyTypes || []
  return types.join(', ') || '-'
}

export function getNetworkPolicyRuleCount(np: any): { ingress: number; egress: number } {
  return {
    ingress: (np.spec?.ingress || []).length,
    egress: (np.spec?.egress || []).length,
  }
}

export function getNetworkPolicySelector(np: any): string {
  const labels = np.spec?.podSelector?.matchLabels
  if (!labels || Object.keys(labels).length === 0) return 'All pods'
  return Object.entries(labels).map(([k, v]) => `${k}=${v}`).join(', ')
}

// ============================================================================
// POD DISRUPTION BUDGET UTILITIES
// ============================================================================

export function getPDBStatus(pdb: any): StatusBadge {
  const status = pdb.status || {}
  const allowed = status.disruptionsAllowed
  const healthy = status.currentHealthy || 0
  const desired = status.desiredHealthy || 0

  if (healthy < desired) return { text: 'Unhealthy', color: healthColors.unhealthy, level: 'unhealthy' }
  if (allowed === 0 && (status.expectedPods || 0) > 0) return { text: 'Blocked', color: healthColors.degraded, level: 'degraded' }
  if (allowed > 0) return { text: 'OK', color: healthColors.healthy, level: 'healthy' }
  return { text: 'Unknown', color: healthColors.unknown, level: 'unknown' }
}

export function getPDBBudget(pdb: any): string {
  const spec = pdb.spec || {}
  if (spec.minAvailable !== undefined) return `min: ${spec.minAvailable}`
  if (spec.maxUnavailable !== undefined) return `max unavail: ${spec.maxUnavailable}`
  return '-'
}

export function getPDBHealthy(pdb: any): string {
  const status = pdb.status || {}
  return `${status.currentHealthy || 0}/${status.expectedPods || 0}`
}

export function getPDBAllowed(pdb: any): number {
  return pdb.status?.disruptionsAllowed ?? 0
}

// ============================================================================
// SERVICE ACCOUNT UTILITIES
// ============================================================================

export function getServiceAccountSecretCount(sa: any): number {
  return (sa.secrets || []).length
}

export function getServiceAccountAutomount(sa: any): string {
  return sa.automountServiceAccountToken === false ? 'No' : 'Yes'
}

// ============================================================================
// ROLE / CLUSTER ROLE UTILITIES
// ============================================================================

export function getRoleRuleCount(role: any): number {
  return (role.rules || []).length
}

// ============================================================================
// ROLE BINDING / CLUSTER ROLE BINDING UTILITIES
// ============================================================================

export function getRoleBindingRole(rb: any): string {
  const ref = rb.roleRef
  if (!ref) return '-'
  return ref.name || '-'
}

export function getRoleBindingSubjectCount(rb: any): number {
  return (rb.subjects || []).length
}

// ============================================================================
// WORKLOAD PROBLEM DETECTION (for table row indicators)
// ============================================================================

export function getWorkloadProblems(resource: any, kind: string): string[] {
  const problems: string[] = []
  const status = resource.status || {}
  const spec = resource.spec || {}

  if (kind === 'daemonsets') {
    const ready = status.numberReady || 0
    const desired = status.desiredNumberScheduled || 0
    if (desired > 0 && ready < desired) {
      problems.push(`${desired - ready} pods not ready`)
    }
  } else {
    const ready = status.readyReplicas || 0
    const desired = spec.replicas ?? 0
    if (desired > 0 && ready < desired) {
      problems.push(`${desired - ready} replicas not ready`)
    }
  }

  const conditions = status.conditions || []
  for (const cond of conditions) {
    if (cond.status === 'True' && cond.type === 'ReplicaFailure') {
      problems.push('ReplicaFailure')
    }
    if (cond.status === 'False' && cond.type === 'Available') {
      problems.push('Unavailable')
    }
  }

  return problems
}

// ============================================================================
// FORMATTING UTILITIES
// ============================================================================

export function truncate(str: string, length: number): string {
  if (!str || str.length <= length) return str
  return str.slice(0, length - 1) + '…'
}

export function formatResources(resources: any): string {
  const parts: string[] = []
  if (resources.cpu) {
    parts.push(`CPU: ${formatCPUString(resources.cpu)}`)
  }
  if (resources.memory) {
    parts.push(`Mem: ${formatMemoryString(resources.memory)}`)
  }
  return parts.join(', ') || '-'
}

// ============================================================================
// GENERIC COLUMN FILTER VALUE EXTRACTION
// ============================================================================

/**
 * Extracts a string filter value for a given column key and resource kind.
 * Used by the generic column filter system to match resources against filter values.
 * Reuses existing utility functions for kind-specific columns.
 */
// Parse column filters from URL `filters` param (format: "col:val|col2:val2")
// Uses `|` as pair separator so values can safely contain commas (e.g., "Ready,SchedulingDisabled")
export function parseColumnFilters(filtersParam: string | null): Record<string, string> {
  if (!filtersParam) return {}
  const filters: Record<string, string> = {}
  for (const pair of filtersParam.split('|')) {
    const colonIdx = pair.indexOf(':')
    if (colonIdx > 0) {
      const key = pair.slice(0, colonIdx).trim()
      const value = pair.slice(colonIdx + 1).trim()
      if (key && value) filters[key] = value
    }
  }
  return filters
}

// Serialize column filters to URL param format
export function serializeColumnFilters(filters: Record<string, string>): string {
  const result = Object.entries(filters)
    .filter(([, v]) => v)
    .map(([k, v]) => `${k}:${v}`)
    .join('|')
  return result
}

export function getCellFilterValue(resource: any, column: string, kind: string): string {
  try {
  const kindLower = kind.toLowerCase()

  switch (column) {
    case 'type':
      if (kindLower === 'secrets' || kindLower === 'sealedsecrets') return getSecretType(resource).type
      if (kindLower === 'services') return resource.spec?.type || ''
      if (kindLower === 'events') return resource.type || ''
      if (kindLower === 'helmrepositories') return getHelmRepositoryType(resource)
      return resource.spec?.type || resource.type || ''
    case 'status':
      if (kindLower === 'pods') return getPodStatus(resource).text
      if (['deployments', 'statefulsets', 'daemonsets', 'replicasets'].includes(kindLower)) {
        const status = getWorkloadStatus(resource, kindLower)
        if (status.text === 'Scaled to 0') return 'Scaled to 0'
        if (status.level === 'healthy') return 'Healthy'
        if (status.level === 'degraded') return 'Degraded'
        if (status.level === 'unhealthy') return 'Unhealthy'
        return 'Unknown'
      }
      if (kindLower === 'nodes') return getNodeStatus(resource).text
      if (kindLower === 'jobs') return getJobStatus(resource).text
      if (kindLower === 'cronjobs') return getCronJobStatus(resource).text
      if (kindLower === 'certificates') return getCertificateStatus(resource).text
      if (kindLower === 'certificaterequests') return getCertificateRequestStatus(resource).text
      if (kindLower === 'clusterissuers' || kindLower === 'issuers') return getClusterIssuerStatus(resource).text
      if (kindLower === 'persistentvolumeclaims') return getPVCStatus(resource).text
      if (kindLower === 'persistentvolumes') return getPVStatus(resource).text
      if (kindLower === 'rollouts') return getRolloutStatus(resource).text
      if (kindLower === 'workflows') return getWorkflowStatus(resource).text
      if (kindLower === 'hpas' || kindLower === 'horizontalpodautoscalers') return getHPAStatus(resource).text
      if (kindLower === 'gateways') return getGatewayStatus(resource).text
      if (kindLower === 'gatewayclasses') return getGatewayClassStatus(resource).text
      if (['httproutes', 'grpcroutes', 'tcproutes', 'tlsroutes'].includes(kindLower)) return getRouteStatus(resource).text
      if (kindLower === 'sealedsecrets') return getSealedSecretStatus(resource).text
      if (kindLower === 'poddisruptionbudgets') return getPDBStatus(resource).text
      if (kindLower === 'applications') return getArgoApplicationStatus(resource).text
      if (kindLower === 'applicationsets') return getArgoApplicationSetStatus(resource).text
      if (kindLower === 'gitrepositories') return getGitRepositoryStatus(resource).text
      if (kindLower === 'ocirepositories') return getOCIRepositoryStatus(resource).text
      if (kindLower === 'helmrepositories') return getHelmRepositoryStatus(resource).text
      if (kindLower === 'kustomizations') return getKustomizationStatus(resource).text
      if (kindLower === 'helmreleases') return getFluxHelmReleaseStatus(resource).text
      if (kindLower === 'alerts') return getFluxAlertStatus(resource).text
      if (kindLower === 'nodepools') return getNodePoolStatus(resource).text
      if (kindLower === 'nodeclaims') return getNodeClaimStatus(resource).text
      if (kindLower === 'scaledobjects') return getScaledObjectStatus(resource).text
      if (kindLower === 'scaledjobs') return getScaledJobStatus(resource).text
      if (kindLower === 'policyreports' || kindLower === 'clusterpolicyreports') return _getPolicyReportStatus(resource).text
      if (kindLower === 'kyvernopolicies' || kindLower === 'clusterpolicies') return _getKyvernoPolicyStatus(resource).text
      if (kindLower === 'backups') return _getBackupStatus(resource).text
      if (kindLower === 'restores') return _getRestoreStatus(resource).text
      if (kindLower === 'schedules') return _getScheduleStatus(resource).text
      if (kindLower === 'backupstoragelocations') return _getBSLStatus(resource).text
      if (kindLower === 'externalsecrets') return _getExternalSecretStatus(resource).text
      if (kindLower === 'clusterexternalsecrets') return _getClusterExternalSecretStatus(resource).text
      if (kindLower === 'secretstores') return _getSecretStoreStatus(resource).text
      if (kindLower === 'clustersecretstores') return _getClusterSecretStoreStatus(resource).text
      // Generic CRDs: try status.phase, then Ready condition
      if (resource.status?.phase) return resource.status.phase
      {
        const conditions = resource.status?.conditions || []
        const ready = conditions.find((c: any) => c.type === 'Ready')
        if (ready?.status === 'True') return 'Ready'
        if (ready?.status === 'False') return 'Not Ready'
      }
      return ''
    case 'state':
      if (kindLower === 'orders') return getOrderState(resource).text
      if (kindLower === 'challenges') return getChallengeState(resource).text
      return resource.status?.state || ''
    case 'challengeType':
      return getChallengeType(resource)
    case 'issuerType':
      return getClusterIssuerType(resource)
    case 'class':
      return resource.spec?.ingressClassName || resource.spec?.gatewayClassName || ''
    case 'roles':
      return getNodeRoles(resource)
    case 'strategy':
      return getRolloutStrategy(resource)
    case 'storageClass':
      return resource.spec?.storageClassName || ''
    case 'reclaimPolicy':
      return resource.reclaimPolicy || resource.spec?.persistentVolumeReclaimPolicy || ''
    case 'bindingMode':
      return getStorageClassBindingMode(resource)
    case 'expansion':
      return getStorageClassExpansion(resource)
    case 'automount':
      return getServiceAccountAutomount(resource)
    case 'policyTypes':
      return getNetworkPolicyTypes(resource)
  }

  // Fallback: try common paths
  const val = resource.status?.[column] ?? resource.spec?.[column] ?? resource[column]
  if (val === undefined || val === null) return ''
  if (typeof val === 'boolean') return val ? 'Yes' : 'No'
  if (typeof val === 'string') return val
  if (typeof val === 'number') return String(val)
  if (typeof val === 'object') return '' // Skip objects/arrays — not filterable
  return String(val)
  } catch (err) {
    console.warn(`[getCellFilterValue] Failed for kind=${kind} column=${column}:`, err)
    return ''
  }
}

// ============================================================================
// TRIVY OPERATOR UTILITIES — re-exported from resource-utils-trivy.ts
// ============================================================================
export {
  getVulnerabilityReportSummary,
  getVulnerabilityReportStatus,
  getVulnerabilityReportImage,
  getVulnerabilityReportContainer,
  getConfigAuditReportSummary,
  getConfigAuditReportStatus,
  getExposedSecretReportSummary,
  getExposedSecretReportContainer,
  getExposedSecretReportImage,
  getExposedSecretReportStatus,
  getRbacAssessmentReportSummary,
  getRbacAssessmentReportStatus,
  getClusterComplianceReportStatus,
  getSbomReportStatus,
  getSbomReportContainer,
} from './resource-utils-trivy'
export type { VulnerabilitySummary, ConfigAuditSummary } from './resource-utils-trivy'

// ============================================================================
// KARPENTER UTILITIES — re-exported from resource-utils-karpenter.ts
// ============================================================================
export {
  getNodePoolStatus,
  getNodePoolNodeClassRef,
  getNodePoolLimits,
  getNodePoolDisruptionPolicy,
  getNodePoolRequirements,
  getNodePoolWeight,
  getNodeClaimStatus,
  getNodeClaimInstanceType,
  getNodeClaimNodeName,
  getNodeClaimCapacity,
  getNodeClaimNodePoolRef,
} from './resource-utils-karpenter'

// ============================================================================
// KEDA UTILITIES — re-exported from resource-utils-keda.ts
// ============================================================================
export {
  getScaledObjectStatus,
  getScaledObjectTarget,
  getScaledObjectTargetKind,
  getScaledObjectTargetName,
  getScaledObjectReplicas,
  getScaledObjectTriggers,
  getScaledObjectTriggerCount,
  getScaledObjectHpaName,
  getScaledObjectLastActiveTime,
  getScaledObjectPollingInterval,
  getScaledObjectCooldownPeriod,
  getScaledJobStatus,
  getScaledJobTarget,
  getScaledJobStrategy,
  getScaledJobTriggerCount,
  getScaledJobTriggers,
} from './resource-utils-keda'

// ============================================================================
// KYVERNO / POLICY REPORT UTILITIES — re-exported from resource-utils-kyverno.ts
// ============================================================================
export {
  getPolicyReportStatus,
  getPolicyReportSummary,
  getPolicyReportResults,
  getPolicyReportResultCount,
  getPolicyReportScope,
  getPolicyReportSource,
  getClusterPolicyReportStatus,
  getKyvernoPolicyStatus,
  getKyvernoPolicyAction,
  getKyvernoPolicyRuleCount,
  getKyvernoPolicyRuleTypes,
  getKyvernoPolicyRules,
  getKyvernoPolicyBackground,
  getKyvernoPolicyRuleCountByType,
  getClusterPolicyStatus,
  getClusterPolicyAction,
  getClusterPolicyRuleCount,
} from './resource-utils-kyverno'

// ============================================================================
// EXTERNAL SECRETS OPERATOR UTILITIES — re-exported from resource-utils-eso.ts
// ============================================================================
export {
  getExternalSecretStatus,
  getExternalSecretStore,
  getExternalSecretRefreshInterval,
  getExternalSecretSecretCount,
  getExternalSecretLastSync,
  getExternalSecretTargetName,
  getExternalSecretProvider,
  getClusterExternalSecretStatus,
  getClusterExternalSecretNamespaceCount,
  getClusterExternalSecretFailedCount,
  getSecretStoreStatus,
  getSecretStoreProviderType,
  getSecretStoreProviderKey,
  getClusterSecretStoreStatus,
} from './resource-utils-eso'
