// Utility functions for resource display in tables

// ============================================================================
// STATUS & HEALTH UTILITIES
// ============================================================================

export type HealthLevel = 'healthy' | 'degraded' | 'unhealthy' | 'unknown' | 'neutral'

export interface StatusBadge {
  text: string
  color: string
  level: HealthLevel
}

// Color classes for different health levels
export const healthColors: Record<HealthLevel, string> = {
  healthy: 'bg-green-500/20 text-green-400',
  degraded: 'bg-yellow-500/20 text-yellow-400',
  unhealthy: 'bg-red-500/20 text-red-400',
  unknown: 'bg-slate-500/20 text-slate-400',
  neutral: 'bg-blue-500/20 text-blue-400',
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

  for (const cs of containerStatuses) {
    // Check waiting state
    if (cs.state?.waiting?.reason) {
      const reason = cs.state.waiting.reason
      if (['CrashLoopBackOff', 'ImagePullBackOff', 'ErrImagePull'].includes(reason)) {
        problems.push({ severity: 'critical', message: reason })
      } else if (reason === 'CreateContainerConfigError') {
        problems.push({ severity: 'critical', message: 'Config Error' })
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
  }

  // Check conditions
  const conditions = pod.status?.conditions || []
  for (const cond of conditions) {
    if (cond.type === 'PodScheduled' && cond.status === 'False') {
      problems.push({ severity: 'high', message: 'Unschedulable' })
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
    ? 'bg-violet-500/20 text-violet-400'
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
    if (ingress.length > 0) {
      return ingress[0].ip || ingress[0].hostname || null
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
  if (service.spec?.externalIPs?.length > 0) {
    return service.spec.externalIPs[0]
  }
  return null
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
    'Opaque': { type: 'Opaque', color: 'bg-slate-500/20 text-slate-400' },
    'kubernetes.io/tls': { type: 'TLS', color: 'bg-blue-500/20 text-blue-400' },
    'kubernetes.io/dockercfg': { type: 'Docker', color: 'bg-purple-500/20 text-purple-400' },
    'kubernetes.io/dockerconfigjson': { type: 'Docker', color: 'bg-purple-500/20 text-purple-400' },
    'kubernetes.io/basic-auth': { type: 'Basic Auth', color: 'bg-orange-500/20 text-orange-400' },
    'kubernetes.io/ssh-auth': { type: 'SSH', color: 'bg-cyan-500/20 text-cyan-400' },
    'kubernetes.io/service-account-token': { type: 'SA Token', color: 'bg-green-500/20 text-green-400' },
    'bootstrap.kubernetes.io/token': { type: 'Bootstrap', color: 'bg-green-500/20 text-green-400' },
  }
  return typeMap[type] || { type: type.split('/').pop() || type, color: 'bg-slate-500/20 text-slate-400' }
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

export function truncate(str: string, length: number): string {
  if (!str || str.length <= length) return str
  return str.slice(0, length - 1) + '…'
}

export function formatResources(resources: any): string {
  const parts: string[] = []
  if (resources.cpu) parts.push(`CPU: ${resources.cpu}`)
  if (resources.memory) parts.push(`Mem: ${resources.memory}`)
  return parts.join(', ') || '-'
}
