import { describe, expect, it } from 'vitest'
import {
  MULTUS_NETWORKS_ANNOTATION,
  MULTUS_NETWORK_STATUS_ANNOTATION,
  NETWORK_ATTACHMENT_DEFINITION_GROUP,
  NETWORK_ATTACHMENT_DEFINITION_PLURAL,
  getPodNetworks,
  hasSecondaryNetworks,
  networkAttachmentKey,
  parseNetworkStatus,
  parseRequestedNetworks,
  uniqueNetworkAttachmentRefs,
} from './multus'
import { kindToPluralWithGroup, knownKindForPluralWithGroup } from './navigation'

function pod(annotations: Record<string, string>, namespace = 'apps') {
  return { metadata: { namespace, annotations } }
}

const status = (entries: unknown[]) => JSON.stringify(entries)

describe('parseRequestedNetworks', () => {
  it('treats a missing, blank or empty list as absent', () => {
    expect(parseRequestedNetworks(undefined)).toEqual({ state: 'absent' })
    expect(parseRequestedNetworks('  ')).toEqual({ state: 'absent' })
    expect(parseRequestedNetworks('[]')).toEqual({ state: 'absent' })
    expect(parseRequestedNetworks(' , ')).toEqual({ state: 'absent' })
  })

  it('reads the comma-separated forms, with or without spaces', () => {
    expect(parseRequestedNetworks('macvlan-conf-1, testns1/macvlan-conf-3 ,macvlan-conf-2@net7,testns1/br@eth9')).toEqual({
      state: 'ok',
      items: [
        { name: 'macvlan-conf-1' },
        { namespace: 'testns1', name: 'macvlan-conf-3' },
        { name: 'macvlan-conf-2', interfaceName: 'net7' },
        { namespace: 'testns1', name: 'br', interfaceName: 'eth9' },
      ],
    })
  })

  it('keeps a definition that is attached more than once', () => {
    const result = parseRequestedNetworks('macvlan-conf,macvlan-conf')
    expect(result).toEqual({ state: 'ok', items: [{ name: 'macvlan-conf' }, { name: 'macvlan-conf' }] })
  })

  it('reads the JSON form with static addressing and a default route', () => {
    const result = parseRequestedNetworks(JSON.stringify([
      { name: 'l2-network', namespace: 'ns1', interface: 'myiface1', ips: ['192.0.2.20/24'], mac: '02:03:04:05:06:07' },
      { name: 'net2', 'default-route': ['192.0.2.1'], cniArgs: { ignored: true } },
    ]))
    expect(result).toEqual({
      state: 'ok',
      items: [
        { name: 'l2-network', namespace: 'ns1', interfaceName: 'myiface1', ips: ['192.0.2.20/24'], mac: '02:03:04:05:06:07' },
        { name: 'net2', defaultRoute: ['192.0.2.1'] },
      ],
    })
  })

  it.each([
    ['a/b/c', /not name/],
    ['/b', /not name/],
    ['a/', /not name/],
    ['name@', /empty interface/],
    ['{"name":"x"}', /comma-separated list or a JSON list/],
    ['[{"name":"x"}', /not valid JSON/],
    ['[{"namespace":"x"}]', /entry 1 has no name/],
    ['["x"]', /entry 1 has no name/],
  ])('reports %s as unreadable instead of dropping entries', (value, reason) => {
    const result = parseRequestedNetworks(value)
    expect(result.state).toBe('invalid')
    expect(result.state === 'invalid' && result.reason).toMatch(reason)
  })

  it('does not echo an unbounded token into its reason', () => {
    const result = parseRequestedNetworks(`${'x'.repeat(500)}@`)
    expect(result.state === 'invalid' && result.reason.length).toBeLessThan(120)
  })
})

describe('parseNetworkStatus', () => {
  it('reads the Multus quickstart example', () => {
    const result = parseNetworkStatus(status([
      { name: 'cbr0', ips: ['10.244.1.73'], default: true, dns: {} },
      { name: 'macvlan-conf', interface: 'net1', ips: ['192.168.1.205'], mac: '86:1d:96:ff:55:0d', dns: {} },
    ]))
    expect(result).toEqual({
      state: 'ok',
      items: [
        { name: 'cbr0', ips: ['10.244.1.73'], isDefault: true },
        { name: 'macvlan-conf', interfaceName: 'net1', ips: ['192.168.1.205'], mac: '86:1d:96:ff:55:0d', isDefault: false },
      ],
    })
  })

  it('does not invent addresses or interfaces an entry omits', () => {
    const result = parseNetworkStatus(status([{ name: 'test-namespace/sriov-network', mac: '6e:a7:5e:3f:49:1b', 'device-info': { type: 'pci' } }]))
    expect(result).toEqual({
      state: 'ok',
      items: [{ name: 'test-namespace/sriov-network', ips: [], mac: '6e:a7:5e:3f:49:1b', isDefault: false }],
    })
  })

  it('is absent when nothing is reported and invalid when the value is not a list of named entries', () => {
    expect(parseNetworkStatus(undefined)).toEqual({ state: 'absent' })
    expect(parseNetworkStatus('[]')).toEqual({ state: 'absent' })
    expect(parseNetworkStatus('nope').state).toBe('invalid')
    expect(parseNetworkStatus('{}').state).toBe('invalid')
    expect(parseNetworkStatus('[{"interface":"net1"}]').state).toBe('invalid')
  })

  it('does not accept a blank name as an attachment', () => {
    const result = parseNetworkStatus('[{"name":"   "}]')
    expect(result.state === 'invalid' && result.reason).toMatch(/entry 1 has no name/)
    expect(parseNetworkStatus('[{"name":"  cbr0  ","default":true}]')).toEqual({
      state: 'ok',
      items: [{ name: 'cbr0', ips: [], isDefault: true }],
    })
  })
})

describe('getPodNetworks', () => {
  it('pairs repeated attachments of one definition in annotation order', () => {
    const { rows } = getPodNetworks(pod({
      [MULTUS_NETWORKS_ANNOTATION]: 'macvlan-conf,macvlan-conf',
      [MULTUS_NETWORK_STATUS_ANNOTATION]: status([
        { name: 'cbr0', interface: 'eth0', ips: ['10.244.1.73'], default: true },
        { name: 'apps/macvlan-conf', interface: 'net1', ips: ['192.168.1.205'] },
        { name: 'apps/macvlan-conf', interface: 'net2', ips: ['192.168.1.206'] },
      ]),
    }))
    expect(rows.map(row => [row.key, row.reported?.interfaceName])).toEqual([
      ['default-0', 'eth0'],
      ['requested-0', 'net1'],
      ['requested-1', 'net2'],
    ])
  })

  it('reads the status Multus v4 wrote on a real Pod with three attachments', () => {
    const { rows } = getPodNetworks(pod({
      [MULTUS_NETWORKS_ANNOTATION]: 'ptp-conf,ptp-conf-b@second,shared/ptp-dual@dual0',
      [MULTUS_NETWORK_STATUS_ANNOTATION]: String.raw`[{
    "name": "kindnet",
    "interface": "eth0",
    "ips": [
        "10.244.0.35"
    ],
    "mac": "7e:df:9a:de:3f:4a",
    "default": true,
    "dns": {},
    "gateway": [
        "\u003cnil\u003e"
    ]
},{
    "name": "default/ptp-conf",
    "interface": "net1",
    "ips": [
        "10.56.217.50"
    ],
    "mac": "aa:a9:b4:04:2e:9e",
    "dns": {}
},{
    "name": "default/ptp-conf-b",
    "interface": "second",
    "ips": [
        "10.58.0.2"
    ],
    "mac": "6a:3c:9b:c5:d8:2a",
    "dns": {}
},{
    "name": "shared/ptp-dual",
    "interface": "dual0",
    "ips": [
        "10.57.0.2",
        "fd00:57::2"
    ],
    "mac": "92:a1:d4:8a:02:50",
    "dns": {}
}]`,
    }, 'default'))
    expect(rows.map(row => [row.key, row.isDefaultNetwork, row.reported?.interfaceName, row.reported?.ips])).toEqual([
      ['default-0', true, 'eth0', ['10.244.0.35']],
      ['requested-0', false, 'net1', ['10.56.217.50']],
      ['requested-1', false, 'second', ['10.58.0.2']],
      ['requested-2', false, 'dual0', ['10.57.0.2', 'fd00:57::2']],
    ])
  })

  it('prefers an explicit interface name over position', () => {
    const { rows } = getPodNetworks(pod({
      [MULTUS_NETWORKS_ANNOTATION]: 'conf@second,conf@first',
      [MULTUS_NETWORK_STATUS_ANNOTATION]: status([
        { name: 'conf', interface: 'first', ips: ['10.0.0.1'] },
        { name: 'conf', interface: 'second', ips: ['10.0.0.2'] },
      ]),
    }))
    expect(rows.map(row => row.reported?.ips)).toEqual([['10.0.0.2'], ['10.0.0.1']])
  })

  it('does not pair a request with a report on a different, explicitly named interface', () => {
    const { rows } = getPodNetworks(pod({
      [MULTUS_NETWORKS_ANNOTATION]: 'conf@net1',
      [MULTUS_NETWORK_STATUS_ANNOTATION]: status([{ name: 'apps/conf', interface: 'net2', ips: ['10.0.0.2'] }]),
    }))
    expect(rows.map(row => [row.key, row.requested?.interfaceName, row.reported?.interfaceName])).toEqual([
      ['requested-0', 'net1', undefined],
      ['reported-0', undefined, 'net2'],
    ])
  })

  it('still pairs a named request with a report that names no interface', () => {
    const { rows } = getPodNetworks(pod({
      [MULTUS_NETWORKS_ANNOTATION]: 'conf@net1',
      [MULTUS_NETWORK_STATUS_ANNOTATION]: status([{ name: 'apps/conf', ips: ['10.0.0.2'] }]),
    }))
    expect(rows).toHaveLength(1)
    expect(rows[0].reported?.ips).toEqual(['10.0.0.2'])
  })

  it('treats a bare name as the Pod namespace, so only its qualified form is the same definition', () => {
    const sameNamespace = getPodNetworks(pod({
      [MULTUS_NETWORKS_ANNOTATION]: 'vlan-100',
      [MULTUS_NETWORK_STATUS_ANNOTATION]: status([{ name: 'apps/vlan-100', interface: 'net1' }]),
    }))
    expect(sameNamespace.rows).toHaveLength(1)
    expect(sameNamespace.rows[0].reported?.interfaceName).toBe('net1')

    const crossNamespace = getPodNetworks(pod({
      [MULTUS_NETWORKS_ANNOTATION]: 'vlan-100',
      [MULTUS_NETWORK_STATUS_ANNOTATION]: status([{ name: 'shared/vlan-100', interface: 'net1' }]),
    }))
    expect(crossNamespace.rows.map(row => [row.key, row.requested?.name, row.reported?.interfaceName])).toEqual([
      ['requested-0', 'vlan-100', undefined],
      ['reported-0', undefined, 'net1'],
    ])
  })

  it('keeps a request Multus has not reported, and a report nothing requested', () => {
    const { rows } = getPodNetworks(pod({
      [MULTUS_NETWORKS_ANNOTATION]: 'wanted',
      [MULTUS_NETWORK_STATUS_ANNOTATION]: status([{ name: 'apps/other', interface: 'net1' }]),
    }))
    expect(rows.map(row => [row.requested?.name, row.reported?.name])).toEqual([
      ['wanted', undefined],
      [undefined, 'apps/other'],
    ])
  })

  it('shows requests alone while the status annotation is absent', () => {
    const { rows, reported } = getPodNetworks(pod({ [MULTUS_NETWORKS_ANNOTATION]: 'a,b' }))
    expect(reported).toEqual({ state: 'absent' })
    expect(rows.map(row => row.reported)).toEqual([undefined, undefined])
  })

  it('keeps every dual-stack address', () => {
    const { rows } = getPodNetworks(pod({
      [MULTUS_NETWORKS_ANNOTATION]: 'dual',
      [MULTUS_NETWORK_STATUS_ANNOTATION]: status([{ name: 'apps/dual', interface: 'net1', ips: ['192.168.10.4', '2001:db8::4'] }]),
    }))
    expect(rows[0].reported?.ips).toEqual(['192.168.10.4', '2001:db8::4'])
  })

  it('never pairs or links the default network', () => {
    const { rows } = getPodNetworks(pod({
      [MULTUS_NETWORKS_ANNOTATION]: 'ovn-kubernetes',
      [MULTUS_NETWORK_STATUS_ANNOTATION]: status([{ name: 'ovn-kubernetes', interface: 'eth0', ips: ['10.131.0.26'], default: true }]),
    }))
    const defaultRow = rows.find(row => row.isDefaultNetwork)
    expect(defaultRow?.ref).toBeUndefined()
    expect(rows.find(row => !row.isDefaultNetwork)?.reported).toBeUndefined()
  })

  it('identifies a definition only by a valid namespace and name', () => {
    const { rows } = getPodNetworks(pod({
      [MULTUS_NETWORKS_ANNOTATION]: JSON.stringify([
        { name: 'good' },
        { name: 'Bad_Name' },
        { name: 'ok', namespace: 'Not A Namespace' },
        { name: '../secrets' },
      ]),
    }))
    expect(rows.map(row => row.ref)).toEqual([{ namespace: 'apps', name: 'good' }, undefined, undefined, undefined])
  })

  it('reports each malformed annotation without hiding the other', () => {
    const networks = getPodNetworks(pod({
      [MULTUS_NETWORKS_ANNOTATION]: 'a',
      [MULTUS_NETWORK_STATUS_ANNOTATION]: 'not json',
    }))
    expect(networks.reported.state).toBe('invalid')
    expect(networks.rows.map(row => row.requested?.name)).toEqual(['a'])
  })

  it('reads a Pod without annotations or metadata', () => {
    expect(getPodNetworks(undefined).rows).toEqual([])
    expect(getPodNetworks({}).rows).toEqual([])
  })
})

describe('hasSecondaryNetworks', () => {
  it('is false for a Pod that only has its default network', () => {
    const networks = getPodNetworks(pod({
      [MULTUS_NETWORK_STATUS_ANNOTATION]: status([{ name: 'ovn-kubernetes', interface: 'eth0', ips: ['10.131.0.26'], default: true }]),
    }))
    expect(hasSecondaryNetworks(networks)).toBe(false)
  })

  it('is false for a Pod with no Multus annotations', () => {
    expect(hasSecondaryNetworks(getPodNetworks(pod({})))).toBe(false)
  })

  it('is true for a request, a reported secondary network, or an unreadable annotation', () => {
    expect(hasSecondaryNetworks(getPodNetworks(pod({ [MULTUS_NETWORKS_ANNOTATION]: 'a' })))).toBe(true)
    expect(hasSecondaryNetworks(getPodNetworks(pod({ [MULTUS_NETWORK_STATUS_ANNOTATION]: status([{ name: 'apps/a', interface: 'net1' }]) })))).toBe(true)
    expect(hasSecondaryNetworks(getPodNetworks(pod({ [MULTUS_NETWORK_STATUS_ANNOTATION]: '{' })))).toBe(true)
  })
})

describe('uniqueNetworkAttachmentRefs', () => {
  it('lists each definition once, whatever the number of attachments', () => {
    const { rows } = getPodNetworks(pod({ [MULTUS_NETWORKS_ANNOTATION]: 'a,a,other/a,b' }))
    expect(uniqueNetworkAttachmentRefs(rows).map(networkAttachmentKey)).toEqual(['apps/a', 'other/a', 'apps/b'])
  })
})

describe('NetworkAttachmentDefinition identity', () => {
  it('resolves to the hyphenated plural before discovery has loaded', () => {
    expect(kindToPluralWithGroup('NetworkAttachmentDefinition', NETWORK_ATTACHMENT_DEFINITION_GROUP))
      .toBe(NETWORK_ATTACHMENT_DEFINITION_PLURAL)
    expect(knownKindForPluralWithGroup(NETWORK_ATTACHMENT_DEFINITION_PLURAL, NETWORK_ATTACHMENT_DEFINITION_GROUP))
      .toBe('NetworkAttachmentDefinition')
  })
})
