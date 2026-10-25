// @vitest-environment jsdom
import { act, createRef, useState, type ReactNode } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { NamespacePicker, type NamespacePickerHandle, type NamespacePickerProps, type NamespaceScopeView } from './NamespacePicker'

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

describe('NamespacePicker Only button', () => {
  const scope = { ...baseScope, actives: ['team-a'], accessibleNamespaces: ['team-a', 'team-b', 'team-c'] }

  const triggerButton = () =>
    document.querySelector<HTMLButtonElement>('button[aria-label="Switch active namespaces"]')!

  async function open(onApply = vi.fn()) {
    await act(async () => {
      root.render(<NamespacePicker scope={scope} onApply={onApply} />)
    })
    await act(async () => { triggerButton().click() })
    return onApply
  }

  const onlyButton = (ns: string) =>
    document.querySelector<HTMLButtonElement>(`button[aria-label="Show only ${ns}"]`)!
  const row = (ns: string) =>
    Array.from(document.querySelectorAll('label')).find(l => l.textContent?.includes(ns))!
  const checkbox = (ns: string) => row(ns).querySelector<HTMLInputElement>('input[type="checkbox"]')!
  const doneButton = () =>
    Array.from(document.querySelectorAll('button')).find(b => b.textContent === 'Done')!
  const isOpen = () => document.querySelector('input[type="checkbox"]') !== null

  it('puts an Only button on every row', async () => {
    await open()
    for (const ns of ['team-a', 'team-b', 'team-c']) expect(onlyButton(ns).textContent).toBe('Only')
  })

  it('applies just that namespace and closes the dropdown', async () => {
    const onApply = await open()
    expect(isOpen()).toBe(true)
    await act(async () => { onlyButton('team-c').click() })

    expect(onApply).toHaveBeenCalledTimes(1)
    expect(onApply).toHaveBeenCalledWith(['team-c'])
    expect(isOpen()).toBe(false)
  })

  it('replaces unsaved checkbox toggles', async () => {
    const onApply = await open()
    await act(async () => { checkbox('team-b').click() })
    await act(async () => { onlyButton('team-c').click() })

    expect(onApply).toHaveBeenCalledWith(['team-c'])
  })

  it('does not re-apply when that namespace is already the only one active', async () => {
    const onApply = await open()
    await act(async () => { onlyButton('team-a').click() })

    expect(onApply).not.toHaveBeenCalled()
    expect(isOpen()).toBe(false)
  })

  it('keeps the name and checkbox as add/remove toggles', async () => {
    const onApply = await open()
    await act(async () => { checkbox('team-b').click() })
    expect(checkbox('team-b').checked).toBe(true)
    expect(onApply).not.toHaveBeenCalled()
    expect(isOpen()).toBe(true)

    const name = Array.from(row('team-c').querySelectorAll('span')).find(s => s.textContent === 'team-c')!
    await act(async () => { name.click() })
    expect(checkbox('team-c').checked).toBe(true)
    expect(onApply).not.toHaveBeenCalled()
    expect(isOpen()).toBe(true)

    await act(async () => { doneButton().click() })
    expect(onApply).toHaveBeenCalledWith(['team-a', 'team-b', 'team-c'])
  })

  it('keeps the button out of the row label', async () => {
    await open()
    expect(document.querySelector('label button')).toBeNull()
    expect(onlyButton('team-b').parentElement).toBe(row('team-b').parentElement)
  })

  it('is reachable by keyboard, with no tabindex taking it out of the tab order', async () => {
    await open()
    const only = onlyButton('team-b')
    expect(only.tabIndex).toBe(0)
    only.focus()
    expect(document.activeElement).toBe(only)
  })

  it.each(['Only', 'Done', 'Clear all'])('reopens from active namespaces after an unacknowledged %s apply', async (action) => {
    const onApply = await open()
    if (action === 'Only') {
      await act(async () => { onlyButton('team-c').click() })
    } else if (action === 'Done') {
      await act(async () => { checkbox('team-b').click() })
      await act(async () => { doneButton().click() })
    } else {
      const clear = document.querySelector<HTMLButtonElement>('button[aria-label="Clear namespace selection"]')!
      await act(async () => { clear.click() })
    }
    expect(onApply).toHaveBeenCalledTimes(1)
    expect(isOpen()).toBe(false)

    await act(async () => { triggerButton().click() })
    expect(checkbox('team-a').checked).toBe(true)
    expect(checkbox('team-b').checked).toBe(false)
    expect(checkbox('team-c').checked).toBe(false)
    await act(async () => { document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true })) })
    expect(onApply).toHaveBeenCalledTimes(1)
  })

  it('also refreshes the draft when the keyboard shortcut opens the picker', async () => {
    const ref = createRef<NamespacePickerHandle>()
    const onApply = vi.fn()
    await act(async () => { root.render(<NamespacePicker ref={ref} scope={scope} onApply={onApply} />) })
    await act(async () => { ref.current?.open() })
    await act(async () => { onlyButton('team-c').click() })
    await act(async () => { ref.current?.open() })
    expect(checkbox('team-a').checked).toBe(true)
    expect(checkbox('team-c').checked).toBe(false)
    await act(async () => { doneButton().click() })
    expect(onApply).toHaveBeenCalledTimes(1)
  })

  it('keeps current edits if the open shortcut is used while the menu is already open', async () => {
    const ref = createRef<NamespacePickerHandle>()
    const onApply = vi.fn()
    await act(async () => { root.render(<NamespacePicker ref={ref} scope={scope} onApply={onApply} />) })
    await act(async () => { ref.current?.open() })
    await act(async () => { checkbox('team-b').click() })
    await act(async () => { ref.current?.open() })
    expect(checkbox('team-a').checked).toBe(true)
    expect(checkbox('team-b').checked).toBe(true)
    expect(onApply).not.toHaveBeenCalled()
  })

  describe('returning focus after the switch', () => {
    let setPending: (pending: boolean) => void

    function Host({ onApply, pendingOnApply = true }: { onApply: (namespaces: string[]) => void; pendingOnApply?: boolean }) {
      const [pending, setPendingState] = useState(false)
      setPending = setPendingState
      return (
        <NamespacePicker
          scope={scope}
          pending={pending}
          onApply={namespaces => {
            if (pendingOnApply) setPendingState(true)
            onApply(namespaces)
          }}
        />
      )
    }

    async function openAndPressOnly(ns: string, pendingOnApply = true) {
      const onApply = vi.fn()
      await act(async () => { root.render(<Host onApply={onApply} pendingOnApply={pendingOnApply} />) })
      await act(async () => { triggerButton().click() })
      const only = onlyButton(ns)
      only.focus()
      await act(async () => { only.click() })
      return onApply
    }

    // A browser blurs a focused button the moment it becomes disabled; jsdom does not.
    async function startPending() {
      ;(document.activeElement as HTMLElement | null)?.blur()
      await act(async () => { setPending(true) })
    }

    it('puts focus back on the trigger when the switch is instant', async () => {
      await open()
      const only = onlyButton('team-a')
      only.focus()
      await act(async () => { only.click() })

      expect(document.activeElement).toBe(triggerButton())
    })

    it('waits for the pending switch to finish, because the trigger is inert until then', async () => {
      await openAndPressOnly('team-c')
      expect(triggerButton().disabled).toBe(true)
      expect(document.activeElement).toBe(document.body)

      await act(async () => { setPending(false) })
      expect(document.activeElement).toBe(triggerButton())
    })

    it('restores focus again when the host only reports pending after the menu has closed', async () => {
      await openAndPressOnly('team-c', false)
      expect(document.activeElement).toBe(triggerButton())

      await startPending()
      expect(triggerButton().disabled).toBe(true)
      expect(document.activeElement).toBe(document.body)

      await act(async () => { setPending(false) })
      expect(document.activeElement).toBe(triggerButton())
    })

    it('leaves focus alone when the user moved elsewhere during the switch', async () => {
      const elsewhere = document.createElement('input')
      document.body.appendChild(elsewhere)
      await openAndPressOnly('team-c')
      await act(async () => { elsewhere.focus() })

      await act(async () => { setPending(false) })
      expect(document.activeElement).toBe(elsewhere)
    })

    it('leaves focus alone when the user clicked elsewhere during the switch', async () => {
      await openAndPressOnly('team-c')
      await act(async () => { document.body.dispatchEvent(new Event('pointerdown', { bubbles: true })) })

      await act(async () => { setPending(false) })
      expect(document.activeElement).toBe(document.body)
    })

    it('does not claim focus for a later switch once the user has moved on', async () => {
      const elsewhere = document.createElement('input')
      document.body.appendChild(elsewhere)
      await openAndPressOnly('team-c', false)
      await act(async () => { elsewhere.focus() })
      await act(async () => { elsewhere.blur() })

      await startPending()
      await act(async () => { setPending(false) })
      expect(document.activeElement).toBe(document.body)
    })

    it('does not move focus for other ways of closing', async () => {
      await open()
      await act(async () => { doneButton().click() })
      expect(document.activeElement).toBe(document.body)
    })
  })
})
