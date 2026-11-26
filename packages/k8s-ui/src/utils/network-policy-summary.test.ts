import { describe, expect, it } from 'vitest'
import { collapseCidrRuns, summarizeNetworkPolicy } from './network-policy-summary'
import { largePolicySpec, nameIn } from './network-policy-fixtures'

const onlyGroup = (spec: any, ns = 'ns1', dir: 'ingress' | 'egress' = 'ingress') => {
  const rules = summarizeNetworkPolicy(spec, ns)[dir].rules
  expect(rules).toHaveLength(1)
  return rules[0].groups
}

describe('summarizeNetworkPolicy', () => {
  it('names the target in words', () => {
    expect(summarizeNetworkPolicy({ podSelector: {} }, 'payments').target).toBe('All pods in payments')
    expect(summarizeNetworkPolicy({ podSelector: {} }).target).toBe('All pods in this namespace')
    expect(
      summarizeNetworkPolicy({ podSelector: { matchExpressions: [{ key: 'tier', operator: 'In', values: ['web'] }] } }).target,
    ).toBe('tier In (web)')
  })

  it('says when policyTypes were defaulted', () => {
    expect(summarizeNetworkPolicy({ podSelector: {} }).defaultedTypes).toBe(true)
    expect(summarizeNetworkPolicy({ podSelector: {}, policyTypes: ['Ingress'] }).defaultedTypes).toBe(false)
  })

  it('reads a direction with no rules as deny-all', () => {
    const s = summarizeNetworkPolicy({ podSelector: {} })
    expect(s.ingress).toEqual({ restricted: true, denyAll: true, rules: [] })
    expect(s.egress.restricted).toBe(false)

    const e = summarizeNetworkPolicy({ podSelector: {}, policyTypes: ['Egress'] })
    expect(e.egress.denyAll).toBe(true)
    expect(e.ingress.restricted).toBe(false)
  })

  it('reads an empty or missing peer list as any source / destination', () => {
    expect(onlyGroup({ ingress: [{ from: [] }] })).toEqual([
      expect.objectContaining({ kind: 'any', title: 'Any source' }),
    ])
    expect(onlyGroup({ ingress: [{}] })[0]).toMatchObject({ kind: 'any', title: 'Any source' })
    expect(onlyGroup({ policyTypes: ['Egress'], egress: [{ ports: [{ port: 53 }] }] }, 'ns1', 'egress')[0])
      .toMatchObject({ kind: 'any', title: 'Any destination' })
  })

  it('groups plain pod selectors into one group', () => {
    expect(onlyGroup({ ingress: [{ from: [{ podSelector: {} }] }] })[0]).toMatchObject({ kind: 'pods', title: 'Any pod in ns1' })

    const two = onlyGroup({
      ingress: [{ from: [{ podSelector: { matchLabels: { app: 'a' } } }, { podSelector: { matchLabels: { app: 'b' } } }] }],
    })
    expect(two).toHaveLength(1)
    expect(two[0]).toMatchObject({ kind: 'pods', title: 'Pods in ns1 matching any of' })
    expect(two[0].items.map((i) => i.label)).toEqual(['app=a', 'app=b'])

    const withAll = onlyGroup({ ingress: [{ from: [{ podSelector: {} }, { podSelector: { matchLabels: { app: 'a' } } }] }] })
    expect(withAll).toHaveLength(1)
    expect(withAll[0].title).toBe('Any pod in ns1')
  })

  it('merges name-only namespace peers into one namespaces group', () => {
    const groups = onlyGroup({
      ingress: [{
        from: [
          { namespaceSelector: nameIn('a', 'b') },
          { namespaceSelector: { matchLabels: { 'kubernetes.io/metadata.name': 'c' } } },
          { podSelector: {}, namespaceSelector: { matchLabels: { 'kubernetes.io/metadata.name': 'd' } } },
        ],
      }],
    })
    expect(groups).toHaveLength(1)
    expect(groups[0]).toMatchObject({ kind: 'namespaces', title: 'Any pod in 4 namespaces', raw: ['a', 'b', 'c', 'd'] })
    expect(groups[0].items.map((i) => i.label)).toEqual(['a', 'b', 'c', 'd'])

    const single = onlyGroup({ ingress: [{ from: [{ namespaceSelector: nameIn('analytics') }] }] })[0]
    expect(single).toMatchObject({ title: 'Any pod in analytics', raw: ['analytics'], items: [] })
    expect(onlyGroup({ ingress: [{ from: [{ namespaceSelector: {} }] }] })[0].title).toBe('Any pod in any namespace')

    const other = onlyGroup({ ingress: [{ from: [{ namespaceSelector: { matchLabels: { team: 'x' } } }] }] })[0]
    expect(other).toMatchObject({ kind: 'namespaceSelector', title: 'Any pod in namespaces matching any of' })
    expect(other.items[0].label).toBe('team=x')
  })

  it('keeps a pod selector inside a namespace selector as one AND peer', () => {
    const g = onlyGroup({
      ingress: [{
        from: [{
          podSelector: { matchLabels: { app: 'radar' } },
          namespaceSelector: { matchLabels: { 'kubernetes.io/metadata.name': 'radar' } },
        }],
      }],
    })
    expect(g).toHaveLength(1)
    expect(g[0].kind).toBe('podsInNamespaces')
    expect(g[0].items[0]).toMatchObject({ label: 'app=radar', detail: 'in kubernetes.io/metadata.name=radar' })
  })

  it('groups the large ipBlock rule and collapses the /32 run', () => {
    const rules = summarizeNetworkPolicy(largePolicySpec, 'payments').ingress.rules
    const ip = rules[2].groups[0]
    expect(ip).toMatchObject({ kind: 'ipBlocks', title: '261 IP ranges' })
    expect(ip.raw).toHaveLength(261)
    expect(ip.items).toHaveLength(6)
    const [run, ...networks] = ip.items
    expect(run.label).toBe('10.42.0–255.0/32')
    expect(networks.map((i) => i.label)).toEqual([
      '192.168.10.0/24', '192.168.12.0/24', '192.168.20.0/24', '192.168.30.0/24', '192.168.40.0/24',
    ])
    expect(run).toMatchObject({ detail: '× 256' })
    expect(run.members).toHaveLength(256)
  })

  it('counts one IP range in the singular and never collapses an except block', () => {
    expect(onlyGroup({ ingress: [{ from: [{ ipBlock: { cidr: '10.0.0.0/8' } }] }] })[0].title).toBe('1 IP range')

    const g = onlyGroup({
      ingress: [{
        from: [
          { ipBlock: { cidr: '10.0.0.0/24' } },
          { ipBlock: { cidr: '10.0.1.0/24', except: ['10.0.1.0/28'] } },
          { ipBlock: { cidr: '10.0.2.0/24' } },
        ],
      }],
    })[0]
    expect(g.items.map((i) => i.label)).toEqual(['10.0.0.0/24', '10.0.2.0/24', '10.0.1.0/24'])
    expect(g.items[2].detail).toBe('except 10.0.1.0/28')
    expect(g.raw).toContain('10.0.1.0/24 except 10.0.1.0/28')
  })

  it('counts and copies a duplicated CIDR once', () => {
    const g = onlyGroup({ ingress: [{ from: [{ ipBlock: { cidr: '10.0.0.0/24' } }, { ipBlock: { cidr: '10.0.0.0/24' } }, { ipBlock: { cidr: '10.0.1.0/24' } }] }] })[0]
    expect(g.title).toBe('2 IP ranges')
    expect(g.raw).toEqual(['10.0.0.0/24', '10.0.1.0/24'])
  })

  it('still shows a source when name requirements select no namespace', () => {
    const g = onlyGroup({
      ingress: [{ from: [{ namespaceSelector: { matchLabels: { 'kubernetes.io/metadata.name': 'a' }, matchExpressions: [{ key: 'kubernetes.io/metadata.name', operator: 'In', values: ['b'] }] } }] }],
    })
    expect(g).toHaveLength(1)
    expect(g[0].kind).toBe('namespaceSelector')
    expect(g[0].items[0].label).toContain('kubernetes.io/metadata.name=a')
  })

  it('orders groups inside a rule', () => {
    const g = onlyGroup({
      ingress: [{
        from: [
          { ipBlock: { cidr: '10.0.0.0/8' } },
          { podSelector: { matchLabels: { a: 'b' } }, namespaceSelector: { matchLabels: { t: 'x' } } },
          { namespaceSelector: { matchLabels: { t: 'x' } } },
          { namespaceSelector: nameIn('n') },
          { podSelector: {} },
        ],
      }],
    })
    expect(g.map((x) => x.kind)).toEqual(['pods', 'namespaces', 'namespaceSelector', 'podsInNamespaces', 'ipBlocks'])
  })

  it('formats ports and numbers rules from 1', () => {
    const rules = summarizeNetworkPolicy(largePolicySpec, 'payments').ingress.rules
    expect(rules.map((r) => r.index)).toEqual([1, 2, 3, 4])
    expect(rules[0].ports).toEqual([])
    expect(rules[3].ports).toEqual(['TCP/5432'])
  })

  it('survives malformed input', () => {
    expect(onlyGroup({ ingress: [{ from: null }] })[0].title).toBe('Any source')

    const empty = onlyGroup({ ingress: [{ from: [{}] }] })[0]
    expect(empty).toMatchObject({ kind: 'pods', title: 'Pods in ns1 matching any of' })
    expect(empty.items[0].label).toBe('?')

    expect(onlyGroup({ ingress: [{ from: [{ ipBlock: {} }] }] })[0].items[0].label).toBe('?')
    expect(onlyGroup({ ingress: [{ from: [{ podSelector: { matchLabels: { port: 80 } } }] }] })[0].items[0].label).toBe('port=80')
  })
})

describe('collapseCidrRuns', () => {
  it('collapses three or more consecutive addresses regardless of input order', () => {
    expect(collapseCidrRuns(['10.0.0.3/32', '10.0.0.1/32', '10.0.0.2/32'])).toEqual([
      { label: '10.0.0.1–3/32', detail: '× 3', members: ['10.0.0.1/32', '10.0.0.2/32', '10.0.0.3/32'] },
    ])
  })

  it('leaves two consecutive addresses alone', () => {
    expect(collapseCidrRuns(['10.0.0.1/32', '10.0.0.2/32']).map((i) => i.label)).toEqual(['10.0.0.1/32', '10.0.0.2/32'])
  })

  it('splits a run at a gap', () => {
    const labels = collapseCidrRuns(['10.0.0.1/32', '10.0.0.2/32', '10.0.0.4/32', '10.0.0.5/32', '10.0.0.6/32']).map((i) => i.label)
    expect(labels).toEqual(['10.0.0.1/32', '10.0.0.2/32', '10.0.0.4–6/32'])
  })

  it('does not run across prefix lengths', () => {
    expect(collapseCidrRuns(['10.0.0.0/24', '10.0.1.0/32', '10.0.2.0/24'])).toHaveLength(3)
  })

  it('keeps IPv6 and unparsable values after IPv4, in input order', () => {
    expect(collapseCidrRuns(['fd00::/64', '10.0.0.0/8', 'bogus']).map((i) => i.label)).toEqual(['10.0.0.0/8', 'fd00::/64', 'bogus'])
  })

  it('counts an exact duplicate once and never merges overlapping ranges', () => {
    expect(collapseCidrRuns(['10.0.0.1/32', '10.0.0.1/32'])).toHaveLength(1)
    expect(collapseCidrRuns(['10.1.0.0/16', '10.0.0.0/8']).map((i) => i.label)).toEqual(['10.0.0.0/8', '10.1.0.0/16'])
  })
})
