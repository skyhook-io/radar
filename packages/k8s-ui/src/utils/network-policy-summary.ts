import { effectivePolicyTypes, formatNetworkPolicyPort } from './network-policy'
import { formatKubernetesLabelSelector } from '../components/resources/resource-utils-calico'
import type { NetworkPolicyPeerType } from '../components/resources/renderers/network-policy-peer-styles'

export type PolicyGroupKind = 'any' | 'pods' | 'namespaces' | 'namespaceSelector' | 'podsInNamespaces' | 'ipBlocks'

export interface PolicyGroupItem {
  label: string
  detail?: string
  /** The raw values behind the chip: one, or every CIDR of a collapsed run. */
  members: string[]
}

export interface PolicyGroup {
  kind: PolicyGroupKind
  title: string
  peerType: NetworkPolicyPeerType
  items: PolicyGroupItem[]
  raw: string[]
}

export interface PolicyRuleSummary {
  index: number
  ports: string[]
  groups: PolicyGroup[]
}

export interface PolicyDirectionSummary {
  restricted: boolean
  denyAll: boolean
  rules: PolicyRuleSummary[]
}

export interface NetworkPolicySummary {
  target: string
  defaultedTypes: boolean
  ingress: PolicyDirectionSummary
  egress: PolicyDirectionSummary
}

const NAME_KEY = 'kubernetes.io/metadata.name'
const MIN_RUN = 3

const PEER_TYPE: Record<PolicyGroupKind, NetworkPolicyPeerType> = {
  any: 'all',
  pods: 'pod',
  namespaces: 'namespace',
  namespaceSelector: 'namespace',
  podsInNamespaces: 'combined',
  ipBlocks: 'cidr',
}

export function summarizeNetworkPolicy(spec: any, namespace?: string): NetworkPolicySummary {
  const types = effectivePolicyTypes(spec)
  const podSelector = spec?.podSelector
  return {
    target: isEmptySelector(podSelector) ? `All pods in ${namespace ?? 'this namespace'}` : formatKubernetesLabelSelector(podSelector),
    defaultedTypes: !types.explicit,
    ingress: summarizeDirection(spec?.ingress, types.ingress, 'from', namespace),
    egress: summarizeDirection(spec?.egress, types.egress, 'to', namespace),
  }
}

function summarizeDirection(rules: unknown, restricted: boolean, peerKey: 'from' | 'to', namespace?: string): PolicyDirectionSummary {
  const list: any[] = restricted && Array.isArray(rules) ? rules : []
  return {
    restricted,
    denyAll: restricted && list.length === 0,
    rules: list.map((rule, i) => ({
      index: i + 1,
      ports: Array.isArray(rule?.ports) ? rule.ports.map(formatNetworkPolicyPort) : [],
      groups: groupPeers(rule?.[peerKey], peerKey, namespace),
    })),
  }
}

function isEmptySelector(selector: any): boolean {
  if (!selector || typeof selector !== 'object') return true
  const labels = selector.matchLabels
  const expressions = selector.matchExpressions
  return (!labels || Object.keys(labels).length === 0) && (!Array.isArray(expressions) || expressions.length === 0)
}

/** The namespace names a selector picks when it only constrains the name label, else null. */
function selectedNames(selector: any): string[] | null {
  const sets: string[][] = []
  for (const [key, value] of Object.entries(selector?.matchLabels ?? {})) {
    if (key !== NAME_KEY) return null
    sets.push([String(value)])
  }
  for (const expr of Array.isArray(selector?.matchExpressions) ? selector.matchExpressions : []) {
    if (expr?.key !== NAME_KEY || expr?.operator !== 'In' || !Array.isArray(expr.values)) return null
    sets.push(expr.values.map(String))
  }
  if (sets.length === 0) return null
  // Selector requirements are ANDed, so the names are the intersection.
  return sets.reduce((acc, set) => acc.filter((name) => set.includes(name)))
}

const item = (label: string, detail?: string): PolicyGroupItem => ({ label, detail, members: [label] })
const unique = (values: string[]) => [...new Set(values)]

function groupPeers(peers: unknown, peerKey: 'from' | 'to', namespace?: string): PolicyGroup[] {
  if (!Array.isArray(peers) || peers.length === 0) {
    return [group('any', peerKey === 'from' ? 'Any source' : 'Any destination', [], [])]
  }

  let allPods = false
  let anyNamespace = false
  const podSelectors: string[] = []
  const names: string[] = []
  const nsSelectors: string[] = []
  const andPeers: PolicyGroupItem[] = []
  const plainCidrs: string[] = []
  const exceptItems: PolicyGroupItem[] = []
  const rawCidrs: string[] = []

  for (const peer of peers) {
    const hasPod = peer?.podSelector != null
    const hasNs = peer?.namespaceSelector != null
    if (peer?.ipBlock != null) {
      const cidr = typeof peer.ipBlock.cidr === 'string' && peer.ipBlock.cidr ? peer.ipBlock.cidr : '?'
      const except = Array.isArray(peer.ipBlock.except) ? peer.ipBlock.except.map(String) : []
      if (except.length > 0) {
        exceptItems.push(item(cidr, `except ${except.join(', ')}`))
        // The except list travels with the CIDR into filter and copy, or the range reads broader than it is.
        rawCidrs.push(`${cidr} except ${except.join(', ')}`)
      } else {
        plainCidrs.push(cidr)
        rawCidrs.push(cidr)
      }
    } else if (hasNs && (!hasPod || isEmptySelector(peer.podSelector))) {
      const nsSelector = peer.namespaceSelector
      const picked = selectedNames(nsSelector)
      if (isEmptySelector(nsSelector)) anyNamespace = true
      // Contradicting name requirements pick nothing; show the selector so the rule keeps a source.
      else if (picked && picked.length > 0) names.push(...picked)
      else nsSelectors.push(formatKubernetesLabelSelector(nsSelector))
    } else if (hasNs) {
      const ns = isEmptySelector(peer.namespaceSelector) ? 'any namespace' : formatKubernetesLabelSelector(peer.namespaceSelector)
      andPeers.push(item(formatKubernetesLabelSelector(peer.podSelector), `in ${ns}`))
    } else if (hasPod && isEmptySelector(peer.podSelector)) {
      allPods = true
    } else {
      podSelectors.push(hasPod ? formatKubernetesLabelSelector(peer.podSelector) : '?')
    }
  }

  const groups: PolicyGroup[] = []
  if (allPods) {
    groups.push(group('pods', `Any pod in ${namespace ?? 'this namespace'}`, [], []))
  } else if (podSelectors.length > 0) {
    const selectors = unique(podSelectors)
    groups.push(group('pods', `Pods in ${namespace ?? 'this namespace'} matching any of`, selectors.map((s) => item(s)), selectors))
  }
  if (anyNamespace) {
    groups.push(group('namespaces', 'Any pod in any namespace', [], []))
  } else {
    const picked = unique(names)
    if (picked.length > 0) {
      const title = picked.length === 1 ? `Any pod in ${picked[0]}` : `Any pod in ${picked.length} namespaces`
      // A single name is already in the title; a chip would repeat it.
      groups.push(group('namespaces', title, picked.length === 1 ? [] : picked.map((n) => item(n)), picked))
    }
    if (nsSelectors.length > 0) {
      const selectors = unique(nsSelectors)
      groups.push(group('namespaceSelector', 'Any pod in namespaces matching any of', selectors.map((s) => item(s)), selectors))
    }
  }
  if (andPeers.length > 0) {
    groups.push(group('podsInNamespaces', 'Pods in selected namespaces', andPeers, andPeers.map((p) => `${p.label} ${p.detail}`)))
  }
  if (rawCidrs.length > 0) {
    const blocks = unique(rawCidrs)
    const title = blocks.length === 1 ? '1 IP range' : `${blocks.length} IP ranges`
    groups.push(group('ipBlocks', title, [...collapseCidrRuns(plainCidrs), ...exceptItems], blocks))
  }
  return groups
}

function group(kind: PolicyGroupKind, title: string, items: PolicyGroupItem[], raw: string[]): PolicyGroup {
  return { kind, title, peerType: PEER_TYPE[kind], items, raw }
}

interface ParsedCidr {
  text: string
  octets: number[]
  prefix: number
  value: number
}

function parseIPv4Cidr(text: string): ParsedCidr | null {
  const m = /^(\d{1,3})\.(\d{1,3})\.(\d{1,3})\.(\d{1,3})\/(\d{1,2})$/.exec(text)
  if (!m) return null
  const octets = m.slice(1, 5).map(Number)
  const prefix = Number(m[5])
  if (octets.some((o) => o > 255) || prefix > 32) return null
  return { text, octets, prefix, value: ((octets[0] * 256 + octets[1]) * 256 + octets[2]) * 256 + octets[3] }
}

/** The one octet position where b is a+1 and every other octet matches, else -1. */
function stepOctet(a: ParsedCidr, b: ParsedCidr): number {
  if (a.prefix !== b.prefix) return -1
  let position = -1
  for (let k = 0; k < 4; k++) {
    if (a.octets[k] === b.octets[k]) continue
    if (position !== -1 || b.octets[k] !== a.octets[k] + 1) return -1
    position = k
  }
  return position
}

/**
 * IPv4 CIDRs sorted numerically, with runs of 3+ that step by one in a single
 * octet folded into one chip. IPv6 and unparsable values follow in input order.
 */
export function collapseCidrRuns(cidrs: string[]): PolicyGroupItem[] {
  const parsed: ParsedCidr[] = []
  const rest: string[] = []
  for (const text of unique(cidrs)) {
    const p = parseIPv4Cidr(text)
    if (p) parsed.push(p)
    else rest.push(text)
  }
  parsed.sort((a, b) => a.value - b.value || a.prefix - b.prefix)

  const items: PolicyGroupItem[] = []
  let i = 0
  while (i < parsed.length) {
    const k = i + 1 < parsed.length ? stepOctet(parsed[i], parsed[i + 1]) : -1
    let end = i
    if (k !== -1) {
      while (end + 1 < parsed.length && stepOctet(parsed[end], parsed[end + 1]) === k) end++
    }
    const length = end - i + 1
    if (length >= MIN_RUN) {
      const first = parsed[i]
      const octets = first.octets.map(String)
      octets[k] = `${first.octets[k]}–${parsed[end].octets[k]}`
      items.push({
        label: `${octets.join('.')}/${first.prefix}`,
        detail: `× ${length}`,
        members: parsed.slice(i, end + 1).map((p) => p.text),
      })
      i = end + 1
    } else {
      items.push(item(parsed[i].text))
      i++
    }
  }
  return [...items, ...rest.map((text) => item(text))]
}
