// @vitest-environment jsdom
import { act, type ReactNode } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { NamespacePicker, type NamespacePickerProps, type NamespaceScopeView } from './NamespacePicker'

vi.mock('../ui/Tooltip', () => ({
  Tooltip: ({ children, content }: { children: ReactNode; content: ReactNode }) => (
    <>
      {children}
      <span data-testid="tooltip">{content}</span>
    </>
  ),
}))

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })
let root: Root

beforeEach(() => {
  const element = document.createElement('div')
  document.body.appendChild(element)
  root = createRoot(element)
})

afterEach(async () => {
  await act(async () => root.unmount())
  document.body.replaceChildren()
})

const baseScope: NamespaceScopeView = {
  actives: ['team-a'],
  accessibleNamespaces: ['team-a'],
  kubeconfigNamespace: 'team-a',
  deniedNamespaces: [],
  mode: 'namespace',
  cacheScoped: false,
  namespaceRescope: false,
  canClearNamespace: true,
  authoritative: false,
}

const help = <a data-testid="help">How to add namespaces</a>

async function renderOpen(scope: Partial<NamespaceScopeView>, props: Partial<NamespacePickerProps> = {}) {
  await act(async () => {
    root.render(<NamespacePicker scope={{ ...baseScope, ...scope }} onApply={vi.fn()} {...props} />)
  })
  const trigger = document.querySelector<HTMLButtonElement>('button[aria-label="Switch active namespaces"]')!
  const warning = trigger.querySelector('.lucide-triangle-alert, .lucide-alert-triangle') !== null
  const tooltip = document.querySelector('[data-testid="tooltip"]')?.textContent ?? ''
  await act(async () => { trigger.click() })
  return { warning, tooltip, text: document.body.textContent ?? '' }
}

describe('NamespacePicker incomplete-list notice', () => {
  it.each(['namespace', 'restricted'] as const)('offers the host action for a non-authoritative list in %s mode', async (mode) => {
    const { text } = await renderOpen({ mode }, { limitedListHelp: help })
    expect(text).toContain('Missing a namespace?')
    expect(document.querySelector('[data-testid="help"]')).not.toBeNull()
  })

  it('says nothing about the list when the host has no action to offer', async () => {
    const { warning, text } = await renderOpen({})
    expect(warning).toBe(false)
    expect(text).not.toContain('Missing a namespace?')
  })

  it('says nothing when the list is authoritative', async () => {
    const { warning, text } = await renderOpen({ authoritative: true }, { limitedListHelp: help })
    expect(warning).toBe(false)
    expect(text).not.toContain('Missing a namespace?')
    expect(document.querySelector('[data-testid="help"]')).toBeNull()
  })

  it('points an empty list at the note below it', async () => {
    const { text } = await renderOpen(
      { actives: [], accessibleNamespaces: [], mode: 'restricted' },
      { limitedListHelp: help },
    )
    expect(text).toContain('No namespaces yet.')
    expect(text).toContain('Missing a namespace?')
  })

  // The picker can't tell an unconfigured list from a configured or timed-out
  // one, so a standing warning icon would nag users who have nothing to fix.
  it('never puts a warning icon on the trigger', async () => {
    const { warning } = await renderOpen({ mode: 'namespace' }, { limitedListHelp: help })
    expect(warning).toBe(false)
  })

  it('says Radar can\u2019t list namespaces when the host can help and nothing is picked', async () => {
    const { tooltip } = await renderOpen({ actives: [], mode: 'restricted' }, { limitedListHelp: help })
    expect(tooltip).toBe('Radar can\u2019t list namespaces on this cluster.')
  })

  // A picked view keeps describing its filter; the footer carries the help.
  it('keeps the filter tooltip once a namespace is picked', async () => {
    const { tooltip, text } = await renderOpen({ mode: 'namespace' }, { limitedListHelp: help })
    expect(tooltip).toBe('View is filtered to namespace team-a. Click to switch or reset.')
    expect(text).toContain('Missing a namespace?')
  })

  // Without a host action the list is usually the viewer's complete RBAC view
  // (auth-enabled installs, Radar Cloud), so say what it is rather than warn.
  it('describes an RBAC-scoped list with nothing picked', async () => {
    const { tooltip, text } = await renderOpen({ actives: [], mode: 'restricted' })
    expect(tooltip).toBe('Showing the namespaces your account can access.')
    expect(text).not.toContain('Missing a namespace?')
  })

  it('keeps the usual tooltip for an authoritative list', async () => {
    const { tooltip } = await renderOpen({ actives: [], mode: 'cluster-wide', authoritative: true })
    expect(tooltip).toBe('Currently viewing all namespaces. Click to narrow the view.')
  })

  // The backend keeps saved picks through a failed namespace load, when the
  // list it returns can be shorter than the pick.
  it('lists a pick missing from the available namespaces so it can be unchecked', async () => {
    const onApply = vi.fn()
    await act(async () => {
      root.render(
        <NamespacePicker
          scope={{ ...baseScope, actives: ['team-a', 'team-b'], accessibleNamespaces: ['team-a'] }}
          onApply={onApply}
        />,
      )
    })
    const trigger = document.querySelector<HTMLButtonElement>('button[aria-label="Switch active namespaces"]')!
    await act(async () => { trigger.click() })

    const row = Array.from(document.querySelectorAll('label')).find(l => l.textContent?.includes('team-b'))
    const checkbox = row?.querySelector<HTMLInputElement>('input[type="checkbox"]')
    expect(checkbox?.checked).toBe(true)

    await act(async () => { checkbox!.click() })
    const done = Array.from(document.querySelectorAll('button')).find(b => b.textContent === 'Done')!
    await act(async () => { done.click() })
    expect(onApply).toHaveBeenCalledWith(['team-a'])
  })
})
