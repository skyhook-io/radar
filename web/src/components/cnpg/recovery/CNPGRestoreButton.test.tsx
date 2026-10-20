// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { afterEach, expect, it, vi } from 'vitest'
import { CNPGRestoreButton } from './CNPGRestoreButton'

vi.mock('./CNPGRestoreDialog', () => ({ CNPGRestoreDialog: ({ entry }: any) => <div role="dialog">{entry.name}</div> }))
;(globalThis as any).IS_REACT_ACT_ENVIRONMENT = true
let root: ReturnType<typeof createRoot>
let host: HTMLDivElement
afterEach(() => { act(() => root?.unmount()); host?.remove() })

it('keeps the blocked entry focusable for its explanation and prevents starting restore', () => {
  host = document.createElement('div'); document.body.append(host); root = createRoot(host)
  act(() => root.render(<CNPGRestoreButton namespace="db" entry={{ kind: 'cluster', name: 'pg' }} disabledReason="Needs create clusters" />))
  const button = host.querySelector('button')!
  expect(button.disabled).toBe(false)
  expect(button.getAttribute('aria-disabled')).toBe('true')
  act(() => { button.focus(); button.click() })
  expect(document.activeElement).toBe(button)
  expect(host.querySelector('[role=dialog]')).toBeNull()
})

it('opens the selected source when restore is allowed', () => {
  host = document.createElement('div'); document.body.append(host); root = createRoot(host)
  act(() => root.render(<CNPGRestoreButton namespace="db" entry={{ kind: 'backup', name: 'base-backup' }} />))
  act(() => host.querySelector('button')!.click())
  expect(host.querySelector('[role=dialog]')?.textContent).toBe('base-backup')
})
