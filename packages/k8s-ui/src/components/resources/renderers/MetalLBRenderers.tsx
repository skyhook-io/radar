import { Fragment, type ReactNode } from 'react'
import { Network, Radio, Share2, Filter, ShieldCheck } from 'lucide-react'
import type { ResourceRef } from '../../../types'
import { Badge } from '../../ui/Badge'
import { healthToSeverity } from '../../../utils/badge-colors'
import { Section, Property, PropertyList, ResourceLink, AlertBanner, LabelSelectorDisplay, useOperationalIssuesShown } from '../../ui/drawer-components'
import {
  METALLB_GROUP,
  getMetalLBPoolStatus,
  getMetalLBPoolCounters,
  formatMetalLBCount,
  isMetalLBPoolAutoAssign,
  getMetalLBAdvertisedPools,
  getMetalLBConfigurationStateStatus,
  getMetalLBConfigurationErrors,
  getMetalLBComponent,
} from '../resource-utils-metallb'

type Navigate = (ref: ResourceRef) => void

const NO_SESSION_NOTE = 'Configuration only. It does not show which node announces a Service, whether a BGP session is established, or whether the address is reachable.'

function stringList(v: unknown): string[] {
  return Array.isArray(v) ? v.filter((s): s is string => typeof s === 'string' && s !== '') : []
}

function selectorList(v: unknown): any[] {
  return Array.isArray(v) ? v : []
}

/** Selectors in a MetalLB list are ORed: an object matching any of them is selected. */
function Selectors({ selectors, emptyText }: { selectors: any[]; emptyText: string }) {
  if (selectors.length === 0) return <span className="text-theme-text-tertiary">{emptyText}</span>
  return <div className="space-y-1">
    {selectors.map((selector, i) => <Fragment key={i}>
      {i > 0 && <div className="text-xs text-theme-text-tertiary">or</div>}
      <LabelSelectorDisplay selector={selector} emptyText="Everything" />
    </Fragment>)}
  </div>
}

function LinkList({ names, render }: { names: string[]; render: (name: string) => ReactNode }) {
  return <span className="flex flex-wrap gap-x-2 gap-y-1">{names.map(name => <span key={name}>{render(name)}</span>)}</span>
}

function PoolLinks({ data, onNavigate }: { data: any; onNavigate?: Navigate }) {
  const pools = getMetalLBAdvertisedPools(data)
  const namespace = data.metadata?.namespace ?? ''
  if (pools.all) return <>All pools</>
  return <div className="space-y-1">
    {pools.names.length > 0 && <LinkList names={pools.names} render={name => <ResourceLink kind="IPAddressPool" group={METALLB_GROUP} name={name} namespace={namespace} onNavigate={onNavigate} />} />}
    {pools.selectors.length > 0 && <div>
      <div className="text-xs text-theme-text-tertiary">{pools.names.length > 0 ? 'and pools matching' : 'Pools matching'}</div>
      <Selectors selectors={pools.selectors} emptyText="Every pool" />
    </div>}
  </div>
}

export function MetalLBIPAddressPoolRenderer({ data }: { data: any }) {
  const state = getMetalLBPoolStatus(data)
  const spec = data.spec ?? {}
  const counters = getMetalLBPoolCounters(data)
  const allocation = spec.serviceAllocation && typeof spec.serviceAllocation === 'object' ? spec.serviceAllocation : undefined
  const has4 = !!counters && counters.assigned4 + counters.available4 > 0
  const has6 = !!counters && counters.assigned6 + counters.available6 > 0
  return <>
    {state.text === 'No addresses' && <AlertBanner variant="warning" title="MetalLB reports no addresses in this pool" message="The controller is not allocating from it. It reports zero addresses for a pool it has not loaded, for example when it rejected the configuration; check the MetalLB ConfigurationStates." />}
    {(state.text === 'Exhausted' || state.text === 'IPv4 exhausted' || state.text === 'IPv6 exhausted') && <AlertBanner
      variant="warning"
      title={state.text === 'Exhausted' ? 'Every address in this pool is assigned' : `Every ${state.text.slice(0, 4)} address in this pool is assigned`}
      message="A new LoadBalancer Service that can only use this pool stays pending until an address is released. Services can share an address, so this does not count Services."
    />}
    {state.text === 'Not reported' && <AlertBanner variant="info" title="No address counters reported" message="MetalLB 0.15 and later report assigned and available addresses here, once the controller has reconciled the pool. Older versions do not." />}
    <Section title="IP Address Pool" icon={Network} defaultExpanded>
      <PropertyList>
        <Property label="Status" value={<Badge severity={healthToSeverity(state.level)}>{state.text}</Badge>} />
        <Property label="Addresses" value={stringList(spec.addresses).length > 0
          ? <div className="space-y-0.5 font-mono text-xs">{stringList(spec.addresses).map(a => <div key={a}>{a}</div>)}</div>
          : 'Not specified'} />
        <Property label="Auto assign" value={isMetalLBPoolAutoAssign(data) ? 'Yes' : 'No, only to Services that request this pool'} />
        <Property label="Avoid buggy IPs" value={spec.avoidBuggyIPs === true ? 'Yes, skips .0 and .255 addresses' : 'No'} />
        {has4 && <Property label="IPv4 assigned" value={`${formatMetalLBCount(counters!.assigned4)} of ${formatMetalLBCount(counters!.assigned4 + counters!.available4)} (${formatMetalLBCount(counters!.available4)} available)`} />}
        {has6 && <Property label="IPv6 assigned" value={`${formatMetalLBCount(counters!.assigned6)} of ${formatMetalLBCount(counters!.assigned6 + counters!.available6)} (${formatMetalLBCount(counters!.available6)} available)`} />}
      </PropertyList>
      <p className="mt-3 text-xs text-theme-text-tertiary">Counts are addresses the controller assigned, not Services: Services can share an address. A Service records the pool it got its address from in the <code>metallb.io/ip-allocated-from-pool</code> annotation.</p>
    </Section>
    {allocation && <Section title="Service Allocation" icon={Filter} defaultExpanded>
      <PropertyList>
        <Property label="Priority" value={typeof allocation.priority === 'number' ? `${allocation.priority} (lower is preferred)` : 'Not set (lowest)'} />
        <Property label="Namespaces" value={stringList(allocation.namespaces).join(', ') || undefined} />
        {selectorList(allocation.namespaceSelectors).length > 0 && <Property label="Namespace selectors" value={<Selectors selectors={selectorList(allocation.namespaceSelectors)} emptyText="Any" />} />}
        {selectorList(allocation.serviceSelectors).length > 0 && <Property label="Service selectors" value={<Selectors selectors={selectorList(allocation.serviceSelectors)} emptyText="Any" />} />}
      </PropertyList>
      <p className="mt-3 text-xs text-theme-text-tertiary">Services that match are given addresses from this pool before pools with a lower priority or no allocation rules.</p>
    </Section>}
  </>
}

export function MetalLBL2AdvertisementRenderer({ data, onNavigate }: { data: any; onNavigate?: Navigate }) {
  const spec = data.spec ?? {}
  const preferred = Array.isArray(spec.preferredNodeSelectors) ? spec.preferredNodeSelectors : []
  return <Section title="L2 Advertisement" icon={Radio} defaultExpanded>
    <PropertyList>
      <Property label="IP address pools" value={<PoolLinks data={data} onNavigate={onNavigate} />} />
      <Property label="Nodes" value={<Selectors selectors={selectorList(spec.nodeSelectors)} emptyText="All nodes" />} />
      <Property label="Interfaces" value={stringList(spec.interfaces).join(', ') || 'All'} />
      {selectorList(spec.serviceSelectors).length > 0 && <Property label="Services" value={<Selectors selectors={selectorList(spec.serviceSelectors)} emptyText="All" />} />}
      {preferred.length > 0 && <Property label="Preferred nodes" value={<div className="space-y-1">
        {preferred.map((p: any, i: number) => <div key={i} className="flex items-start gap-2">
          <span className="text-xs text-theme-text-tertiary shrink-0">weight {typeof p?.weight === 'number' ? p.weight : '?'}</span>
          <LabelSelectorDisplay selector={p?.preference} emptyText="Everything" />
        </div>)}
      </div>} />}
    </PropertyList>
    <p className="mt-3 text-xs text-theme-text-tertiary">One node at a time answers ARP or NDP for each Service address. {NO_SESSION_NOTE}</p>
  </Section>
}

export function MetalLBBGPAdvertisementRenderer({ data, onNavigate }: { data: any; onNavigate?: Navigate }) {
  const spec = data.spec ?? {}
  const namespace = data.metadata?.namespace ?? ''
  const peers = stringList(spec.peers)
  return <Section title="BGP Advertisement" icon={Share2} defaultExpanded>
    <PropertyList>
      <Property label="IP address pools" value={<PoolLinks data={data} onNavigate={onNavigate} />} />
      <Property label="Peers" value={peers.length > 0
        ? <LinkList names={peers} render={name => <ResourceLink kind="BGPPeer" group={METALLB_GROUP} name={name} namespace={namespace} onNavigate={onNavigate} />} />
        : 'All peers'} />
      <Property label="Nodes" value={<Selectors selectors={selectorList(spec.nodeSelectors)} emptyText="All nodes" />} />
      <Property label="Communities" value={stringList(spec.communities).join(', ') || undefined} />
      <Property label="Local preference" value={typeof spec.localPref === 'number' && spec.localPref > 0 ? spec.localPref : undefined} />
      <Property label="AS path prepend" value={typeof spec.asPathPrepend === 'number' && spec.asPathPrepend > 0 ? `${spec.asPathPrepend} times` : undefined} />
      <Property label="Aggregation length" value={typeof spec.aggregationLength === 'number' ? `/${spec.aggregationLength} (IPv4)` : '/32 (IPv4, default)'} />
      <Property label="Aggregation length v6" value={typeof spec.aggregationLengthV6 === 'number' ? `/${spec.aggregationLengthV6} (IPv6)` : '/128 (IPv6, default)'} />
      {selectorList(spec.serviceSelectors).length > 0 && <Property label="Services" value={<Selectors selectors={selectorList(spec.serviceSelectors)} emptyText="All" />} />}
    </PropertyList>
    <p className="mt-3 text-xs text-theme-text-tertiary">{NO_SESSION_NOTE}</p>
  </Section>
}

function duration(v: unknown): string | undefined {
  return typeof v === 'string' && v ? v : undefined
}

export function MetalLBBGPPeerRenderer({ data }: { data: any }) {
  const spec = data.spec ?? {}
  const peerASN = typeof spec.peerASN === 'number' && spec.peerASN > 0 ? String(spec.peerASN) : undefined
  const secret = typeof spec.passwordSecret?.name === 'string' && spec.passwordSecret.name ? spec.passwordSecret.name : undefined
  const inlinePassword = typeof spec.password === 'string' && spec.password !== ''
  return <>
    <Section title="BGP Peer" icon={Share2} defaultExpanded>
      <PropertyList>
        <Property label="Peer address" value={spec.peerAddress || (spec.interface ? undefined : 'Not specified')} />
        <Property label="Interface" value={spec.interface ? `${spec.interface} (unnumbered)` : undefined} />
        <Property label="Peer port" value={typeof spec.peerPort === 'number' && spec.peerPort > 0 ? spec.peerPort : '179 (default)'} />
        <Property label="Peer ASN" value={peerASN ?? (spec.dynamicASN ? `Detected from the session (${spec.dynamicASN})` : 'Not specified')} />
        <Property label="My ASN" value={typeof spec.myASN === 'number' ? spec.myASN : 'Not specified'} />
        <Property label="Local ASN" value={typeof spec.localASN === 'number' && spec.localASN > 0 ? spec.localASN : undefined} />
        <Property label="Source address" value={spec.sourceAddress || undefined} />
        <Property label="Router ID" value={spec.routerID || undefined} />
        <Property label="VRF" value={spec.vrf || undefined} />
        <Property label="BFD profile" value={spec.bfdProfile || undefined} />
        <Property label="eBGP multi-hop" value={spec.ebgpMultiHop === true ? 'Yes' : 'No'} />
        <Property label="Graceful restart" value={spec.enableGracefulRestart === true ? 'Enabled' : undefined} />
        <Property label="Hold time" value={duration(spec.holdTime)} />
        <Property label="Keepalive time" value={duration(spec.keepaliveTime)} />
        <Property label="Connect time" value={duration(spec.connectTime)} />
        <Property label="Nodes" value={<Selectors selectors={selectorList(spec.nodeSelectors)} emptyText="All nodes" />} />
      </PropertyList>
      <p className="mt-3 text-xs text-theme-text-tertiary">Configuration only. MetalLB does not report on this object whether the session is established.</p>
    </Section>
    {(secret || inlinePassword) && <Section title="Authentication" icon={ShieldCheck} defaultExpanded>
      <PropertyList>
        <Property label="Password" value={secret ? `From Secret ${secret}` : 'Set inline (not shown)'} />
      </PropertyList>
    </Section>}
  </>
}

export function MetalLBConfigurationStateRenderer({ data, onNavigate }: { data: any; onNavigate?: Navigate }) {
  const operationalIssuesShown = useOperationalIssuesShown()
  const state = getMetalLBConfigurationStateStatus(data)
  const errors = getMetalLBConfigurationErrors(data)
  const { component, node } = getMetalLBComponent(data)
  return <>
    {state.text === 'Invalid' && !operationalIssuesShown && <AlertBanner
      variant="warning"
      title="MetalLB rejected its configuration"
      message="This component keeps running the last configuration it accepted, if any, until the errors are fixed."
      items={errors.length > 0 ? errors : undefined}
    />}
    <Section title="Configuration State" icon={ShieldCheck} defaultExpanded>
      <PropertyList>
        <Property label="Result" value={<Badge severity={healthToSeverity(state.level)}>{state.text}</Badge>} />
        <Property label="Component" value={component === 'controller' ? 'Controller' : component === 'speaker' ? 'Speaker' : 'Not labelled'} />
        <Property label="Node" value={node ? <ResourceLink kind="nodes" name={node} onNavigate={onNavigate} /> : undefined} />
        {errors.length > 0 && <Property label="Errors" value={<div className="space-y-1">{errors.map((e, i) => <div key={i} className="break-words">{e}</div>)}</div>} />}
      </PropertyList>
      <p className="mt-3 text-xs text-theme-text-tertiary">Invalid means a reconciler could not parse the configuration. Other failures leave the result Valid, so Valid does not prove addresses are announced.</p>
    </Section>
  </>
}
