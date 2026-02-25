// Istio CRD utility functions

import type { StatusBadge } from './resource-utils'
import { healthColors } from './resource-utils'

// ============================================================================
// SHARED HELPERS
// ============================================================================

function parseIstioHost(host: string): { name: string; namespace: string } {
  // Istio hosts can be: "reviews", "reviews.default", "reviews.default.svc.cluster.local"
  const parts = host.split('.')
  return {
    name: parts[0] || host,
    namespace: parts.length >= 2 ? parts[1] : '',
  }
}

// ============================================================================
// VIRTUALSERVICE UTILITIES
// ============================================================================

export function getVirtualServiceStatus(resource: any): StatusBadge {
  const spec = resource.spec || {}
  const httpRoutes = spec.http || []
  const tcpRoutes = spec.tcp || []
  const tlsRoutes = spec.tls || []
  const hosts = spec.hosts || []

  if (hosts.length === 0) {
    return { text: 'No Hosts', color: healthColors.unhealthy, level: 'unhealthy' }
  }

  const totalRoutes = httpRoutes.length + tcpRoutes.length + tlsRoutes.length
  if (totalRoutes === 0) {
    return { text: 'No Routes', color: healthColors.unhealthy, level: 'unhealthy' }
  }

  // Check for fault injection on any route
  const hasFaultInjection = httpRoutes.some((r: any) => r.fault)
  if (hasFaultInjection) {
    return { text: 'Fault Injection', color: healthColors.degraded, level: 'degraded' }
  }

  // Check for traffic mirroring
  const hasMirror = httpRoutes.some((r: any) => r.mirror)
  if (hasMirror) {
    return { text: 'Mirroring', color: healthColors.degraded, level: 'degraded' }
  }

  return { text: 'Active', color: healthColors.healthy, level: 'healthy' }
}

export function getVirtualServiceHosts(resource: any): string {
  const hosts = resource.spec?.hosts || []
  if (hosts.length === 0) return '-'
  if (hosts.length > 3) return `${hosts.slice(0, 3).join(', ')} +${hosts.length - 3}`
  return hosts.join(', ')
}

export function getVirtualServiceHostsList(resource: any): string[] {
  return resource.spec?.hosts || []
}

export function getVirtualServiceGateways(resource: any): string {
  const gateways = resource.spec?.gateways || []
  if (gateways.length === 0) return '-'
  return gateways.join(', ')
}

export function getVirtualServiceGatewaysList(resource: any): string[] {
  return resource.spec?.gateways || []
}

export function getVirtualServiceRouteCount(resource: any): number {
  const spec = resource.spec || {}
  return (spec.http || []).length + (spec.tcp || []).length + (spec.tls || []).length
}

export function getVirtualServiceHttpRoutes(resource: any): Array<{
  match?: any[]
  route?: Array<{ destination: { host: string; port?: { number: number }; subset?: string }; weight?: number }>
  timeout?: string
  retries?: { attempts: number; perTryTimeout?: string; retryOn?: string }
  fault?: { delay?: { percentage?: { value: number }; fixedDelay?: string }; abort?: { percentage?: { value: number }; httpStatus?: number } }
  mirror?: { host: string; port?: { number: number } }
  mirrorPercentage?: { value: number }
  name?: string
}> {
  return resource.spec?.http || []
}

export function getVirtualServiceTcpRoutes(resource: any): any[] {
  return resource.spec?.tcp || []
}

export function getVirtualServiceTlsRoutes(resource: any): any[] {
  return resource.spec?.tls || []
}

export function getVirtualServiceDestinations(resource: any): Array<{ host: string; namespace: string; port?: number; subset?: string; weight?: number }> {
  const destinations: Array<{ host: string; namespace: string; port?: number; subset?: string; weight?: number }> = []
  const httpRoutes = resource.spec?.http || []
  for (const route of httpRoutes) {
    for (const dest of (route.route || [])) {
      if (dest.destination?.host) {
        const parsed = parseIstioHost(dest.destination.host)
        destinations.push({
          host: dest.destination.host,
          namespace: parsed.namespace || resource.metadata?.namespace || '',
          port: dest.destination.port?.number,
          subset: dest.destination.subset,
          weight: dest.weight,
        })
      }
    }
  }
  return destinations
}

// ============================================================================
// DESTINATIONRULE UTILITIES
// ============================================================================

export function getDestinationRuleStatus(resource: any): StatusBadge {
  const spec = resource.spec || {}
  const host = spec.host

  if (!host) {
    return { text: 'No Host', color: healthColors.unhealthy, level: 'unhealthy' }
  }

  const subsets = spec.subsets || []
  const trafficPolicy = spec.trafficPolicy

  if (subsets.length > 0) {
    return { text: `${subsets.length} Subset${subsets.length !== 1 ? 's' : ''}`, color: healthColors.healthy, level: 'healthy' }
  }

  if (trafficPolicy) {
    return { text: 'Configured', color: healthColors.healthy, level: 'healthy' }
  }

  return { text: 'Active', color: healthColors.healthy, level: 'healthy' }
}

export function getDestinationRuleHost(resource: any): string {
  return resource.spec?.host || '-'
}

export function getDestinationRuleSubsetCount(resource: any): number {
  return (resource.spec?.subsets || []).length
}

export function getDestinationRuleSubsets(resource: any): Array<{ name: string; labels: Record<string, string>; trafficPolicy?: any }> {
  return (resource.spec?.subsets || []).map((s: any) => ({
    name: s.name || '',
    labels: s.labels || {},
    trafficPolicy: s.trafficPolicy,
  }))
}

export function getDestinationRuleTrafficPolicy(resource: any): {
  connectionPool?: { tcp?: any; http?: any }
  loadBalancer?: { simple?: string; consistentHash?: any }
  outlierDetection?: any
  tls?: { mode?: string }
} | null {
  return resource.spec?.trafficPolicy || null
}

export function getDestinationRuleLoadBalancer(resource: any): string {
  const lb = resource.spec?.trafficPolicy?.loadBalancer
  if (!lb) return '-'
  if (lb.simple) return lb.simple
  if (lb.consistentHash) return 'ConsistentHash'
  return '-'
}

// ============================================================================
// ISTIO GATEWAY UTILITIES
// ============================================================================

export function getIstioGatewayStatus(resource: any): StatusBadge {
  const servers = resource.spec?.servers || []

  if (servers.length === 0) {
    return { text: 'No Servers', color: healthColors.unhealthy, level: 'unhealthy' }
  }

  const hasTLS = servers.some((s: any) => s.tls)
  if (hasTLS) {
    return { text: 'TLS', color: healthColors.healthy, level: 'healthy' }
  }

  return { text: 'Active', color: healthColors.healthy, level: 'healthy' }
}

export function getIstioGatewayServers(resource: any): Array<{
  port: { number: number; name: string; protocol: string }
  hosts: string[]
  tls?: { mode?: string; credentialName?: string; serverCertificate?: string; privateKey?: string }
}> {
  return (resource.spec?.servers || []).map((s: any) => ({
    port: s.port || { number: 0, name: '', protocol: '' },
    hosts: s.hosts || [],
    tls: s.tls,
  }))
}

export function getIstioGatewayServerCount(resource: any): number {
  return (resource.spec?.servers || []).length
}

export function getIstioGatewaySelector(resource: any): Record<string, string> {
  return resource.spec?.selector || {}
}

export function getIstioGatewaySelectorString(resource: any): string {
  const selector = resource.spec?.selector || {}
  const entries = Object.entries(selector)
  if (entries.length === 0) return '-'
  return entries.map(([k, v]) => `${k}=${v}`).join(', ')
}

// ============================================================================
// SERVICEENTRY UTILITIES
// ============================================================================

export function getServiceEntryStatus(resource: any): StatusBadge {
  const spec = resource.spec || {}
  const hosts = spec.hosts || []

  if (hosts.length === 0) {
    return { text: 'No Hosts', color: healthColors.unhealthy, level: 'unhealthy' }
  }

  const location = spec.location || 'MESH_EXTERNAL'
  if (location === 'MESH_EXTERNAL') {
    return { text: 'External', color: healthColors.healthy, level: 'healthy' }
  }

  return { text: 'Internal', color: healthColors.healthy, level: 'healthy' }
}

export function getServiceEntryHosts(resource: any): string {
  const hosts = resource.spec?.hosts || []
  if (hosts.length === 0) return '-'
  if (hosts.length > 3) return `${hosts.slice(0, 3).join(', ')} +${hosts.length - 3}`
  return hosts.join(', ')
}

export function getServiceEntryHostsList(resource: any): string[] {
  return resource.spec?.hosts || []
}

export function getServiceEntryLocation(resource: any): string {
  return resource.spec?.location || 'MESH_EXTERNAL'
}

export function getServiceEntryPorts(resource: any): Array<{ number: number; name: string; protocol: string }> {
  return (resource.spec?.ports || []).map((p: any) => ({
    number: p.number || 0,
    name: p.name || '',
    protocol: p.protocol || '',
  }))
}

export function getServiceEntryPortsString(resource: any): string {
  const ports = resource.spec?.ports || []
  if (ports.length === 0) return '-'
  return ports.map((p: any) => `${p.number}/${p.protocol || 'TCP'}`).join(', ')
}

export function getServiceEntryResolution(resource: any): string {
  return resource.spec?.resolution || 'NONE'
}

export function getServiceEntryEndpoints(resource: any): Array<{ address: string; ports?: Record<string, number>; labels?: Record<string, string> }> {
  return (resource.spec?.endpoints || []).map((e: any) => ({
    address: e.address || '',
    ports: e.ports,
    labels: e.labels,
  }))
}

// ============================================================================
// PEERAUTHENTICATION UTILITIES
// ============================================================================

export function getPeerAuthenticationStatus(resource: any): StatusBadge {
  const mode = resource.spec?.mtls?.mode || 'UNSET'

  switch (mode) {
    case 'STRICT':
      return { text: 'Strict mTLS', color: healthColors.healthy, level: 'healthy' }
    case 'PERMISSIVE':
      return { text: 'Permissive', color: healthColors.degraded, level: 'degraded' }
    case 'DISABLE':
      return { text: 'Disabled', color: healthColors.unhealthy, level: 'unhealthy' }
    default:
      return { text: 'Unset', color: healthColors.unknown, level: 'unknown' }
  }
}

export function getPeerAuthenticationMode(resource: any): string {
  return resource.spec?.mtls?.mode || 'UNSET'
}

export function getPeerAuthenticationSelector(resource: any): Record<string, string> {
  return resource.spec?.selector?.matchLabels || {}
}

export function getPeerAuthenticationSelectorString(resource: any): string {
  const labels = resource.spec?.selector?.matchLabels || {}
  const entries = Object.entries(labels)
  if (entries.length === 0) return 'Namespace-wide'
  return entries.map(([k, v]) => `${k}=${v}`).join(', ')
}

export function getPeerAuthenticationPortLevelMtls(resource: any): Record<string, { mode: string }> {
  return resource.spec?.portLevelMtls || {}
}

// ============================================================================
// AUTHORIZATIONPOLICY UTILITIES
// ============================================================================

export function getAuthorizationPolicyStatus(resource: any): StatusBadge {
  const action = resource.spec?.action || 'ALLOW'
  const rules = resource.spec?.rules || []

  switch (action) {
    case 'ALLOW':
      return { text: `Allow (${rules.length} rule${rules.length !== 1 ? 's' : ''})`, color: healthColors.healthy, level: 'healthy' }
    case 'DENY':
      return { text: `Deny (${rules.length} rule${rules.length !== 1 ? 's' : ''})`, color: healthColors.unhealthy, level: 'unhealthy' }
    case 'CUSTOM':
      return { text: 'Custom', color: healthColors.degraded, level: 'degraded' }
    case 'AUDIT':
      return { text: 'Audit', color: healthColors.degraded, level: 'degraded' }
    default:
      return { text: action, color: healthColors.unknown, level: 'unknown' }
  }
}

export function getAuthorizationPolicyAction(resource: any): string {
  return resource.spec?.action || 'ALLOW'
}

export function getAuthorizationPolicyRuleCount(resource: any): number {
  return (resource.spec?.rules || []).length
}

export function getAuthorizationPolicyRules(resource: any): Array<{
  from?: Array<{ source: { principals?: string[]; namespaces?: string[]; ipBlocks?: string[] } }>
  to?: Array<{ operation: { hosts?: string[]; ports?: string[]; methods?: string[]; paths?: string[] } }>
  when?: Array<{ key: string; values?: string[]; notValues?: string[] }>
}> {
  return resource.spec?.rules || []
}

export function getAuthorizationPolicySelector(resource: any): Record<string, string> {
  return resource.spec?.selector?.matchLabels || {}
}

export function getAuthorizationPolicySelectorString(resource: any): string {
  const labels = resource.spec?.selector?.matchLabels || {}
  const entries = Object.entries(labels)
  if (entries.length === 0) return 'Namespace-wide'
  return entries.map(([k, v]) => `${k}=${v}`).join(', ')
}
