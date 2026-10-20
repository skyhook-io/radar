import { getCNPGPostgresMajor } from '../resources/resource-utils-cnpg'
import { cnpgRoleState } from './databaseRole'
import { cnpgFormatBytes } from './workspace'
import { type Fact } from '../facts'

/** Where a Subscription's publisher lives, as far as the subscriber's spec shows. */
export type CNPGPublisher =
  | { kind: 'cluster'; namespace: string; name: string; via: string; cluster: any }
  | { kind: 'external'; host?: string; reason: string }

export interface CNPGLogicalPath {
  subscription: { namespace: string; name: string; sqlName?: string; dbname?: string; cluster?: string; applied: boolean | null; message?: string }
  externalCluster: { name?: string; declared: boolean; host?: string; dbname?: string }
  publisher: CNPGPublisher
  publication: {
    name?: string
    dbname?: string
    /** The Publication object on the publisher Cluster declaring it, when one is visible. */
    object?: { namespace: string; name: string; applied: boolean | null }
    /** Why Publication objects in the publisher's namespace could not be read; absence then proves nothing. */
    unavailable?: string
  }
  /** The slot PostgreSQL creates for the subscription: `slot_name`, else the subscription's name. */
  slot: { name?: string; reason?: string }
  failover: Fact
}

const SERVICE_SUFFIXES = ['-rw', '-ro', '-r']

function stripHost(host: string): { name: string; namespace?: string } {
  const h = host.trim().toLowerCase().replace(/\.$/, '').replace(/\.svc\.cluster\.local$/, '').replace(/\.svc$/, '')
  const [name, namespace] = h.split('.')
  return { name, namespace }
}

/**
 * Resolves an external cluster's host to a visible CNPG Cluster through its
 * -rw / -ro / -r Service or a Pooler's Service. Anything else stays external:
 * the host is never guessed to be a cluster it only resembles.
 */
export function cnpgResolvePublisher(host: string | undefined, subscriberNs: string, clusters: any[], poolers: any[]): CNPGPublisher {
  if (!host) return { kind: 'external', reason: 'the external cluster names no host' }
  const { name, namespace = subscriberNs } = stripHost(host)
  if (host.includes('.') && !/\.svc(\.cluster\.local)?\.?$/i.test(host.trim()) && host.split('.').length > 2) {
    return { kind: 'external', host, reason: 'the host is not a Kubernetes Service name' }
  }
  const inNs = (list: any[]) => list.filter((o) => o?.metadata?.namespace === namespace)
  for (const suffix of SERVICE_SUFFIXES) {
    if (!name.endsWith(suffix)) continue
    const base = name.slice(0, -suffix.length)
    const c = inNs(clusters).find((x) => x.metadata?.name === base)
    if (c) return { kind: 'cluster', namespace, name: base, via: `${name}.${namespace}`, cluster: c }
  }
  const pooler = inNs(poolers).find((p) => p.metadata?.name === name)
  if (pooler?.spec?.cluster?.name) {
    const c = inNs(clusters).find((x) => x.metadata?.name === pooler.spec.cluster.name)
    if (c) return { kind: 'cluster', namespace, name: c.metadata.name, via: `Pooler ${name}`, cluster: c }
  }
  return { kind: 'external', host, reason: 'no visible CloudNativePG Cluster serves this host' }
}

/**
 * The namespace a Subscription's publisher host names, so a host can read that
 * namespace too before resolving the publisher. Undefined when the subscriber
 * or its external cluster is not visible, or the host names none.
 */
export function cnpgSubscriptionHostNamespace(subscription: any, clusters: any[]): string | undefined {
  const ns = subscription?.metadata?.namespace
  const subscriber = clusters.find((c) => c?.metadata?.namespace === ns && c?.metadata?.name === subscription?.spec?.cluster?.name)
  const ext = (subscriber?.spec?.externalClusters ?? []).find((e: any) => e?.name === subscription?.spec?.externalClusterName)
  const host = ext?.connectionParameters?.host
  return host ? stripHost(host).namespace ?? ns : undefined
}


function truthy(v: unknown): boolean | undefined {
  if (v === undefined || v === null) return undefined
  const s = String(v).trim().toLowerCase()
  if (['true', 'on', 'yes', '1'].includes(s)) return true
  if (['false', 'off', 'no', '0'].includes(s)) return false
  return undefined
}

const FAILOVER_SOURCE = "Publisher's spec.replicationSlots.highAvailability (enabled and synchronizeLogicalDecoding), its PostgreSQL major and the Subscription's failover parameter; /pg/status does not report a slot's failover flag"

/**
 * Whether the publisher's slot for this subscription is kept on its standbys,
 * so a failover of the publisher does not lose it. Declared configuration
 * only: CloudNativePG does not report which slots were actually synchronized.
 */
export function cnpgSlotFailover(publisher: CNPGPublisher, subscription: any): Fact {
  if (publisher.kind !== 'cluster') {
    return { text: 'Unknown: the publisher is not a CloudNativePG Cluster Radar can see', tone: 'unknown', source: FAILOVER_SOURCE }
  }
  const c = publisher.cluster
  if ((c?.spec?.instances ?? 1) <= 1) {
    return { text: 'No standby to fail over to: a single-instance publisher', tone: 'neutral', source: FAILOVER_SOURCE }
  }
  const ha = c?.spec?.replicationSlots?.highAvailability
  // GetEnabled defaults to true; the operator enables sync only with both.
  if (ha?.synchronizeLogicalDecoding === true && ha?.enabled === false) {
    return {
      text: 'Lost on failover: synchronizeLogicalDecoding is on, but HA replication slots are disabled, so standbys have no physical slot to synchronize through',
      tone: 'degraded',
      source: FAILOVER_SOURCE,
    }
  }
  const sync = ha?.synchronizeLogicalDecoding === true
  if (!sync) {
    return {
      text: 'Lost on failover: the publisher does not synchronize logical slots to standbys (synchronizeLogicalDecoding is off)',
      tone: 'degraded',
      source: FAILOVER_SOURCE,
    }
  }
  const major = getCNPGPostgresMajor(c)
  if (major === undefined) {
    return { text: 'Unknown: synchronization is on, but the publisher\'s PostgreSQL major is not reported', tone: 'unknown', source: FAILOVER_SOURCE }
  }
  if (major < 17) {
    return {
      text: 'Synchronized only if pg_failover_slots is loaded on the publisher (PostgreSQL < 17); Radar cannot tell',
      tone: 'unknown',
      source: FAILOVER_SOURCE,
    }
  }
  const failover = truthy(subscription?.spec?.parameters?.failover)
  if (failover === true) {
    return { text: 'Kept on standbys (declared): synchronization is on and the subscription requests failover', tone: 'healthy', source: FAILOVER_SOURCE }
  }
  return {
    text: "Lost on failover: PostgreSQL 17 synchronizes only slots created with failover = true, and the subscription's parameters do not set it",
    tone: 'degraded',
    source: FAILOVER_SOURCE,
  }
}

export function cnpgLogicalPaths(
  subscriptions: any[],
  clusters: any[],
  publications: any[],
  poolers: any[],
  /** Why Publications in a namespace are not readable (coverage), or null when they are. */
  publicationsUnavailable?: (namespace: string) => string | null,
): CNPGLogicalPath[] {
  return subscriptions.map((sub) => {
    const ns: string = sub?.metadata?.namespace ?? ''
    const subscriber = clusters.find((c) => c?.metadata?.namespace === ns && c?.metadata?.name === sub?.spec?.cluster?.name)
    const extName: string | undefined = sub?.spec?.externalClusterName
    const ext = (subscriber?.spec?.externalClusters ?? []).find((e: any) => e?.name === extName)
    const params = ext?.connectionParameters ?? {}
    const publisher: CNPGPublisher = !subscriber
      ? { kind: 'external', reason: 'the subscriber Cluster is not visible, so its external cluster cannot be read' }
      : !ext
        ? { kind: 'external', reason: `external cluster ${extName ?? '(unset)'} is not declared on ${subscriber.metadata?.name}` }
        : cnpgResolvePublisher(params.host, ns, clusters, poolers)
    const pubDb: string | undefined = sub?.spec?.publicationDBName || params.dbname
    const pubName: string | undefined = sub?.spec?.publicationName
    const pubObj =
      publisher.kind === 'cluster'
        ? publications.find(
            (p) =>
              p?.metadata?.namespace === publisher.namespace &&
              p?.spec?.cluster?.name === publisher.name &&
              p?.spec?.name === pubName &&
              (!pubDb || p?.spec?.dbname === pubDb),
          )
        : undefined
    const slotParam: string | undefined = sub?.spec?.parameters?.slot_name
    const createSlot = truthy(sub?.spec?.parameters?.create_slot)
    const slot =
      slotParam && slotParam.toLowerCase() === 'none'
        ? { reason: 'slot_name = NONE: the subscription uses no slot' }
        : slotParam
          ? { name: slotParam }
          : createSlot === false
            ? { name: sub?.spec?.name, reason: 'create_slot = false: the slot must be created by hand' }
            : { name: sub?.spec?.name }
    return {
      subscription: {
        namespace: ns,
        name: sub?.metadata?.name,
        sqlName: sub?.spec?.name,
        dbname: sub?.spec?.dbname,
        cluster: sub?.spec?.cluster?.name,
        applied: cnpgRoleState(sub) === 'pending' ? null : cnpgRoleState(sub) === 'applied',
        message: sub?.status?.message || undefined,
      },
      externalCluster: { name: extName, declared: !!ext, host: params.host, dbname: params.dbname },
      publisher,
      publication: {
        name: pubName,
        dbname: pubDb,
        object: pubObj
          ? { namespace: pubObj.metadata.namespace, name: pubObj.metadata.name, applied: cnpgRoleState(pubObj) === 'pending' ? null : cnpgRoleState(pubObj) === 'applied' }
          : undefined,
        unavailable: !pubObj && publisher.kind === 'cluster' ? publicationsUnavailable?.(publisher.namespace) ?? undefined : undefined,
      },
      slot,
      failover: cnpgSlotFailover(publisher, sub),
    }
  })
}

/** What the publisher primary's instance manager says about one slot, in a shape independent of the runtime API. */
export interface CNPGPublisherSlots {
  /** partial: the report was capped or incomplete, so a slot missing from it may exist. */
  state: 'ok' | 'partial' | 'denied' | 'unavailable' | 'notRead'
  reason?: string
  /** The latest refresh failed; slots are from an earlier read. */
  stale?: boolean
  slots?: { name: string; type?: string; active?: boolean; walStatus?: string; retainedBytes?: number; database?: string }[]
}

const SLOT_SOURCE = "Publisher primary's instance manager (/pg/status replicationSlotsInfo) and exporter (retained WAL)"

export function cnpgLogicalSlotFact(path: CNPGLogicalPath, observed: CNPGPublisherSlots): Fact {
  if (!path.slot.name) return { text: path.slot.reason ?? 'No slot', tone: 'neutral' }
  if (path.publisher.kind !== 'cluster') return { text: `Slot ${path.slot.name}: not observable (publisher outside this cluster's view)`, tone: 'unknown' }
  if (observed.state === 'denied') return { text: `Slot ${path.slot.name}: no access (needs get pods/proxy on the publisher)`, tone: 'unknown', source: SLOT_SOURCE }
  if ((observed.state !== 'ok' && observed.state !== 'partial') || !observed.slots) {
    return { text: `Slot ${path.slot.name}: not read${observed.reason ? ` (${observed.reason})` : ''}`, tone: 'unknown', source: SLOT_SOURCE }
  }
  const s = observed.slots.find((x) => x.name === path.slot.name)
  if (!s && (observed.state === 'partial' || observed.stale)) {
    return {
      text: `Slot ${path.slot.name}: not in the reported slots (${observed.stale ? 'from an earlier read; the latest refresh failed' : `report incomplete${observed.reason ? `: ${observed.reason}` : ''}`})`,
      tone: 'unknown',
      source: SLOT_SOURCE,
    }
  }
  if (!s) {
    return {
      text: `Slot ${path.slot.name} not found on the publisher primary${path.slot.reason ? `: ${path.slot.reason}` : ''}`,
      tone: path.subscription.applied === true ? 'degraded' : 'unknown',
      source: SLOT_SOURCE,
    }
  }
  const parts = [`Slot ${s.name}`, s.type ?? 'type unknown', s.active === undefined ? 'activity unknown' : s.active ? 'active' : 'inactive']
  if (s.retainedBytes !== undefined) parts.push(`retains ${cnpgFormatBytes(s.retainedBytes)} of WAL`)
  if (s.walStatus) parts.push(`WAL ${s.walStatus}`)
  const bad = s.active === false || s.walStatus === 'lost' || s.walStatus === 'unreserved'
  if (observed.stale) {
    return { text: `${parts.join(' · ')} (from an earlier read; the latest refresh failed)`, tone: 'unknown', source: SLOT_SOURCE }
  }
  return { text: parts.join(' · '), tone: s.walStatus === 'lost' ? 'unhealthy' : bad ? 'degraded' : 'healthy', source: SLOT_SOURCE }
}

/** "cluster/database", with an unknown database said in words rather than as "?". */
export function cnpgLogicalLocation(where: string, dbname: string | undefined): string {
  return dbname ? `${where}/${dbname}` : `${where} · database unknown`
}
