// @vitest-environment jsdom
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { NetworkPolicyGraphView } from './NetworkPolicyGraphView'
import { summarizeNetworkPolicy } from '../../../utils/network-policy-summary'
import { largePolicySpec, nameIn } from '../../../utils/network-policy-fixtures'

vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true)

let root: Root | undefined
let element: HTMLDivElement

async function mount(spec: any, staged = false) {
  element = document.createElement('div')
  document.body.appendChild(element)
  root = createRoot(element)
  await act(async () => root!.render(<NetworkPolicyGraphView summary={summarizeNetworkPolicy(spec, 'payments')} staged={staged} />))
}

afterEach(async () => {
  await act(async () => root?.unmount())
  element?.remove()
  root = undefined
})

const nodes = (dir: 'ingress' | 'egress') => [...element.querySelectorAll(`[data-graph-node="${dir}"]`)]
const columns = () => element.querySelector('[data-graph-columns]')!

describe('NetworkPolicyGraphView', () => {
  it('draws one node per rule group, tagged with its rule', async () => {
    await mount(largePolicySpec)
    const ingress = nodes('ingress')
    expect(ingress).toHaveLength(4)
    expect(ingress.map((n) => n.querySelector('[data-rule-tag]')!.textContent)).toEqual(['Rule 1', 'Rule 2', 'Rule 3', 'Rule 4'])
    expect(ingress[2].textContent).toContain('261 IP ranges')
    for (const node of ingress) expect(node.querySelectorAll('[data-sample]').length).toBeLessThanOrEqual(2)
  })

  it('gives each group of a rule its own node', async () => {
    await mount({
      ingress: [{ from: [{ namespaceSelector: nameIn('a', 'b') }, { ipBlock: { cidr: '10.0.0.0/8' } }] }],
    })
    const ingress = nodes('ingress')
    expect(ingress).toHaveLength(2)
    expect(ingress.map((n) => n.querySelector('[data-rule-tag]')!.textContent)).toEqual(['Rule 1', 'Rule 1'])
  })

  it('names the ports of every rule on its node, any port included, also for screen readers in wide mode', async () => {
    await mount(largePolicySpec)
    const ingress = nodes('ingress')
    expect(ingress[0].textContent).toContain('any port')
    expect(ingress[3].textContent).toContain('TCP/5432')
    expect(ingress[0].querySelector('[data-node-ports]')!.classList.contains('@2xl:sr-only')).toBe(true)
  })

  it('keeps the denied line readable in light mode', async () => {
    await mount(largePolicySpec)
    const line = [...element.querySelectorAll('[data-graph-target] div')].find((d) => d.textContent === 'other inbound: denied')!
    expect(line.classList.contains('text-red-600')).toBe(true)
  })

  it('labels edges with the rule ports', async () => {
    await mount(largePolicySpec)
    const labels = [...element.querySelectorAll('[data-edge-label]')].map((l) => l.textContent)
    expect(labels).toEqual(['TCP/5432'])
  })

  it('labels the ports of a rule once, even when the rule has several groups', async () => {
    await mount({
      ingress: [{ from: [{ namespaceSelector: nameIn('a') }, { ipBlock: { cidr: '10.0.0.0/8' } }], ports: [{ port: 8080 }, { port: 9000 }] }],
    })
    const labels = [...element.querySelectorAll('[data-edge-label]')].map((l) => l.textContent)
    expect(labels).toEqual(['2 ports'])
  })

  it('keeps a long port range inside the edge column', async () => {
    await mount({ ingress: [{ from: [{ podSelector: {} }], ports: [{ port: 30000, endPort: 32767 }] }] })
    const label = element.querySelector('[data-edge-label]')!
    expect(label.textContent).toBe('TCP/30000-32767')
    const width = Number(label.closest('svg')!.getAttribute('width'))
    // 9px monospace is about 5.5px a character, plus the 4px inset each side.
    expect(width).toBeGreaterThanOrEqual(label.textContent!.length * 5.5 + 8)
  })

  it('sizes nodes and edges from one height', async () => {
    await mount(largePolicySpec)
    expect((columns() as HTMLElement).style.getPropertyValue('--node-h')).toBe('60px')
    for (const node of nodes('ingress')) expect(node.classList.contains('@2xl:h-[var(--node-h)]')).toBe(true)
  })

  it('shows the selected pods and what else is denied', async () => {
    await mount(largePolicySpec)
    const target = element.querySelector('[data-graph-target]')!
    expect(target.textContent).toContain('All pods in payments')
    expect(target.textContent).toContain('other inbound: denied')
    const egress = nodes('egress')
    expect(egress).toHaveLength(1)
    expect(egress[0].textContent).toContain('not restricted by this policy')
  })

  it('draws a deny-all direction as one node', async () => {
    await mount({ podSelector: {} })
    const ingress = nodes('ingress')
    expect(ingress).toHaveLength(1)
    expect(ingress[0].textContent).toContain('All inbound denied')
  })

  it('opens the clicked group below the graph and keeps the target in place', async () => {
    await mount(largePolicySpec)
    const target = element.querySelector('[data-graph-target]')
    await act(async () => (nodes('ingress')[2] as HTMLElement).click())
    const detail = element.querySelector('[data-graph-detail]')!
    expect(columns().contains(detail)).toBe(false)
    expect(detail.textContent).toContain('261 IP ranges')
    expect(detail.querySelector('input[type=search]')).not.toBeNull()
    expect(element.querySelector('[data-graph-target]')).toBe(target)
    expect(columns().contains(target)).toBe(true)

    await act(async () => (nodes('ingress')[2] as HTMLElement).click())
    expect(nodes('ingress')[2].getAttribute('aria-pressed')).toBe('false')
  })

  it('closes the detail panel when the selected rule disappears from the policy', async () => {
    await mount(largePolicySpec)
    await act(async () => (nodes('ingress')[2] as HTMLElement).click())
    const fewer = { ...largePolicySpec, ingress: largePolicySpec.ingress.slice(0, 2) }
    await act(async () => root!.render(<NetworkPolicyGraphView summary={summarizeNetworkPolicy(fewer, 'payments')} />))
    const panel = element.querySelector('[data-graph-detail]')!.parentElement!.parentElement!
    expect(panel.style.gridTemplateRows).toBe('0fr')
  })

  it('keeps the same group open when an earlier rule is removed', async () => {
    await mount(largePolicySpec)
    await act(async () => (nodes('ingress')[2] as HTMLElement).click())
    const withoutFirst = { ...largePolicySpec, ingress: largePolicySpec.ingress.slice(1) }
    await act(async () => root!.render(<NetworkPolicyGraphView summary={summarizeNetworkPolicy(withoutFirst, 'payments')} />))
    const detail = element.querySelector('[data-graph-detail]')!
    expect(detail.parentElement!.parentElement!.style.gridTemplateRows).toBe('1fr')
    expect(detail.textContent).toContain('261 IP ranges')
    expect(detail.textContent).toContain('Rule 2')
  })

  it('does not reopen the panel when a removed rule comes back', async () => {
    await mount(largePolicySpec)
    await act(async () => (nodes('ingress')[2] as HTMLElement).click())
    const fewer = { ...largePolicySpec, ingress: largePolicySpec.ingress.slice(0, 2) }
    await act(async () => root!.render(<NetworkPolicyGraphView summary={summarizeNetworkPolicy(fewer, 'payments')} />))
    await act(async () => root!.render(<NetworkPolicyGraphView summary={summarizeNetworkPolicy(largePolicySpec, 'payments')} />))
    const panel = element.querySelector('[data-graph-detail]')!.parentElement!.parentElement!
    expect(panel.style.gridTemplateRows).toBe('0fr')
  })

  it('gives every node of a rule its ports', async () => {
    await mount({
      ingress: [{ from: [{ namespaceSelector: nameIn('a') }, { ipBlock: { cidr: '10.0.0.0/8' } }], ports: [{ port: 8080 }] }],
    })
    for (const node of nodes('ingress')) expect(node.querySelector('[data-node-ports]')!.textContent).toContain('TCP/8080')
  })

  it('paints the target and deny nodes with theme backgrounds', async () => {
    await mount({ podSelector: {} })
    expect(element.querySelector('[data-graph-target]')!.classList.contains('bg-theme-elevated')).toBe(true)
    expect(element.innerHTML).not.toMatch(/bg-(red|indigo)-500\/8/)
  })

  it('dashes edges of a staged policy', async () => {
    await mount(largePolicySpec)
    expect(element.querySelector('path[data-edge="ingress"][stroke-dasharray]')).toBeNull()
    await act(async () => root!.unmount())
    element.remove()
    await mount(largePolicySpec, true)
    expect(element.querySelectorAll('path[data-edge="ingress"][stroke-dasharray]')).toHaveLength(4)
  })
})
