// @vitest-environment jsdom
import { act, useEffect } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { DockProvider, useDock } from '../../../../packages/k8s-ui/src/components/dock/DockContext'
import type { LocalTerminalTabProps } from '../../../../packages/k8s-ui/src/components/dock/LocalTerminalTab'
import { LocalTerminalTab } from './LocalTerminalTab'

const state = vi.hoisted(() => ({
  context: 'staging',
  props: new Map<string, LocalTerminalTabProps>(),
}))
vi.mock('../../context/ConnectionContext', () => ({
  useConnection: () => ({ connection: { context: state.context } }),
}))
vi.mock('@skyhook-io/k8s-ui', async () => {
  const dock = await import('../../../../packages/k8s-ui/src/components/dock/DockContext')
  const { ClusterName } = await import('../../../../packages/k8s-ui/src/components/ui/ClusterName')
  const { Tooltip } = await import('../../../../packages/k8s-ui/src/components/ui/Tooltip')
  const { parseContextName } = await import('../../../../packages/k8s-ui/src/utils/context-name')
  return {
    ...dock, ClusterName, Tooltip, parseContextName,
    LocalTerminalTab: (props: LocalTerminalTabProps) => {
      state.props.set(props.initialCommand!, props)
      return <div data-testid="terminal-toolbar">{props.toolbarExtra}</div>
    },
  }
})
Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })

let root: Root
let element: HTMLDivElement
function Harness() {
  const { tabs, addTab } = useDock()
  useEffect(() => {
    addTab({ type: 'local-terminal', title: 'Terminal', initialCommand: 'first' })
    addTab({ type: 'local-terminal', title: 'Auth', initialCommand: 'second' })
  }, [addTab])
  return <>{tabs.map(tab => (
    <section key={tab.id}>
      <h2 data-tooltip={tab.titleTooltip}>{tab.title}</h2>
      <LocalTerminalTab tabId={tab.id} title={tab.title} initialCommand={tab.initialCommand} />
    </section>
  ))}</>
}
async function render() {
  await act(async () => root.render(<DockProvider><Harness /></DockProvider>))
}
beforeEach(() => {
  state.context = 'staging'
  state.props.clear()
  element = document.createElement('div')
  document.body.appendChild(element)
  root = createRoot(element)
  vi.stubGlobal('ResizeObserver', class { observe() {} disconnect() {} })
})
afterEach(async () => {
  await act(async () => root.unmount())
  element.remove()
  vi.unstubAllGlobals()
})

it('waits for server metadata, labels each tab independently, and notices a UI switch', async () => {
  await render()
  expect(element.textContent).toContain('Context not confirmed')
  expect(element.querySelector('[role="status"]')).toBeNull()
  await act(async () => {
    state.props.get('first')!.onSessionInfo!({ context: 'production', kubeconfigIsolated: true })
    state.props.get('second')!.onSessionInfo!({ context: 'staging', kubeconfigIsolated: true })
  })
  expect([...element.querySelectorAll('h2')].map(el => el.textContent)).toEqual(['Terminal · production', 'Auth · staging'])
  expect(element.querySelector('[role="status"]')?.getAttribute('aria-label')).toContain('This terminal was opened for production')
  expect(element.querySelector('[role="status"]')?.closest('[data-testid="terminal-toolbar"]')).not.toBeNull()
  state.context = 'production'
  await render()
  const sections = element.querySelectorAll('section')
  expect(sections[0].querySelector('[role="status"]')).toBeNull()
  expect(sections[0].textContent).toContain('Opened for:production')
  expect(sections[1].querySelector('[role="status"]')?.getAttribute('aria-label')).toContain('This terminal was opened for staging')
  await act(async () => sections[1].querySelector<HTMLButtonElement>('button')!.click())
  expect([...element.querySelectorAll('h2')].map(el => el.textContent)).toEqual(['Terminal · production', 'Auth · staging', 'Terminal'])
})

it('marks fallback sessions unconfirmed and replaces labels for a newly connected shell', async () => {
  await render()
  await act(async () => state.props.get('second')!.onSessionInfo!({ context: '', kubeconfigIsolated: false }))
  const section = element.querySelectorAll('section')[1]
  expect(section.textContent).toContain('Auth · context not confirmed')
  expect(section.textContent).toContain('Context not confirmed')
  expect(section.querySelector('[role="status"]')).toBeNull()
  await act(async () => state.props.get('second')!.onSessionInfo!(null))
  expect(section.querySelector('h2')?.textContent).toBe('Auth')
  await act(async () => state.props.get('second')!.onSessionInfo!({ context: 'staging', kubeconfigIsolated: true }))
  expect(section.querySelector('h2')?.textContent).toBe('Auth · staging')
})

it('compares full contexts even when both have the same short cluster name', async () => {
  state.context = 'gke_project-b_us-east1_production'
  await render()
  await act(async () => state.props.get('first')!.onSessionInfo!({ context: 'gke_project-a_us-east1_production', kubeconfigIsolated: true }))
  expect(element.querySelector('h2')?.textContent).toBe('Terminal · production')
  expect(element.querySelector('h2')?.getAttribute('data-tooltip')).toBe('gke_project-a_us-east1_production')
  const message = element.querySelector('[role="status"]')?.getAttribute('aria-label')
  expect(message).toContain('gke_project-a_us-east1_production')
  expect(message).toContain('gke_project-b_us-east1_production')
  state.context = ''
  await render()
  expect(element.querySelector('[role="status"]')).toBeNull()
})
