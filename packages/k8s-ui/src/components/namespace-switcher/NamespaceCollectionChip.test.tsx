// @vitest-environment jsdom
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { NamespaceCollectionChip } from './NamespaceCollectionChip'
import type { NamespaceCollection } from '../../utils/namespace-collection'

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

const collection: NamespaceCollection = {
  groups: [
    { kinds: [{ kind: 'ConfigMap' }, { kind: 'Pod' }], namespaces: { count: 1, names: ['team-a'] } },
    {
      kinds: ['Deployment', 'Job', 'CronJob', 'Secret', 'Service'].map(kind => ({ kind })),
      namespaces: { count: 2, names: ['team-a', 'team-b'] },
    },
  ],
}

async function render(props: Partial<{ collection: NamespaceCollection; variant: 'chip' | 'segment' }> = {}) {
  await act(async () => {
    root.render(
      <>
        <button data-testid="outside">Elsewhere</button>
        <NamespaceCollectionChip collection={collection} variant="chip" {...props} />
      </>,
    )
  })
}

const chip = () => document.querySelector<HTMLButtonElement>('button[aria-label^="Partial"]')!
const panel = () => document.querySelector<HTMLElement>('[role="group"]')
const press = (key: string) => act(async () => {
  chip().dispatchEvent(new KeyboardEvent('keydown', { key, bubbles: true, cancelable: true }))
})
const open = () => act(async () => { chip().click() })

describe('NamespaceCollectionChip', () => {
  it('is a button that says "Partial" and starts collapsed', async () => {
    await render()
    expect(chip().type).toBe('button')
    expect(chip().textContent).toBe('Partial')
    expect(chip().getAttribute('aria-expanded')).toBe('false')
    expect(panel()).toBeNull()
  })

  it('opens a panel naming which kinds Radar collects in which namespaces', async () => {
    await render()
    await open()
    expect(chip().getAttribute('aria-expanded')).toBe('true')
    expect(chip().getAttribute('aria-controls')).toBe(panel()!.id)
    expect(panel()!.textContent).toContain('Radar collects some kinds only in some namespaces.')
    expect(panel()!.textContent).toContain('ConfigMaps and Pods in team-a')
    expect(panel()!.textContent).toContain('5 kinds in team-a and team-b')
  })

  it('closes on a second press, and leaves nothing for the keyboard to reach', async () => {
    await render()
    await open()
    await open()
    expect(chip().getAttribute('aria-expanded')).toBe('false')
    expect(panel()?.hasAttribute('inert') ?? true).toBe(true)
  })

  it('keeps keyboard focus on the button while it opens and closes', async () => {
    await render()
    const button = chip()
    button.focus()
    await open()
    expect(chip()).toBe(button)
    expect(document.activeElement).toBe(button)
    await open()
    expect(chip()).toBe(button)
    expect(document.activeElement).toBe(button)
  })

  it('limits the panel to the room below the button and scrolls inside it', async () => {
    await render()
    await open()
    expect(panel()!.style.maxHeight).toBe(`${window.innerHeight - 12}px`)
    expect(panel()!.className).toContain('overflow-y-auto')
  })

  it('puts the panel right after the button so tabbing on enters it', async () => {
    await render()
    await open()
    expect(panel()!.previousElementSibling!.contains(chip())).toBe(true)
    expect(chip().compareDocumentPosition(panel()!) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
  })

  it('closes on Escape, returns focus to the button, and keeps the key from anything behind', async () => {
    const behind = vi.fn()
    window.addEventListener('keydown', behind)
    try {
      await render()
      await open()
      chip().focus()
      await press('Escape')
      expect(chip().getAttribute('aria-expanded')).toBe('false')
      expect(document.activeElement).toBe(chip())
      expect(behind).not.toHaveBeenCalled()
    } finally {
      window.removeEventListener('keydown', behind)
    }
  })

  it('leaves Escape alone while closed', async () => {
    const behind = vi.fn()
    window.addEventListener('keydown', behind)
    try {
      await render()
      await press('Escape')
      expect(behind).toHaveBeenCalledTimes(1)
    } finally {
      window.removeEventListener('keydown', behind)
    }
  })

  it('closes when a press lands outside, but not inside', async () => {
    await render()
    await open()
    await act(async () => { panel()!.dispatchEvent(new Event('pointerdown', { bubbles: true })) })
    expect(chip().getAttribute('aria-expanded')).toBe('true')
    await act(async () => {
      document.querySelector('[data-testid="outside"]')!.dispatchEvent(new Event('pointerdown', { bubbles: true }))
    })
    expect(chip().getAttribute('aria-expanded')).toBe('false')
  })

  it('closes when focus moves on to something else', async () => {
    await render()
    await open()
    await act(async () => { document.querySelector<HTMLButtonElement>('[data-testid="outside"]')!.focus() })
    expect(chip().getAttribute('aria-expanded')).toBe('false')
  })

  it('names the kinds of a large group only once asked', async () => {
    await render()
    await open()
    const toggle = [...panel()!.querySelectorAll<HTMLButtonElement>('button')].find(b => b.textContent?.startsWith('5 kinds'))!
    expect(toggle.getAttribute('aria-expanded')).toBe('false')
    const list = document.getElementById(toggle.getAttribute('aria-controls')!)!
    expect(list.textContent).toBe('Deployments, Jobs, CronJobs, Secrets, Services')
    expect(list.querySelector('[inert]')).not.toBeNull()
    await act(async () => { toggle.click() })
    expect(toggle.getAttribute('aria-expanded')).toBe('true')
    expect(list.querySelector('[inert]')).toBeNull()
  })

  it('does not offer a list for a small group', async () => {
    await render({ collection: { groups: [collection.groups[0]] } })
    await open()
    expect(panel()!.querySelectorAll('button')).toHaveLength(0)
  })

  it('says the list may be incomplete only when Radar says so', async () => {
    await render()
    await open()
    expect(panel()!.textContent).not.toContain('can\u2019t list every namespace')
    await open()
    await render({ collection: { ...collection, mayBeIncomplete: true } })
    await open()
    expect(panel()!.textContent).toContain('Radar can\u2019t list every namespace here, so it may collect in more than these.')
  })

  it('rounds the last segment of the scope pill itself, which it is when embedded there', async () => {
    await render({ variant: 'segment' })
    expect(chip().className).toContain('rounded-r-[7px]')
    expect(chip().className).not.toContain('border')
  })
})
