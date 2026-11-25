import { isApiGroup } from './resource-utils-cnpg'
import { healthColors, type StatusBadge } from './resource-utils'

export const METALLB_GROUP = 'metallb.io'
const COMPONENT_LABEL = 'metallb.io/component-type'
const NODE_LABEL = 'metallb.io/node-name'

const isMetalLBKind = (data: any, kind: string): boolean => data?.kind === kind && isApiGroup(data.apiVersion, METALLB_GROUP)

export const isMetalLBIPAddressPool = (data: any) => isMetalLBKind(data, 'IPAddressPool')
export const isMetalLBL2Advertisement = (data: any) => isMetalLBKind(data, 'L2Advertisement')
export const isMetalLBBGPAdvertisement = (data: any) => isMetalLBKind(data, 'BGPAdvertisement')
export const isMetalLBBGPPeer = (data: any) => isMetalLBKind(data, 'BGPPeer')
export const isMetalLBConfigurationState = (data: any) => isMetalLBKind(data, 'ConfigurationState')

const badge = (text: string, level: StatusBadge['level']): StatusBadge => ({ text, level, color: healthColors[level] })

function nonEmptyString(v: unknown): string | undefined {
  return typeof v === 'string' && v ? v : undefined
}

function stringList(v: unknown): string[] {
  return Array.isArray(v) ? v.filter((s): s is string => typeof s === 'string' && s !== '') : []
}

function count(v: unknown): number {
  return typeof v === 'number' && Number.isFinite(v) && v > 0 ? v : 0
}

export interface MetalLBPoolCounters {
  assigned4: number
  available4: number
  assigned6: number
  available6: number
}

/**
 * The address counters the controller writes to a pool's status (MetalLB
 * 0.15 and later). Missing, or lacking any of the four, means unknown: an
 * older MetalLB, a pool outside MetalLB's namespace, or one not reconciled yet.
 */
export function getMetalLBPoolCounters(data: any): MetalLBPoolCounters | undefined {
  const status = data?.status
  if (!status || typeof status !== 'object') return undefined
  // The controller writes all four counters together, so a status missing
  // any of them has not been reported, rather than reporting zero.
  const keys = ['assignedIPv4', 'availableIPv4', 'assignedIPv6', 'availableIPv6']
  if (!keys.every((k) => typeof status[k] === 'number' && Number.isFinite(status[k]))) return undefined
  return {
    assigned4: count(status.assignedIPv4),
    available4: count(status.availableIPv4),
    assigned6: count(status.assignedIPv6),
    available6: count(status.availableIPv6),
  }
}

/**
 * MetalLB reports the largest int64 for an IPv6 range with 62 or more host
 * bits, which it treats as never running out; past 2^53 a JSON number is not
 * exact anyway.
 */
export function formatMetalLBCount(n: number): string {
  return n > Number.MAX_SAFE_INTEGER ? 'Unlimited' : n.toLocaleString()
}

/**
 * Address availability as the controller counts it. Counts are addresses,
 * not Services: Services can share one. A pool the controller has not
 * loaded, for example because it rejected the configuration, reports zero
 * for everything. Mirrors metalLBPoolState in pkg/ai/context.
 */
export function getMetalLBPoolStatus(data: any): StatusBadge {
  if (data?.metadata?.deletionTimestamp) return badge('Terminating', 'neutral')
  const c = getMetalLBPoolCounters(data)
  if (!c) return badge('Not reported', 'unknown')
  const has4 = c.assigned4 + c.available4 > 0
  const has6 = c.assigned6 + c.available6 > 0
  if (!has4 && !has6) return badge('No addresses', 'degraded')
  const out4 = has4 && c.available4 === 0
  const out6 = has6 && c.available6 === 0
  if ((out4 || !has4) && (out6 || !has6)) return badge('Exhausted', 'degraded')
  if (out4) return badge('IPv4 exhausted', 'degraded')
  if (out6) return badge('IPv6 exhausted', 'degraded')
  return badge('Available', 'healthy')
}

/** "assigned / total" per family the pool has addresses in. */
export function describeMetalLBPoolUsage(c: MetalLBPoolCounters): string {
  const parts: string[] = []
  if (c.assigned4 + c.available4 > 0) parts.push(`IPv4 ${formatMetalLBCount(c.assigned4)} / ${formatMetalLBCount(c.assigned4 + c.available4)}`)
  if (c.assigned6 + c.available6 > 0) parts.push(`IPv6 ${formatMetalLBCount(c.assigned6)} / ${formatMetalLBCount(c.assigned6 + c.available6)}`)
  return parts.join(' · ')
}

/** autoAssign defaults to true; false means only Services that request the pool get its addresses. */
export function isMetalLBPoolAutoAssign(data: any): boolean {
  return data?.spec?.autoAssign !== false
}

export interface MetalLBAdvertisedPools {
  names: string[]
  selectors: any[]
  /** Neither names nor selectors: the advertisement covers every pool. */
  all: boolean
}

export function getMetalLBAdvertisedPools(data: any): MetalLBAdvertisedPools {
  const names = stringList(data?.spec?.ipAddressPools)
  const selectors = Array.isArray(data?.spec?.ipAddressPoolSelectors) ? data.spec.ipAddressPoolSelectors : []
  return { names, selectors, all: names.length === 0 && selectors.length === 0 }
}

function describeSelectors(selectors: unknown): string {
  if (!Array.isArray(selectors) || selectors.length === 0) return ''
  return selectors.map((s: any) => {
    const labels = s?.matchLabels && typeof s.matchLabels === 'object' ? Object.entries(s.matchLabels).map(([k, v]) => `${k}=${v}`) : []
    const expressions = Array.isArray(s?.matchExpressions)
      ? s.matchExpressions.map((e: any) => `${e?.key} ${String(e?.operator ?? '').toLowerCase()}${Array.isArray(e?.values) && e.values.length ? ` (${e.values.join(', ')})` : ''}`)
      : []
    return [...labels, ...expressions].join(', ') || 'everything'
  }).join(' or ')
}

function describeAdvertisedPools(data: any): string {
  const pools = getMetalLBAdvertisedPools(data)
  if (pools.all) return 'All pools'
  const selectors = describeSelectors(pools.selectors)
  return [...pools.names, ...(selectors ? [`pools matching ${selectors}`] : [])].join(', ')
}

function describeNodes(data: any): string {
  return describeSelectors(data?.spec?.nodeSelectors) || 'All nodes'
}

export interface MetalLBComponent {
  component: 'controller' | 'speaker' | undefined
  node?: string
}

export function getMetalLBComponent(data: any): MetalLBComponent {
  const labels = data?.metadata?.labels ?? {}
  const component = labels[COMPONENT_LABEL] === 'controller' || labels[COMPONENT_LABEL] === 'speaker' ? labels[COMPONENT_LABEL] : undefined
  return { component, node: nonEmptyString(labels[NODE_LABEL]) }
}

function describeComponent(data: any): string {
  const { component, node } = getMetalLBComponent(data)
  if (component === 'controller') return 'Controller'
  if (component === 'speaker') return node ? `Speaker on ${node}` : 'Speaker'
  return ''
}

/**
 * Whether a MetalLB component accepted the configuration. Invalid means a
 * reconciler could not parse it, and the component keeps the last
 * configuration it accepted; failures that are not parse errors leave it
 * Valid. Mirrors summarizeMetalLBConfigurationState in pkg/ai/context.
 */
export function getMetalLBConfigurationStateStatus(data: any): StatusBadge {
  if (data?.metadata?.deletionTimestamp) return badge('Terminating', 'neutral')
  switch (data?.status?.result) {
    case 'Valid': return badge('Valid', 'healthy')
    case 'Invalid': return badge('Invalid', 'degraded')
    case 'Unknown': return badge('Unknown', 'unknown')
    default: return badge('Not reported', 'unknown')
  }
}

export function getMetalLBConfigurationErrors(data: any): string[] {
  const summary = data?.status?.errorSummary
  return typeof summary === 'string' ? summary.split('\n').map(s => s.trim()).filter(Boolean) : []
}

export function getMetalLBColumnValue(data: any, column: string): string | undefined {
  if (isMetalLBIPAddressPool(data)) {
    switch (column) {
      case 'status': return getMetalLBPoolStatus(data).text
      case 'poolAddresses': return stringList(data.spec?.addresses).join(', ')
      case 'poolAssigned': {
        const c = getMetalLBPoolCounters(data)
        return c ? describeMetalLBPoolUsage(c) : ''
      }
      case 'poolAutoAssign': return isMetalLBPoolAutoAssign(data) ? 'Yes' : 'No'
      case 'poolAvoidBuggyIPs': return data.spec?.avoidBuggyIPs === true ? 'Yes' : 'No'
      default: return undefined
    }
  }
  if (isMetalLBL2Advertisement(data)) {
    switch (column) {
      case 'advertisedPools': return describeAdvertisedPools(data)
      case 'advertisedNodes': return describeNodes(data)
      case 'l2Interfaces': return stringList(data.spec?.interfaces).join(', ') || 'All'
      default: return undefined
    }
  }
  if (isMetalLBBGPAdvertisement(data)) {
    switch (column) {
      case 'advertisedPools': return describeAdvertisedPools(data)
      case 'advertisedNodes': return describeNodes(data)
      case 'bgpPeers': return stringList(data.spec?.peers).join(', ') || 'All peers'
      case 'bgpCommunities': return stringList(data.spec?.communities).join(', ')
      default: return undefined
    }
  }
  if (isMetalLBBGPPeer(data)) {
    const spec = data.spec ?? {}
    switch (column) {
      case 'peerAddress': return nonEmptyString(spec.peerAddress) ?? (nonEmptyString(spec.interface) ? `Interface ${spec.interface}` : '')
      case 'peerASN': return count(spec.peerASN) ? String(spec.peerASN) : nonEmptyString(spec.dynamicASN) ? `Detected (${spec.dynamicASN})` : ''
      case 'myASN': return count(spec.myASN) ? String(spec.myASN) : ''
      case 'bfdProfile': return nonEmptyString(spec.bfdProfile) ?? ''
      case 'ebgpMultiHop': return spec.ebgpMultiHop === true ? 'Yes' : 'No'
      default: return undefined
    }
  }
  if (isMetalLBConfigurationState(data)) {
    switch (column) {
      case 'status': return getMetalLBConfigurationStateStatus(data).text
      case 'configComponent': return describeComponent(data)
      case 'configErrors': return getMetalLBConfigurationErrors(data).join('; ')
      default: return undefined
    }
  }
  return undefined
}

/** Pools sort by the share of their addresses in use; unreported counters sort first. */
export function getMetalLBSortValue(data: any, column: string): string | number | undefined {
  const value = getMetalLBColumnValue(data, column)
  if (value === undefined) return undefined
  if (column === 'poolAssigned') {
    const c = getMetalLBPoolCounters(data)
    const total = c ? c.assigned4 + c.available4 + c.assigned6 + c.available6 : 0
    return c && total > 0 ? (c.assigned4 + c.assigned6) / total : -1
  }
  if (column === 'peerASN' || column === 'myASN') return count(data.spec?.[column]) || -1
  return value
}
