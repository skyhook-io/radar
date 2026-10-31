// @vitest-environment jsdom
import { act, type ReactNode } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { Issue } from '@skyhook-io/k8s-ui'
import { ProblemsPanel } from './HomeView'
import { IssueDiagnoseButton } from '../diagnose/LocalDiagnoseAction'
import { HOVER_REVEAL_ACTION, HOVER_REVEAL_HIT, HOVER_REVEAL_REST } from '../../utils/animation'

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

const classesOf = (fragment: string) => fragment.split(/\s+/).filter(Boolean)

const issue: Issue = {
  id: 'deploy-unavailable', severity: 'critical', source: 'problem', category: 'workload', category_group: 'workload', grouping_scope: 'resource',
  kind: 'Deployment', group: 'apps', namespace: 'gl-shop', name: 'checkout', reason: 'Unavailable', message: 'No ready replicas',
}
const target = { kind: 'Deployment', group: 'apps', namespace: 'gl-shop', name: 'checkout' }

let root: Root
let container: HTMLDivElement
const onResourceClick = vi.fn()

function render(rowIssue: Issue = issue) {
  act(() => {
    root.render(
      <ProblemsPanel
        issues={[rowIssue]}
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

  const agedIssue = () => ({ ...issue, first_seen: new Date(Date.now() - 3_600_000).toISOString() })
  const ageSlot = () => container.querySelector('.group button .ml-auto')!

  it('swaps the age slot for the action on hover or keyboard focus, in the same group', () => {
    render(agedIssue())
    const action = investigateButton()!
    const group = action.closest('.group')!
    expect(ageSlot().textContent).toMatch(/\d/)
    for (const c of classesOf(HOVER_REVEAL_REST)) expect(ageSlot().classList).toContain(c)
    const overlay = group.lastElementChild!
    expect(overlay.contains(action)).toBe(true)
    expect(overlay.classList).toContain('absolute')
    for (const c of classesOf(HOVER_REVEAL_ACTION)) expect(overlay.classList).toContain(c)
    // The overlay itself never takes clicks; only the control inside does.
    expect(overlay.classList).toContain('pointer-events-none')
    const hit = action.parentElement!.closest('span:not([aria-hidden])')!
    expect(hit.parentElement).toBe(overlay)
    for (const c of classesOf(HOVER_REVEAL_HIT)) expect(hit.classList).toContain(c)
    // Keyboard reveal keys on :focus-visible, so a row left focused by a click
    // doesn't keep its action shown.
    expect(HOVER_REVEAL_ACTION).toContain('group-has-[:focus-visible]:opacity-100')
  })

  it('keeps the age slot in place where there is no action to swap in', () => {
    diagnose.setupState = 'off'
    render(agedIssue())
    expect(ageSlot().textContent).toMatch(/\d/)
    expect(ageSlot().classList).not.toContain('group-hover:opacity-0')
    expect(container.querySelector('.group > div')).toBeNull()
  })
})

describe('IssueDiagnoseButton size', () => {
  it('keeps the Issues page look by default and shrinks only when compact', () => {
    act(() => root.render(<><IssueDiagnoseButton {...target} /><IssueDiagnoseButton {...target} size="compact" /></>))
    const [regular, compact] = buttons()
    expect(regular.className).toContain('px-2 py-1 text-xs')
    expect(compact.className).toContain('h-4')
    expect(compact.className).not.toContain('py-1 text-xs')
  })
})
