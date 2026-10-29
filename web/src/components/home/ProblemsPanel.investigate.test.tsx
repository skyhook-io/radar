// @vitest-environment jsdom
import { act, type ReactNode } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { Issue } from '@skyhook-io/k8s-ui'
import { ProblemsPanel } from './HomeView'

const diagnose = vi.hoisted(() => ({
  setupState: 'ready' as string,
  available: true,
  hosted: false,
  agentLabel: 'Claude Code',
  cliOverride: false,
  openInvestigation: vi.fn(),
  openHome: vi.fn(),
}))
vi.mock('../diagnose/DiagnoseContext', () => ({ useDiagnose: () => diagnose }))
vi.mock('../ui/Tooltip', () => ({ Tooltip: ({ children }: { children: ReactNode }) => children }))

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })

const issue: Issue = {
  id: 'deploy-unavailable', severity: 'critical', source: 'problem', category: 'workload', category_group: 'workload', grouping_scope: 'resource',
  kind: 'Deployment', group: 'apps', namespace: 'gl-shop', name: 'checkout', reason: 'Unavailable', message: 'No ready replicas',
}
const target = { kind: 'Deployment', group: 'apps', namespace: 'gl-shop', name: 'checkout' }

let root: Root
let container: HTMLDivElement
const onResourceClick = vi.fn()

function render() {
  act(() => {
    root.render(
      <ProblemsPanel
        issues={[issue]}
        issueCount={1}
        onRetryIssues={() => {}}
        hasData
        isLoading={false}
        isRefreshing={false}
        error={null}
        totalReturned={1}
        onNavigateToIssues={() => {}}
        onResourceClick={onResourceClick}
      />,
    )
  })
}

const buttons = () => Array.from(container.querySelectorAll('button'))
const investigateButton = () => buttons().find((b) => b.textContent?.trim() === 'Investigate')

beforeEach(() => {
  Object.assign(diagnose, { setupState: 'ready', available: true })
  diagnose.openInvestigation.mockReset()
  diagnose.openHome.mockReset()
  onResourceClick.mockReset()
  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
})

afterEach(() => {
  act(() => root.unmount())
  container.remove()
})

describe('Home Active Issues Investigate action', () => {
  it('starts an investigation for the row resource without opening it', () => {
    render()
    const button = investigateButton()
    expect(button).toBeDefined()
    act(() => button!.click())
    expect(diagnose.openInvestigation).toHaveBeenCalledWith(target)
    expect(onResourceClick).not.toHaveBeenCalled()
  })

  it('still opens the resource from the rest of the row', () => {
    render()
    const row = buttons().find((b) => b.textContent?.includes('checkout'))
    act(() => row!.click())
    expect(onResourceClick).toHaveBeenCalledWith(target)
    expect(diagnose.openInvestigation).not.toHaveBeenCalled()
  })

  it('does not nest the action inside the row button', () => {
    render()
    expect(investigateButton()!.parentElement!.closest('button')).toBeNull()
  })

  it.each(['off', 'unknown'])('is absent where investigations cannot run (%s)', (setupState) => {
    diagnose.setupState = setupState
    render()
    expect(investigateButton()).toBeUndefined()
  })

  it('opens setup instead of starting a run when no agent is ready', () => {
    Object.assign(diagnose, { setupState: 'needs-install', available: false })
    render()
    act(() => investigateButton()!.click())
    expect(diagnose.openHome).toHaveBeenCalled()
    expect(diagnose.openInvestigation).not.toHaveBeenCalled()
    expect(onResourceClick).not.toHaveBeenCalled()
  })
})
