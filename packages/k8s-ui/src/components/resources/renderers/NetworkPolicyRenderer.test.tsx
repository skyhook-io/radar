import { describe, expect, it } from 'vitest'
import { renderToString } from 'react-dom/server'
import { NetworkPolicyRenderer } from './NetworkPolicyRenderer'
import { NetworkPolicyDiagram } from './NetworkPolicyDiagram'

const render = (spec: any) => renderToString(<NetworkPolicyRenderer data={{ kind: 'NetworkPolicy', spec }} />)
const renderDiagram = (spec: any) => renderToString(<NetworkPolicyDiagram spec={spec} />)

describe('NetworkPolicyRenderer', () => {
  it('shows a policy without policyTypes as isolating ingress, and says the type was defaulted', () => {
    const html = render({ podSelector: {} })
    expect(html).toContain('Ingress Rules')
    expect(html).toContain('Deny all ingress')
    expect(html).toContain('policyTypes not set')
    expect(html).not.toContain('Egress Rules')
  })

  it('does not call an explicitly typed policy defaulted', () => {
    const html = render({ podSelector: {}, policyTypes: ['Ingress'] })
    expect(html).toContain('Deny all ingress')
    expect(html).not.toContain('policyTypes not set')
  })

  it('renders both selectors of a peer that sets podSelector and namespaceSelector', () => {
    const html = render({
      podSelector: { matchLabels: { app: 'prometheus' } },
      policyTypes: ['Ingress'],
      ingress: [{
        from: [{
          podSelector: { matchLabels: { app: 'radar' } },
          namespaceSelector: { matchLabels: { 'kubernetes.io/metadata.name': 'radar' } },
        }],
      }],
    })
    expect(html).toContain('podSelector')
    expect(html).toContain('in namespaceSelector')
    expect(html).toContain('kubernetes.io/metadata.name')
  })

  it('renders matchExpressions rather than calling the selector empty', () => {
    const html = render({
      podSelector: { matchExpressions: [{ key: 'tier', operator: 'In', values: ['web', 'api'] }] },
      policyTypes: ['Ingress'],
      ingress: [{ from: [{ podSelector: { matchExpressions: [{ key: 'role', operator: 'Exists' }] } }] }],
    })
    expect(html).toContain('tier')
    expect(html).toContain('In')
    expect(html).toContain('role')
    expect(html).not.toContain('All pods in namespace')
  })

  it('renders endPort ranges in the rule port list', () => {
    const html = render({
      podSelector: {},
      policyTypes: ['Ingress'],
      ingress: [{ ports: [{ port: 9000, endPort: 9100 }, { protocol: 'UDP', port: 53 }] }],
    })
    expect(html).toContain('TCP/9000-9100')
    expect(html).toContain('UDP/53')
  })

  it('diagram: reads matchExpressions, shows ranges, and treats an empty selector as all pods', () => {
    const html = renderDiagram({
      podSelector: { matchExpressions: [{ key: 'tier', operator: 'In', values: ['web'] }] },
      ingress: [{ ports: [{ port: 9000, endPort: 9100 }] }],
    })
    expect(html).toContain('tier In (web)')
    expect(html).toContain('TCP/9000-9100')
    expect(html).not.toContain('All pods')

    const empty = renderDiagram({ podSelector: {} })
    expect(empty).toContain('All pods')
    expect(empty).not.toContain('all workloads')
  })
})
