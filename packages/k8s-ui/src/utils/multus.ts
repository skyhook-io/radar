import { validateRFC1123Label, validateRFC1123Subdomain } from './validators'

export const MULTUS_NETWORKS_ANNOTATION = 'k8s.v1.cni.cncf.io/networks'
export const MULTUS_NETWORK_STATUS_ANNOTATION = 'k8s.v1.cni.cncf.io/network-status'

// Multus annotates under k8s.v1.cni.cncf.io but serves its CRD from k8s.cni.cncf.io.
export const NETWORK_ATTACHMENT_DEFINITION_GROUP = 'k8s.cni.cncf.io'
// Hyphenated, so it cannot be recovered by pluralizing the Kind.
export const NETWORK_ATTACHMENT_DEFINITION_PLURAL = 'network-attachment-definitions'

/** Identity of a NetworkAttachmentDefinition, always namespace-qualified. */
export interface NetworkAttachmentRef {
  namespace: string
  name: string
}

/** One attachment asked for in the `networks` annotation. */
export interface RequestedNetwork {
  /** As written; absent when the annotation named the definition bare. */
  namespace?: string
  name: string
  interfaceName?: string
  ips?: string[]
  mac?: string
  defaultRoute?: string[]
}

/** One entry of the `network-status` annotation, as Multus wrote it. */
export interface ReportedNetwork {
  /** Bare or `namespace/name`, depending on the Multus version. */
  name: string
  interfaceName?: string
  ips: string[]
  mac?: string
  isDefault: boolean
}

export type ParsedAnnotation<T> =
  | { state: 'absent' }
  | { state: 'invalid'; reason: string }
  | { state: 'ok'; items: T[] }

export interface NetworkAttachmentRow {
  /** Positional, because the same definition can be attached more than once. */
  key: string
  requested?: RequestedNetwork
  reported?: ReportedNetwork
  isDefaultNetwork: boolean
  /** Unset for the default network and for names that cannot identify a definition. */
  ref?: NetworkAttachmentRef
}

export interface PodNetworks {
  requested: ParsedAnnotation<RequestedNetwork>
  reported: ParsedAnnotation<ReportedNetwork>
  rows: NetworkAttachmentRow[]
}

export type NetworkAttachmentLookup = 'found' | 'missing' | 'forbidden' | 'unavailable' | 'unchecked'

/** Lookup results by `networkAttachmentKey`. A missing key means the lookup has not finished. */
export type NetworkAttachmentLookups = Record<string, NetworkAttachmentLookup>

interface PodWithAnnotations {
  metadata?: { namespace?: string; annotations?: Record<string, string> }
}

const ABSENT = { state: 'absent' } as const

function invalid(reason: string): { state: 'invalid'; reason: string } {
  return { state: 'invalid', reason }
}

function parsed<T>(items: T[]): ParsedAnnotation<T> {
  return items.length > 0 ? { state: 'ok', items } : ABSENT
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}

function optionalString(value: unknown): string | undefined {
  return typeof value === 'string' && value !== '' ? value : undefined
}

function optionalStrings(value: unknown): string[] | undefined {
  if (!Array.isArray(value)) return undefined
  const strings = value.filter((item): item is string => typeof item === 'string' && item !== '')
  return strings.length > 0 ? strings : undefined
}

function clip(text: string, max = 60): string {
  return text.length > max ? `${text.slice(0, max)}…` : text
}

function parseJSONList(text: string): { entries: unknown[] } | { reason: string } {
  let value: unknown
  try {
    value = JSON.parse(text)
  } catch {
    return { reason: 'not valid JSON' }
  }
  return Array.isArray(value) ? { entries: value } : { reason: 'expected a JSON list' }
}

function parseRequestedText(text: string): ParsedAnnotation<RequestedNetwork> {
  const items: RequestedNetwork[] = []
  for (const token of text.split(',')) {
    const entry = token.trim()
    if (!entry) continue
    const at = entry.indexOf('@')
    const reference = at === -1 ? entry : entry.slice(0, at)
    const interfaceName = at === -1 ? undefined : entry.slice(at + 1)
    const parts = reference.split('/')
    if (interfaceName === '') return invalid(`"${clip(entry)}" has an empty interface name`)
    if (parts.length > 2 || parts.some(part => part === '')) {
      return invalid(`"${clip(entry)}" is not name, namespace/name or name@interface`)
    }
    items.push(parts.length === 2
      ? { namespace: parts[0], name: parts[1], interfaceName }
      : { name: parts[0], interfaceName })
  }
  return parsed(items)
}

function parseRequestedList(text: string): ParsedAnnotation<RequestedNetwork> {
  const list = parseJSONList(text)
  if ('reason' in list) return invalid(list.reason)
  const items: RequestedNetwork[] = []
  for (let index = 0; index < list.entries.length; index++) {
    const entry = list.entries[index]
    const name = isRecord(entry) ? optionalString(entry.name)?.trim() : undefined
    if (!isRecord(entry) || !name) return invalid(`entry ${index + 1} has no name`)
    items.push({
      name,
      namespace: optionalString(entry.namespace),
      interfaceName: optionalString(entry.interface),
      ips: optionalStrings(entry.ips),
      mac: optionalString(entry.mac),
      defaultRoute: optionalStrings(entry['default-route']),
    })
  }
  return parsed(items)
}

/** Parses the `k8s.v1.cni.cncf.io/networks` annotation: a comma-separated list or a JSON list. */
export function parseRequestedNetworks(value: string | undefined): ParsedAnnotation<RequestedNetwork> {
  const text = value?.trim()
  if (!text) return ABSENT
  if (text.startsWith('[')) return parseRequestedList(text)
  if (text.startsWith('{')) return invalid('expected a comma-separated list or a JSON list')
  return parseRequestedText(text)
}

/** Parses the `k8s.v1.cni.cncf.io/network-status` annotation. */
export function parseNetworkStatus(value: string | undefined): ParsedAnnotation<ReportedNetwork> {
  const text = value?.trim()
  if (!text) return ABSENT
  const list = parseJSONList(text)
  if ('reason' in list) return invalid(list.reason)
  const items: ReportedNetwork[] = []
  for (let index = 0; index < list.entries.length; index++) {
    const entry = list.entries[index]
    const name = isRecord(entry) ? optionalString(entry.name)?.trim() : undefined
    if (!isRecord(entry) || !name) return invalid(`entry ${index + 1} has no name`)
    items.push({
      name,
      interfaceName: optionalString(entry.interface),
      ips: optionalStrings(entry.ips) ?? [],
      mac: optionalString(entry.mac),
      isDefault: entry.default === true,
    })
  }
  return parsed(items)
}

export function networkAttachmentKey(ref: NetworkAttachmentRef): string {
  return `${ref.namespace}/${ref.name}`
}

function splitQualifiedName(name: string): { namespace?: string; name: string } {
  const slash = name.indexOf('/')
  return slash === -1 ? { name } : { namespace: name.slice(0, slash), name: name.slice(slash + 1) }
}

// Names come from a user-controlled annotation and end up in a request path.
function toRef(namespace: string, name: string): NetworkAttachmentRef | undefined {
  return validateRFC1123Label(namespace).valid && validateRFC1123Subdomain(name).valid
    ? { namespace, name }
    : undefined
}

function buildRows(podNamespace: string, requested: RequestedNetwork[], reported: ReportedNetwork[]): NetworkAttachmentRow[] {
  const requestedId = (network: RequestedNetwork) => `${network.namespace ?? podNamespace}/${network.name}`
  const reportedId = (network: ReportedNetwork) => {
    const { namespace, name } = splitQualifiedName(network.name)
    return `${namespace ?? podNamespace}/${name}`
  }

  const secondary = reported.filter(network => !network.isDefault)
  const claimed = new Set<ReportedNetwork>()
  const claim = (matches: (network: ReportedNetwork) => boolean) => {
    const found = secondary.find(network => !claimed.has(network) && matches(network))
    if (found) claimed.add(found)
    return found
  }

  const paired: (ReportedNetwork | undefined)[] = requested.map(() => undefined)
  // An explicit interface name is the strongest evidence of which entry is which.
  requested.forEach((network, index) => {
    if (!network.interfaceName) return
    paired[index] = claim(candidate =>
      candidate.interfaceName === network.interfaceName && reportedId(candidate) === requestedId(network))
  })
  // Multus attaches in annotation order, so repeats of one definition line up by position.
  // A named interface that differs from the reported one is a different attachment.
  requested.forEach((network, index) => {
    if (paired[index]) return
    paired[index] = claim(candidate =>
      reportedId(candidate) === requestedId(network)
      && !(network.interfaceName && candidate.interfaceName && network.interfaceName !== candidate.interfaceName))
  })

  const rows: NetworkAttachmentRow[] = []
  reported.filter(network => network.isDefault).forEach((network, index) => {
    rows.push({ key: `default-${index}`, reported: network, isDefaultNetwork: true })
  })
  requested.forEach((network, index) => {
    rows.push({
      key: `requested-${index}`,
      requested: network,
      reported: paired[index],
      isDefaultNetwork: false,
      ref: toRef(network.namespace ?? podNamespace, network.name),
    })
  })
  secondary.filter(network => !claimed.has(network)).forEach((network, index) => {
    const { namespace, name } = splitQualifiedName(network.name)
    rows.push({
      key: `reported-${index}`,
      reported: network,
      isDefaultNetwork: false,
      ref: toRef(namespace ?? podNamespace, name),
    })
  })
  return rows
}

/** Reads a Pod's Multus annotations into rows pairing each requested attachment with what Multus reported. */
export function getPodNetworks(pod: PodWithAnnotations | undefined): PodNetworks {
  const annotations = pod?.metadata?.annotations
  const requested = parseRequestedNetworks(annotations?.[MULTUS_NETWORKS_ANNOTATION])
  const reported = parseNetworkStatus(annotations?.[MULTUS_NETWORK_STATUS_ANNOTATION])
  return {
    requested,
    reported,
    rows: buildRows(
      pod?.metadata?.namespace ?? '',
      requested.state === 'ok' ? requested.items : [],
      reported.state === 'ok' ? reported.items : [],
    ),
  }
}

/** False for a Pod that only has its default network, which is every Pod on some clusters. */
export function hasSecondaryNetworks(networks: PodNetworks): boolean {
  return networks.requested.state === 'invalid'
    || networks.reported.state === 'invalid'
    || networks.rows.some(row => !row.isDefaultNetwork)
}

export function uniqueNetworkAttachmentRefs(rows: NetworkAttachmentRow[]): NetworkAttachmentRef[] {
  const refs = new Map<string, NetworkAttachmentRef>()
  for (const row of rows) {
    if (row.ref) refs.set(networkAttachmentKey(row.ref), row.ref)
  }
  return Array.from(refs.values())
}
