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
  issues: { issues: [], truncated: false, incomplete: false, loading: false },
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
    render(props({ topology: smallTopology, issues: { issues: [issue('default', 'critical')], truncated: false, incomplete: false, loading: false } }))
    const card = container.firstElementChild as HTMLElement
    expect(card.tagName).toBe('BUTTON')
    expect(container.textContent).toContain('2 resources')
    expect(container.textContent).not.toContain('Too large')
    expect(namespaceRows()).toHaveLength(0)
  })

  it('keeps the graph preview while a picked namespace reaches the server', () => {
    render(props({ namespaceSelected: true, issues: { issues: [issue('default', 'critical')], truncated: false, incomplete: false, loading: false } }))
    expect((container.firstElementChild as HTMLElement).tagName).toBe('BUTTON')
    expect(namespaceRows()).toHaveLength(0)
  })

  it('lists the namespaces with the most issues on a large cluster', () => {
    render(props({
      issues: {
        issues: [issue('payments', 'warning', 'a'), issue('payments', 'warning', 'b'), issue('auth', 'critical')],
        truncated: false,
        incomplete: false,
        loading: false,
      },
    }))
    const rows = namespaceRows()
    expect(rows.map(r => r.textContent)).toEqual([
      'auth1 critical · 1 issue',
      'payments2 issues',
    ])
  })

  it('marks counts as lower bounds when the issue list was cut short', () => {
    render(props({ issues: { issues: [issue('payments', 'critical')], truncated: true, incomplete: false, loading: false } }))
    expect(namespaceRows()[0].textContent).toBe('payments≥1 critical · ≥1 issue')
    expect(container.textContent).toContain('Namespaces from the 1 most severe issues:')
    expect(container.textContent).not.toContain('most issues')
  })

  it('qualifies counts when some issue sources went unchecked', () => {
    render(props({ issues: { issues: [issue('payments', 'warning')], truncated: false, incomplete: true, loading: false } }))
    expect(namespaceRows()[0].textContent).toBe('payments≥1 issue')
    expect(container.textContent).toContain('Namespaces with the most issues Radar could check:')
  })

  it('opens the clicked namespace in Topology, and Open Topology still opens the view', () => {
    const onOpenNamespace = vi.fn()
    const onNavigate = vi.fn()
    render(props({ onOpenNamespace, onNavigate, issues: { issues: [issue('payments', 'warning')], truncated: false, incomplete: false, loading: false } }))
    act(() => namespaceRows()[0].click())
    expect(onOpenNamespace).toHaveBeenCalledWith('payments')
    expect(onNavigate).not.toHaveBeenCalled()

    const open = Array.from(container.querySelectorAll('button')).find(b => b.textContent === 'Open Topology')!
    act(() => open.click())
    expect(onNavigate).toHaveBeenCalledTimes(1)
  })

  it('offers the namespace picker when no namespace has issues', () => {
    const onNavigate = vi.fn()
    render(props({ onNavigate, issues: { issues: [issue(undefined, 'warning', 'node-1')], truncated: false, incomplete: false, loading: false } }))
    expect(namespaceRows()).toHaveLength(0)
    expect(container.textContent).not.toContain('most issues')
    const choose = Array.from(container.querySelectorAll('button')).find(b => b.textContent === 'Choose a namespace')!
    act(() => choose.click())
    expect(onNavigate).toHaveBeenCalledTimes(1)
  })

  it('shows placeholders, not the picker fallback, while issues load', () => {
    render(props({ issues: { issues: [], truncated: false, incomplete: false, loading: true } }))
    expect(container.querySelector('[aria-label="Loading namespaces"]')).not.toBeNull()
    expect(container.textContent).not.toContain('Choose a namespace')
  })
})
