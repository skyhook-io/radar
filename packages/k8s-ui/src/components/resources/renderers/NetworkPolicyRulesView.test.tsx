import { describe, expect, it } from 'vitest'
import { renderToString } from 'react-dom/server'
import { NetworkPolicyAppliesTo, NetworkPolicyRulesView } from './NetworkPolicyRulesView'
import { summarizeNetworkPolicy } from '../../../utils/network-policy-summary'
import { largePolicySpec } from '../../../utils/network-policy-fixtures'

const rules = (spec: any, staged = false) =>
  renderToString(<NetworkPolicyRulesView summary={summarizeNetworkPolicy(spec, 'payments')} staged={staged} />)
const appliesTo = (spec: any) => renderToString(<NetworkPolicyAppliesTo summary={summarizeNetworkPolicy(spec, 'payments')} />)

describe('NetworkPolicyAppliesTo', () => {
  it('names the selected pods and what each direction does', () => {
    const html = appliesTo(largePolicySpec)
    expect(html).toContain('All pods in payments')
    expect(html).toContain('Ingress restricted')
    expect(html).toContain('Egress not restricted')
    expect(html).not.toContain('policyTypes not set')
  })

  it('lets a long target shrink and wrap instead of pushing the view toggle out', () => {
    const html = appliesTo({ podSelector: { matchExpressions: [{ key: 'app', operator: 'In', values: ['a'.repeat(80)] }] } })
    expect(html).toMatch(/data-applies-to="true" class="[^"]*\bmin-w-0\b[^"]*\bflex-1\b/)
    expect(html).toContain('[overflow-wrap:anywhere]')
  })

  it('says when policyTypes were defaulted', () => {
    expect(appliesTo({ podSelector: {} })).toContain('policyTypes not set')
  })
})

describe('NetworkPolicyRulesView', () => {
  it('renders one card per rule with ports and groups', () => {
    const html = rules(largePolicySpec)
    for (const n of [1, 2, 3, 4]) expect(html).toContain(`Rule ${n}`)
    expect(html).toContain('Allowed inbound')
    expect(html).toContain('4 rules, any one matches')
    expect(html.match(/any port/g)).toHaveLength(3)
    expect(html).toContain('TCP/5432')
    expect(html).toContain('Any pod in 14 namespaces')
    expect(html).toContain('261 IP ranges')
    expect(html).toContain('10.42.0–255.0/32')
    expect(html).toContain('Everything else inbound is denied')
  })

  it('counts a single rule in the singular', () => {
    expect(rules({ ingress: [{ from: [{ podSelector: {} }] }] })).toContain('1 rule<')
  })

  it('reads a restricted direction with no rules as deny-all', () => {
    const html = rules({ podSelector: {} })
    expect(html).toContain('All inbound denied')
    expect(html).not.toContain('Rule 1')
  })

  it('renders egress-only policies on the outbound side', () => {
    const html = rules({
      podSelector: {},
      policyTypes: ['Egress'],
      egress: [{ to: [{ ipBlock: { cidr: '0.0.0.0/0' } }], ports: [{ protocol: 'UDP', port: 53 }] }],
    })
    expect(html).toContain('Allowed outbound')
    expect(html).toContain('UDP/53')
    expect(html).toContain('Everything else outbound is denied')
    expect(html).not.toContain('Allowed inbound')
  })

  it('paints panels with theme backgrounds', () => {
    const html = rules({ podSelector: {} }) + appliesTo(largePolicySpec) + rules(largePolicySpec)
    expect(html).not.toMatch(/bg-(red|indigo)-500\/8/)
    expect(html).not.toContain('bg-indigo-500')
    expect(html).toContain('bg-theme-elevated')
  })

  it('marks a staged policy', () => {
    expect(rules(largePolicySpec, true)).toContain('Staged preview')
  })
})
