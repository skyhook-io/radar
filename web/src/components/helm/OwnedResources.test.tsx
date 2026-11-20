// @vitest-environment jsdom
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, describe, expect, it, vi } from 'vitest'
import type { HelmOwnedResource } from '../../types'
import { OwnedResources } from './OwnedResources'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

let root: Root | undefined

afterEach(() => {
  act(() => root?.unmount())
  root = undefined
  document.body.innerHTML = ''
})

function mount(resources: HelmOwnedResource[], onNavigate = vi.fn()) {
  const container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
  act(() => root!.render(<OwnedResources resources={resources} onNavigate={onNavigate} />))
  return { container, onNavigate }
}

function row(container: HTMLElement, name: string): HTMLElement {
  const label = Array.from(container.querySelectorAll('span')).find((el) => el.textContent === name)
  if (!label?.parentElement) throw new Error(`no row for ${name}`)
  return label.parentElement
}

const readable: HelmOwnedResource = { kind: 'ConfigMap', apiVersion: 'v1', name: 'settings', namespace: 'team-a', status: 'Active' }

describe('OwnedResources rows whose status the caller is not shown', () => {
  it('marks a denied object and does not open it', () => {
    const { container, onNavigate } = mount([
      readable,
      { kind: 'Deployment', apiVersion: 'apps/v1', name: 'worker', namespace: 'shared', statusUnread: 'user_namespace_not_visible' },
    ])
    expect(row(container, 'worker').parentElement?.textContent).toContain('No access')
    act(() => row(container, 'worker').click())
    expect(onNavigate).not.toHaveBeenCalled()
    act(() => row(container, 'settings').click())
    expect(onNavigate).toHaveBeenCalledTimes(1)
  })

  it('does not call an unanswered check a denial', () => {
    const { container } = mount([
      { kind: 'Deployment', apiVersion: 'apps/v1', name: 'worker', namespace: 'shared', statusUnread: 'user_permission_check_failed' },
    ])
    const text = row(container, 'worker').parentElement?.textContent ?? ''
    expect(text).not.toContain('No access')
    expect(text).toContain('Not checked')
    expect(text).not.toContain('Unknown')
  })

  it('does not count a withheld object as healthy or failed', () => {
    const { container } = mount([
      readable,
      { kind: 'Deployment', apiVersion: 'apps/v1', name: 'worker', namespace: 'shared', statusUnread: 'user_forbidden' },
    ])
    const summary = container.querySelector('.p-4 > div')?.textContent ?? ''
    expect(summary).toContain('1 healthy')
    expect(summary).not.toContain('failed')
  })

  it('counts withheld objects as not checked beside the healthy ones, and filters to them', () => {
    const { container } = mount([
      readable,
      { kind: 'ConfigMap', apiVersion: 'v1', name: 'flags', namespace: 'team-a', status: 'Active' },
      { kind: 'Deployment', apiVersion: 'apps/v1', name: 'worker', namespace: 'shared', statusUnread: 'user_namespace_not_visible' },
    ])
    const summary = container.querySelector('.p-4 > div')
    expect(summary?.textContent).toContain('2 healthy')
    const notChecked = Array.from(summary?.querySelectorAll('button') ?? []).find((b) => b.textContent === '1 not checked')
    expect(notChecked).toBeDefined()
    act(() => notChecked!.click())
    expect(container.textContent).toContain('worker')
    expect(container.textContent).not.toContain('settings')
  })

  it('shows no not-checked count when every object was read', () => {
    const { container } = mount([readable])
    expect(container.textContent).not.toContain('not checked')
  })
})
