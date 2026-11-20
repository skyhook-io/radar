// @vitest-environment jsdom
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { Omnibar } from './Omnibar'
import type { SearchHit } from '../../api/client'

vi.mock('../../api/usage-data', () => ({ recordUsageEvent: vi.fn() }))

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })
let root: Root
let host: HTMLDivElement

beforeEach(() => {
  vi.useFakeTimers()
  Element.prototype.scrollIntoView = vi.fn()
  host = document.createElement('div')
  document.body.appendChild(host)
  root = createRoot(host)
})

afterEach(() => {
  act(() => root.unmount())
  host.remove()
  document.body.innerHTML = ''
  vi.useRealTimers()
})

function render(onInvestigateQuery: ((query: { text: string; namespaces: string[] }) => void) | undefined, hits: SearchHit[] = []) {
  act(() => {
    root.render(<Omnibar onOpenResource={vi.fn()} commandItems={[]} searchData={{ hits }} onInvestigateQuery={onInvestigateQuery} />)
  })
}

function typeQuery(text: string) {
  const input = host.querySelector('input')!
  act(() => input.focus())
  act(() => {
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')!.set!.call(input, text)
    input.dispatchEvent(new Event('input', { bubbles: true }))
  })
  act(() => { vi.advanceTimersByTime(200) })
}

const investigateButton = () =>
  [...document.body.querySelectorAll('button')].find((b) => b.textContent?.startsWith('Investigate “'))

describe('Omnibar investigate row', () => {
  it('hands the typed words to the host as a question', () => {
    const onInvestigate = vi.fn()
    render(onInvestigate)
    typeQuery('checkout returns 502s')
    const row = investigateButton()
    expect(row?.textContent).toContain('Investigate “checkout returns 502s”')
    act(() => { row!.click() })
    expect(onInvestigate).toHaveBeenCalledWith({ text: 'checkout returns 502s', namespaces: [] })
  })

  it('comes after a resource match', () => {
    render(vi.fn(), [{ score: 1, kind: 'Deployment', group: 'apps', namespace: 'shop', name: 'checkout' }])
    typeQuery('checkout')
    const rows = [...document.body.querySelectorAll('[data-selected]')]
    expect(rows.at(-1)?.textContent).toContain('Investigate “checkout”')
    expect(rows[0]?.textContent).not.toContain('Investigate')
  })

  it('waits for search results before Enter can ask', () => {
    const onInvestigate = vi.fn()
    act(() => {
      root.render(<Omnibar onOpenResource={vi.fn()} commandItems={[]} searchData={{ hits: [] }} isFetching onInvestigateQuery={onInvestigate} />)
    })
    typeQuery('checkout')
    act(() => {
      host.querySelector('input')!.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true, cancelable: true }))
    })
    expect(onInvestigate).not.toHaveBeenCalled()
  })

  it('is absent when the host has no question entry', () => {
    render(undefined)
    typeQuery('checkout returns 502s')
    expect(investigateButton()).toBeUndefined()
  })

  it('carries ns: modifiers as where to start', () => {
    const onInvestigate = vi.fn()
    act(() => {
      root.render(<Omnibar onOpenResource={vi.fn()} commandItems={[]} searchData={{ hits: [] }} seedNamespaces={['payments']} onInvestigateQuery={onInvestigate} />)
    })
    typeQuery('slow checkout')
    expect(investigateButton()?.textContent).toContain('as a question · in payments')
    act(() => { investigateButton()!.click() })
    expect(onInvestigate).toHaveBeenCalledWith({ text: 'slow checkout', namespaces: ['payments'] })
  })

  it('counts a long namespace list instead of naming it', () => {
    const namespaces = Array.from({ length: 25 }, (_, i) => `ns-${i}`)
    act(() => {
      root.render(<Omnibar onOpenResource={vi.fn()} commandItems={[]} searchData={{ hits: [] }} seedNamespaces={namespaces} onInvestigateQuery={vi.fn()} />)
    })
    typeQuery('pods keep restarting')
    expect(investigateButton()?.textContent).toContain('Investigate “pods keep restarting”')
    expect(investigateButton()?.textContent).toContain('as a question · in 25 namespaces')
  })
})
