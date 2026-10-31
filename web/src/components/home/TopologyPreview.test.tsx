// @vitest-environment jsdom
import { act, type ComponentProps } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { Issue } from '@skyhook-io/k8s-ui'
import type { Topology } from '../../types'
import { TopologyPreview, rankNamespacesByIssues } from './TopologyPreview'

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })

type Props = ComponentProps<typeof TopologyPreview>

function issue(namespace: string | undefined, severity: 'critical' | 'warning', name = 'x'): Issue {
  return { id: `${namespace}/${name}/${severity}`, severity, namespace, name, kind: 'Deployment' } as Issue
}

const largeTopology = { nodes: [], edges: [], requiresNamespaceFilter: true } as unknown as Topology
const smallTopology = {
  nodes: [
    { id: 'deployment/default/web', kind: 'Deployment', name: 'web', status: 'healthy', data: { namespace: 'default' } },
    { id: 'service/default/web', kind: 'Service', name: 'web', status: 'healthy', data: { namespace: 'default' } },
  ],
  edges: [{ id: 'e1', source: 'service/default/web', target: 'deployment/default/web', type: 'exposes' }],
} as unknown as Topology

const props = (overrides: Partial<Props>): Props => ({
  topology: largeTopology,
  namespaceSelected: false,
  onNavigate: vi.fn(),
  issues: { issues: [], loading: false },
  onOpenNamespace: vi.fn(),
  ...overrides,
})

let container: HTMLDivElement
let root: Root

beforeEach(() => {
  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
})

afterEach(() => {
  act(() => root.unmount())
  container.remove()
})

function render(p: Props) {
  act(() => root.render(<TopologyPreview {...p} />))
}

function namespaceRows(): HTMLButtonElement[] {
  return Array.from(container.querySelectorAll<HTMLButtonElement>('li button'))
}

describe('rankNamespacesByIssues', () => {
  it('ranks by critical issues, then all issues, then name, and skips cluster-scoped issues', () => {
    const ranked = rankNamespacesByIssues([
      issue('payments', 'warning', 'a'),
      issue('payments', 'warning', 'b'),
      issue('payments', 'warning', 'c'),
      issue('auth', 'critical'),
      issue('billing', 'warning', 'a'),
      issue('billing', 'warning', 'b'),
      issue('api', 'warning', 'a'),
      issue('api', 'warning', 'b'),
      issue(undefined, 'critical', 'node-1'),
    ])
    expect(ranked).toEqual([
      { name: 'auth', critical: 1, total: 1 },
      { name: 'payments', critical: 0, total: 3 },
      { name: 'api', critical: 0, total: 2 },
      { name: 'billing', critical: 0, total: 2 },
    ])
  })

  it('caps the list', () => {
    const issues = ['a', 'b', 'c', 'd', 'e', 'f'].map(ns => issue(ns, 'warning'))
    expect(rankNamespacesByIssues(issues)).toHaveLength(4)
  })
})

describe('TopologyPreview', () => {
  it('keeps the graph preview when the cluster is small enough to graph', () => {
    render(props({ topology: smallTopology, issues: { issues: [issue('default', 'critical')], loading: false } }))
    const card = container.firstElementChild as HTMLElement
    expect(card.tagName).toBe('BUTTON')
    expect(container.textContent).toContain('2 resources')
    expect(namespaceRows()).toHaveLength(0)
  })

  it('keeps the graph preview while a picked namespace reaches the server', () => {
    render(props({ namespaceSelected: true, issues: { issues: [issue('default', 'critical')], loading: false } }))
    expect((container.firstElementChild as HTMLElement).tagName).toBe('BUTTON')
    expect(namespaceRows()).toHaveLength(0)
  })

  it('lists namespace names ranked by issues on a large cluster, without counts', () => {
    render(props({
      namespaceCount: 88,
      issues: {
        issues: [
          issue('payments', 'warning', 'a'), issue('payments', 'warning', 'b'), issue('auth', 'critical'),
          issue('c', 'warning'), issue('d', 'warning'), issue('e', 'warning'),
        ],
        loading: false,
      },
    }))
    expect(namespaceRows().map(r => r.textContent)).toEqual(['auth', 'payments', 'c', 'd'])
    // The dot is aria-hidden, so severity rides on the accessible name.
    expect(namespaceRows().slice(0, 2).map(r => r.getAttribute('aria-label'))).toEqual(['auth, critical issues', 'payments, issues'])
    expect(container.textContent).toContain('88 namespaces')
    expect(container.textContent).not.toMatch(/critical|issue/)
    // The drawing stays, as on the graph card.
    expect(container.querySelector('svg[aria-hidden="true"] marker#arr')).not.toBeNull()
  })

  it('names a single visible namespace in the singular', () => {
    render(props({ namespaceCount: 1, issues: { issues: [issue('payments', 'warning')], loading: false } }))
    expect(container.textContent).toContain('1 namespace')
    expect(container.textContent).not.toContain('1 namespaces')
  })

  it('leaves the namespace count out when it is not known', () => {
    render(props({ issues: { issues: [issue('payments', 'warning')], loading: false } }))
    expect(container.textContent).not.toMatch(/\d+ namespaces/)
  })

  it('opens the clicked namespace in Topology, and the rest of the card opens the view unfiltered', () => {
    const onOpenNamespace = vi.fn()
    const onNavigate = vi.fn()
    render(props({ onOpenNamespace, onNavigate, issues: { issues: [issue('payments', 'warning')], loading: false } }))
    act(() => namespaceRows()[0].click())
    expect(onOpenNamespace).toHaveBeenCalledWith('payments')
    expect(onNavigate).not.toHaveBeenCalled()

    const open = container.querySelector('button[aria-label="Open Topology"]') as HTMLButtonElement
    act(() => open.click())
    expect(onNavigate).toHaveBeenCalledTimes(1)
    expect(onOpenNamespace).toHaveBeenCalledTimes(1)
  })

  it('shows just the drawing when no namespace has issues, and the card still opens Topology', () => {
    const onNavigate = vi.fn()
    render(props({ onNavigate, issues: { issues: [issue(undefined, 'warning', 'node-1')], loading: false } }))
    expect(namespaceRows()).toHaveLength(0)
    expect(container.querySelector('svg[aria-hidden="true"] marker#arr')).not.toBeNull()
    act(() => (container.querySelector('button[aria-label="Open Topology"]') as HTMLButtonElement).click())
    expect(onNavigate).toHaveBeenCalledTimes(1)
  })

  it('shows placeholders while issues load', () => {
    render(props({ issues: { issues: [], loading: true } }))
    expect(container.querySelector('[aria-label="Loading namespaces"]')).not.toBeNull()
  })
})
