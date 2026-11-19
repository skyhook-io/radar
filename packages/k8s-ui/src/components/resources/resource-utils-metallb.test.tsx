import { describe, it, expect } from 'vitest'
import { renderToStaticMarkup } from 'react-dom/server'
import { getGenericResourceStatus } from './generic-status'
import { getCellFilterValue } from './resource-utils'
import {
  getMetalLBPoolStatus,
  getMetalLBColumnValue,
  getMetalLBSortValue,
  getMetalLBConfigurationStateStatus,
  formatMetalLBCount,
} from './resource-utils-metallb'
import {
  MetalLBIPAddressPoolRenderer,
  MetalLBL2AdvertisementRenderer,
  MetalLBBGPAdvertisementRenderer,
  MetalLBBGPPeerRenderer,
  MetalLBConfigurationStateRenderer,
} from './renderers/MetalLBRenderers'

// Shaped like what MetalLB v0.16.2 wrote on a kind cluster.
const pool = (status?: any, spec: any = {}): any => ({
  apiVersion: 'metallb.io/v1beta1', kind: 'IPAddressPool',
  metadata: { name: 'public', namespace: 'metallb-system' },
  spec: { addresses: ['172.18.255.200-172.18.255.209'], autoAssign: true, avoidBuggyIPs: false, ...spec },
  ...(status ? { status } : {}),
})
const counters = (assignedIPv4: number, availableIPv4: number, assignedIPv6 = 0, availableIPv6 = 0) => ({ assignedIPv4, availableIPv4, assignedIPv6, availableIPv6 })

const l2 = (): any => ({
  apiVersion: 'metallb.io/v1beta1', kind: 'L2Advertisement',
  metadata: { name: 'public-l2', namespace: 'metallb-system' },
  spec: { ipAddressPools: ['public'], interfaces: ['eth0'], nodeSelectors: [{ matchLabels: { 'kubernetes.io/hostname': 'worker' } }] },
})

const bgpAdvertisement = (): any => ({
  apiVersion: 'metallb.io/v1beta1', kind: 'BGPAdvertisement',
  metadata: { name: 'public-bgp', namespace: 'metallb-system' },
  spec: { ipAddressPoolSelectors: [{ matchLabels: { exposure: 'public' } }], peers: ['router'], communities: ['65535:65282'], localPref: 100, aggregationLength: 32 },
})

const peer = (spec: any = {}): any => ({
  apiVersion: 'metallb.io/v1beta2', kind: 'BGPPeer',
  metadata: { name: 'router', namespace: 'metallb-system' },
  spec: { myASN: 64500, peerASN: 64501, peerAddress: '172.18.0.100', holdTime: '1m30s', ...spec },
})

const configurationState = (result: string | undefined, errorSummary?: string, labels: Record<string, string> = { 'metallb.io/component-type': 'speaker', 'metallb.io/node-name': 'worker' }): any => ({
  apiVersion: 'metallb.io/v1beta1', kind: 'ConfigurationState',
  metadata: { name: 'speaker-worker', namespace: 'metallb-system', labels },
  status: result ? { result, ...(errorSummary ? { errorSummary } : {}) } : undefined,
})

describe('MetalLB IPAddressPool status', () => {
  it.each([
    ['no counters reported', undefined, 'Not reported', 'unknown'],
    ['addresses free', counters(2, 8), 'Available', 'healthy'],
    ['all assigned', counters(10, 0), 'Exhausted', 'degraded'],
    ['not loaded by the controller', counters(0, 0), 'No addresses', 'degraded'],
    ['dual stack, IPv4 out', counters(4, 0, 1, 15), 'IPv4 exhausted', 'degraded'],
    ['dual stack, IPv6 out', counters(1, 3, 2, 0), 'IPv6 exhausted', 'degraded'],
    ['dual stack, both out', counters(4, 0, 2, 0), 'Exhausted', 'degraded'],
  ])('%s', (_, status, text, level) => {
    const badge = getMetalLBPoolStatus(pool(status))
    expect(badge.text).toBe(text)
    expect(badge.level).toBe(level)
    expect(getGenericResourceStatus(pool(status))?.text).toBe(text)
  })

  it('reads a terminating pool as terminating', () => {
    expect(getMetalLBPoolStatus({ ...pool(counters(10, 0)), metadata: { name: 'p', deletionTimestamp: '2026-11-20T10:00:00Z' } }).text).toBe('Terminating')
  })

  it('reads an IPv6 range with 62 or more host bits as unlimited', () => {
    expect(formatMetalLBCount(9223372036854775807)).toBe('Unlimited')
    expect(getMetalLBColumnValue(pool(counters(0, 0, 3, 9223372036854775804), { addresses: ['fc00::/64'] }), 'poolAssigned')).toBe('IPv6 3 / Unlimited')
  })
})

describe('MetalLB columns', () => {
  it('describes pools', () => {
    const p = pool(counters(2, 8, 1, 15), { addresses: ['172.18.255.200-172.18.255.209', 'fc00:f853:ccd:e799::/124'], autoAssign: false })
    expect(getMetalLBColumnValue(p, 'poolAddresses')).toBe('172.18.255.200-172.18.255.209, fc00:f853:ccd:e799::/124')
    expect(getMetalLBColumnValue(p, 'poolAssigned')).toBe('IPv4 2 / 10 · IPv6 1 / 16')
    expect(getMetalLBColumnValue(p, 'poolAutoAssign')).toBe('No')
    expect(getMetalLBColumnValue(pool(), 'poolAutoAssign')).toBe('Yes')
    expect(getMetalLBColumnValue(pool(), 'poolAssigned')).toBe('')
    expect(getCellFilterValue(p, 'poolAssigned', 'ipaddresspools')).toBe('IPv4 2 / 10 · IPv6 1 / 16')
  })

  it('sorts pools by the share of addresses in use, unreported first', () => {
    expect(getMetalLBSortValue(pool(counters(5, 5)), 'poolAssigned')).toBe(0.5)
    expect(getMetalLBSortValue(pool(), 'poolAssigned')).toBe(-1)
    expect(getMetalLBSortValue(pool(counters(0, 0)), 'poolAssigned')).toBe(-1)
  })

  it('describes advertisements, with no pools meaning every pool', () => {
    expect(getMetalLBColumnValue(l2(), 'advertisedPools')).toBe('public')
    expect(getMetalLBColumnValue(l2(), 'advertisedNodes')).toBe('kubernetes.io/hostname=worker')
    expect(getMetalLBColumnValue(l2(), 'l2Interfaces')).toBe('eth0')
    const everything = { ...l2(), spec: {} }
    expect(getMetalLBColumnValue(everything, 'advertisedPools')).toBe('All pools')
    expect(getMetalLBColumnValue(everything, 'advertisedNodes')).toBe('All nodes')
    expect(getMetalLBColumnValue(everything, 'l2Interfaces')).toBe('All')
    expect(getMetalLBColumnValue(bgpAdvertisement(), 'advertisedPools')).toBe('pools matching exposure=public')
    expect(getMetalLBColumnValue(bgpAdvertisement(), 'bgpPeers')).toBe('router')
    expect(getMetalLBColumnValue({ ...bgpAdvertisement(), spec: {} }, 'bgpPeers')).toBe('All peers')
    expect(getMetalLBColumnValue(bgpAdvertisement(), 'bgpCommunities')).toBe('65535:65282')
  })

  it('describes peers, sorting ASNs numerically', () => {
    expect(getMetalLBColumnValue(peer(), 'peerAddress')).toBe('172.18.0.100')
    expect(getMetalLBColumnValue(peer({ peerAddress: undefined, interface: 'eth1' }), 'peerAddress')).toBe('Interface eth1')
    expect(getMetalLBColumnValue(peer({ peerASN: undefined, dynamicASN: 'external' }), 'peerASN')).toBe('Detected (external)')
    expect(getMetalLBColumnValue(peer(), 'ebgpMultiHop')).toBe('No')
    expect(getMetalLBSortValue(peer(), 'peerASN')).toBe(64501)
    expect(getMetalLBSortValue(peer({ peerASN: undefined }), 'peerASN')).toBe(-1)
  })

  it('describes configuration states', () => {
    const invalid = configurationState('Invalid', 'configuration error: parsing peer router-secret secret ref not found for peer config "metallb-system"/"router-secret"\nfailed to parse peer router-secret password secret')
    expect(getMetalLBColumnValue(invalid, 'configComponent')).toBe('Speaker on worker')
    expect(getMetalLBColumnValue(invalid, 'configErrors')).toBe('configuration error: parsing peer router-secret secret ref not found for peer config "metallb-system"/"router-secret"; failed to parse peer router-secret password secret')
    expect(getMetalLBColumnValue(configurationState('Valid', undefined, { 'metallb.io/component-type': 'controller' }), 'configComponent')).toBe('Controller')
  })

  it('leaves other groups to the generic columns', () => {
    for (const data of [{ ...pool(counters(1, 1)), apiVersion: 'fake.metallb.io/v1beta1' }, { ...peer(), apiVersion: 'crd.projectcalico.org/v1' }]) {
      for (const column of ['status', 'poolAssigned', 'peerAddress', 'peerASN']) {
        expect(getMetalLBColumnValue(data, column)).toBeUndefined()
      }
    }
  })
})

describe('MetalLB ConfigurationState status', () => {
  it.each([
    ['Valid', 'Valid', 'healthy'],
    ['Invalid', 'Invalid', 'degraded'],
    ['Unknown', 'Unknown', 'unknown'],
    [undefined, 'Not reported', 'unknown'],
  ])('%s', (result, text, level) => {
    const badge = getMetalLBConfigurationStateStatus(configurationState(result))
    expect(badge.text).toBe(text)
    expect(badge.level).toBe(level)
  })

  it('carries the errors as the generic status reason', () => {
    expect(getGenericResourceStatus(configurationState('Invalid', 'bad peer'))).toMatchObject({ text: 'Invalid', reason: 'bad peer' })
    expect(getGenericResourceStatus(configurationState('Valid'))?.reason).toBeUndefined()
  })
})

describe('MetalLB renderers', () => {
  it('explains an exhausted pool and its counters', () => {
    const html = renderToStaticMarkup(<MetalLBIPAddressPoolRenderer data={pool(counters(10, 0), { serviceAllocation: { priority: 10, namespaces: ['web'] } })} />)
    expect(html).toContain('Every address in this pool is assigned')
    expect(html).toContain('10 of 10 (0 available)')
    expect(html).toContain('Service Allocation')
    expect(html).toContain('10 (lower is preferred)')
    expect(html).not.toContain('IPv6 assigned')
  })

  it('says why a pool has no counters, and when it has no addresses', () => {
    expect(renderToStaticMarkup(<MetalLBIPAddressPoolRenderer data={pool()} />)).toContain('No address counters reported')
    expect(renderToStaticMarkup(<MetalLBIPAddressPoolRenderer data={pool(counters(0, 0))} />)).toContain('MetalLB reports no addresses in this pool')
  })

  it('links advertisements to their pools and peers', () => {
    const navigated: any[] = []
    const l2Html = renderToStaticMarkup(<MetalLBL2AdvertisementRenderer data={l2()} onNavigate={ref => navigated.push(ref)} />)
    expect(l2Html).toContain('public')
    expect(l2Html).toContain('eth0')
    expect(l2Html).toContain('Configuration only')
    const bgpHtml = renderToStaticMarkup(<MetalLBBGPAdvertisementRenderer data={bgpAdvertisement()} onNavigate={ref => navigated.push(ref)} />)
    expect(bgpHtml).toContain('Pools matching')
    expect(bgpHtml).toContain('router')
    expect(bgpHtml).toContain('65535:65282')
    expect(renderToStaticMarkup(<MetalLBL2AdvertisementRenderer data={{ ...l2(), spec: {} }} />)).toContain('All pools')
  })

  it('never shows an inline BGP password', () => {
    const inline = renderToStaticMarkup(<MetalLBBGPPeerRenderer data={peer({ password: 's3cret-md5' })} />)
    expect(inline).toContain('Set inline (not shown)')
    expect(inline).not.toContain('s3cret-md5')
    expect(renderToStaticMarkup(<MetalLBBGPPeerRenderer data={peer({ passwordSecret: { name: 'bgp-auth', namespace: 'metallb-system' } })} />)).toContain('From Secret bgp-auth')
    expect(renderToStaticMarkup(<MetalLBBGPPeerRenderer data={peer()} />)).not.toContain('Authentication')
  })

  it('lists a rejected configuration\'s errors', () => {
    const html = renderToStaticMarkup(<MetalLBConfigurationStateRenderer data={configurationState('Invalid', 'failed to parse peer router: secret ref not found')} />)
    expect(html).toContain('MetalLB rejected its configuration')
    expect(html).toContain('secret ref not found')
    expect(html).toContain('worker')
    expect(renderToStaticMarkup(<MetalLBConfigurationStateRenderer data={configurationState('Valid')} />)).not.toContain('rejected its configuration')
  })
})
