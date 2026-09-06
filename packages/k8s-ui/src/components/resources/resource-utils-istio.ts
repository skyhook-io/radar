// Istio CRD utility functions

import type { StatusBadge } from './resource-utils'
import { healthColors } from './resource-utils'
import { pluralize } from '../../utils/pluralize'

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

/**
 * The only failure this object can show on its own.
 *
 * A rule without a host applies to nothing. Everything else it declares —
 * subsets, a traffic policy — is configuration, and says nothing about whether
 * the host is reachable or the policy is in effect.
 */
export function getDestinationRuleStatus(resource: any): StatusBadge {
  if (!resource.spec?.host) {
    return { text: 'No Host', color: healthColors.unhealthy, level: 'unhealthy' }
  }
  return { text: 'Not assessed', color: healthColors.unknown, level: 'unknown' }
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

/**
 * The client-side TLS mode this rule declares for its host.
 *
 * What it means depends on a different object. A DISABLE here sends plaintext:
 * against a PERMISSIVE PeerAuthentication that succeeds and the traffic is
 * simply unencrypted; against a STRICT one the server rejects it and the
 * requests fail. Neither outcome is visible from this row, and subset and port
 * policies can override the mode besides — so this reports the declaration and
 * not the effective posture.
 */
export function getDestinationRuleTlsMode(resource: any): string {
  return getDestinationRuleTrafficPolicy(resource)?.tls?.mode || '-'
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

/**
 * What one AuthorizationPolicy declares — never a health verdict.
 *
 * An action is not a state of wellness: a DENY doing its job is not unhealthy,
 * and an ALLOW is not proof anything is permitted. The request decision is made
 * across every policy selecting the workload, so no single object can establish
 * it, and a green or red tone would assert an answer this object does not have.
 *
 * The one shape worth marking is an ALLOW — the default action — carrying no
 * rules. Rules are alternatives, so zero of them match nothing, and Istio
 * documents `spec: {}` as the deny-all idiom. Amber says look closer; it stops
 * short of claiming the outage, because other policies may permit the traffic.
 */
export function getAuthorizationPolicyStatus(resource: any): StatusBadge {
  const action = resource.spec?.action || 'ALLOW'
  const rules = resource.spec?.rules || []

  if (action === 'ALLOW' && rules.length === 0) {
    return { text: 'No allow rules', color: healthColors.degraded, level: 'degraded' }
  }
  const known: Record<string, string> = { ALLOW: 'Allow', DENY: 'Deny', CUSTOM: 'Custom', AUDIT: 'Audit' }
  const label = known[action]
  if (!label) return { text: action, color: healthColors.unknown, level: 'unknown' }
  return {
    text: `${label} (${pluralize(rules.length, 'rule')})`,
    color: healthColors.neutral,
    level: 'neutral',
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

/**
 * What the policy declares it applies to.
 *
 * A policy can attach by targetRef instead of by labels — waypoints and
 * Gateways in ambient mode do — so the selector alone does not describe scope.
 * With neither, scope is inherited: the namespace, or the whole mesh when the policy
 * sits in the mesh root namespace. Which of those applies depends on
 * MeshConfig.rootNamespace, which is not readable from this object, so the cell
 * names both rather than guessing from the conventional namespace name.
 */
export function getAuthorizationPolicySelectorString(resource: any): string {
  const spec = resource.spec || {}
  const targets = [
    ...(Array.isArray(spec.targetRefs) ? spec.targetRefs : []),
    ...(spec.targetRef ? [spec.targetRef] : []),
  ].filter(Boolean)
  if (targets.length > 0) {
    return targets
      .map((t: any) => {
        const name = [t?.kind, t?.name].filter(Boolean).join('/') || 'target'
        return t?.namespace && t.namespace !== resource.metadata?.namespace
          ? `${t.namespace}/${name}`
          : name
      })
      .join(', ')
  }
  const entries = Object.entries(spec.selector?.matchLabels || {})
  if (entries.length === 0) return 'Namespace / mesh scope'
  return entries.map(([k, v]) => `${k}=${v}`).join(', ')
}

/**
 * What an AuthorizationPolicy's rule list means for the traffic it governs.
 *
 * Rules are alternatives and an unset list never matches, so BOTH actions with
 * no rules are inert in the same way — a DENY with none denies nothing, an
 * ALLOW with none permits nothing. Only the ALLOW case has a consequence worth
 * raising, because a workload with any ALLOW policy admits only what one of
 * them matches. Neither can be stated as an outcome for the workload: every
 * policy selecting it takes part in the decision.
 */
export function getAuthorizationPolicyRuleNotice(
  resource: any,
): { level: 'warning' | 'info'; title: string; message: string } | null {
  const action = resource?.spec?.action || 'ALLOW'
  const rules = resource?.spec?.rules
  if (Array.isArray(rules) && rules.length > 0) {
    // A rule matches every request when it carries no conditions — which
    // includes serialized empty lists, not just an empty object. On a DENY that
    // is not a baseline other policies carve exceptions out of: DENY is
    // evaluated first, so no ALLOW can re-permit what it matches. Unknown keys
    // are treated as conditions, so a future field cannot be read as blanket.
    const CONDITION_KEYS = ['from', 'to', 'when']
    const matchesEverything = rules.some((r: any) => {
      if (!r || typeof r !== 'object') return false
      const keys = Object.keys(r)
      if (keys.some(k => !CONDITION_KEYS.includes(k))) return false
      return CONDITION_KEYS.every(k => !Array.isArray(r[k]) || r[k].length === 0)
    })
    if (action === 'DENY' && matchesEverything) {
      return {
        level: 'info',
        title: 'Matches all requests',
        message: 'A rule with no conditions matches every request, so this policy denies all traffic to the workloads it selects. DENY is evaluated before ALLOW, so no ALLOW policy can permit an exception.',
      }
    }
    return null
  }
  if (action === 'ALLOW') {
    return {
      level: 'warning',
      title: 'No allow rules',
      message: 'Rules are alternatives, so an ALLOW policy with none of them matches nothing and contributes no permitted traffic. Other ALLOW policies selecting the same workload may still permit requests — a default-deny-plus-exceptions setup looks exactly like this.',
    }
  }
  if (action === 'DENY') {
    return {
      level: 'info',
      title: 'No deny rules',
      message: 'This DENY policy has no rules, so it matches no requests and denies no traffic. Other policies may still deny requests.',
    }
  }
  return null
}
