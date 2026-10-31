// @vitest-environment jsdom
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { HomeAgentBlock } from './HomeAgentBlock'

const diagnose = vi.hoisted(() => ({ current: {} as Record<string, unknown> }))
const nav = vi.hoisted(() => ({ embedded: false }))

vi.mock('../diagnose/DiagnoseContext', () => ({ useDiagnose: () => diagnose.current }))
vi.mock('../../context/NavCustomization', () => ({ useNavCustomization: () => nav }))
vi.mock('./MCPSetupDialog', () => ({
  MCPSetupDialog: ({ open, readOnly }: { open: boolean; readOnly?: boolean }) =>
    open ? <div data-testid="mcp-dialog">{readOnly ? 'read-only' : 'full'}</div> : null,
}))

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })
let root: Root
let host: HTMLDivElement

function setDiagnose(overrides: Record<string, unknown>) {
  diagnose.current = {
    available: true,
    setupState: 'ready',
    agentLabel: 'Claude Code',
    hosted: false,
    questions: true,
    cliOverride: false,
    openQuestion: vi.fn(),
    openWorkspace: vi.fn(),
    openHome: vi.fn(),
    ...overrides,
  }
}

function render(props: { readOnly?: boolean } = {}) {
  act(() => {
    root.render(<HomeAgentBlock mcpUrl="http://localhost:9280/mcp" {...props} />)
  })
}

const button = (name: RegExp) =>
  [...host.querySelectorAll('button')].find((b) => name.test(b.textContent ?? ''))

beforeEach(() => {
  nav.embedded = false
  setDiagnose({})
  host = document.createElement('div')
  document.body.appendChild(host)
  root = createRoot(host)
})

afterEach(() => {
  act(() => root.unmount())
  host.remove()
})

describe('HomeAgentBlock', () => {
  it('names the agent and opens the question composer', () => {
    render()
    const ask = button(/Ask Claude Code about this cluster/)
    expect(ask).toBeTruthy()
    act(() => ask!.click())
    expect(diagnose.current.openQuestion).toHaveBeenCalledWith({})
  })

  it('opens investigations when the backend does not take questions', () => {
    setDiagnose({ questions: false, agentLabel: 'Codex' })
    render()
    act(() => button(/Investigate with Codex/)!.click())
    expect(diagnose.current.openWorkspace).toHaveBeenCalled()
    expect(diagnose.current.openQuestion).not.toHaveBeenCalled()
  })

  it('offers agent setup when no agent is installed', () => {
    setDiagnose({ available: false, setupState: 'needs-install' })
    render()
    expect(button(/Ask/)).toBeUndefined()
    act(() => button(/Set up an agent/)!.click())
    expect(diagnose.current.openHome).toHaveBeenCalled()
  })

  it('points at the setup notice when an installed agent cannot run', () => {
    setDiagnose({ available: false, setupState: 'needs-restart', cliOverride: true })
    render()
    act(() => button(/Check agent setup/)!.click())
    expect(diagnose.current.openHome).toHaveBeenCalled()
  })

  it.each(['off', 'unknown'])('shows only the MCP line when investigations are %s here', (setupState) => {
    setDiagnose({ available: false, setupState })
    render()
    expect(button(/Ask|Investigate|Set up|Check agent/)).toBeUndefined()
    expect(host.textContent).toContain('MCP live')
    expect(button(/^Connect$/)).toBeTruthy()
  })

  it('adds nothing for an embedded host', () => {
    nav.embedded = true
    render()
    expect(button(/Ask|Investigate|Set up/)).toBeUndefined()
    expect(host.textContent).toContain('MCP live')
  })

  it('keeps the MCP endpoint and opens the setup dialog', () => {
    render({ readOnly: true })
    expect(host.textContent).toContain('MCP live (read-only)')
    expect(host.textContent).toContain('localhost:9280/mcp')
    expect(host.querySelector('[data-testid="mcp-dialog"]')).toBeNull()
    act(() => button(/^Connect$/)!.click())
    expect(host.querySelector('[data-testid="mcp-dialog"]')?.textContent).toBe('read-only')
  })
})
