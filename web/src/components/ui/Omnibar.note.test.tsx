// @vitest-environment jsdom
import { act, createRef } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { Omnibar, type OmnibarHandle, type OmnibarNote } from './Omnibar'
import { searchCoverageNote } from './searchCoverageNote'
import { coverageOf } from './searchCoverage.fixtures'

vi.mock('../../api/usage-data', () => ({ recordUsageEvent: vi.fn() }))

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })
let root: Root
let host: HTMLDivElement

beforeEach(() => {
  vi.useFakeTimers()
  // jsdom has no layout; the omnibar scrolls the selected row into view.
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

function render(note: OmnibarNote | undefined, { isPlaceholderData = false, isFetching = false } = {}) {
  act(() => {
    root.render(<Omnibar onOpenResource={vi.fn()} commandItems={[]} searchData={{ hits: [] }} note={note} isPlaceholderData={isPlaceholderData} isFetching={isFetching} />)
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

const findButton = (label: string) => [...document.body.querySelectorAll('button')].find((b) => b.textContent === label)

describe('Omnibar note', () => {
  const recoveryText = "Couldn't read Certificate; matches of that kind may be missing. Search kind:Certificate to re-check."
  const namespace = searchCoverageNote(coverageOf([{ kind: '*', group: '', reason: 'radar_namespace_not_collected', namespaces: ['rel-owner'] }]), {})!
  const loading = searchCoverageNote(coverageOf([{ kind: '*', group: '', reason: 'kind_sync_pending' }]), {})!

  it.each([
    ['alone', [], []],
    ['before a namespace note', [], [namespace]],
    ['after a namespace note', [namespace], []],
    ['before a loading note', [], [loading]],
    ['after a loading note', [loading], []],
    ['between loading and namespace notes', [loading], [namespace]],
    ['between namespace and loading notes', [namespace], [loading]],
  ] as [string, OmnibarNote[], OmnibarNote[]][])(
    'renders the recovery action inside its sentence %s and adds its modifier', (_, before, after) => {
      const ref = createRef<OmnibarHandle>()
      const onQueryChange = vi.fn()
      const recovery = searchCoverageNote(coverageOf([{ kind: 'Certificate', group: 'cert-manager.io', reason: 'read_error' }]), {
        recheckKind: (kind) => ref.current?.addModifier({ key: 'kind', value: kind }),
      })!
      const parts = [...before, recovery, ...after]
      const expected = [...before.map((part) => part.text), recoveryText, ...after.map((part) => part.text)].join(' ')
      act(() => {
        root.render(<Omnibar ref={ref} onOpenResource={vi.fn()} commandItems={[]} searchData={{ hits: [] }} onQueryChange={onQueryChange} note={parts.length === 1 ? recovery : { text: expected, parts }} />)
      })
      typeQuery('ns:prod')
      act(() => host.querySelector('input')!.dispatchEvent(new KeyboardEvent('keydown', { key: ' ', bubbles: true })))
      typeQuery('renewal')
      const button = findButton('kind:Certificate')!
      expect(button).toBeDefined()
      expect(button.parentElement!.textContent?.trim()).toBe(recoveryText)
      expect(button.parentElement!.parentElement!.textContent).toBe(expected)
      act(() => button.click())
      act(() => { vi.advanceTimersByTime(300) })
      expect(onQueryChange).toHaveBeenLastCalledWith('ns:prod kind:Certificate renewal', true)
      expect(host.querySelector('input')!.value).toContain('renewal')
    },
  )

  it('shows the host note under the empty results and runs its action on click, then closes', () => {
    const onSelect = vi.fn()
    render({ text: "Leases can't be searched.", action: { label: 'Open the Leases list instead.', onSelect } })
    typeQuery('lease')
    expect(document.body.textContent).toContain("Leases can't be searched.")

    const action = findButton('Open the Leases list instead.')!
    const mousedown = new MouseEvent('mousedown', { bubbles: true, cancelable: true })
    act(() => { action.dispatchEvent(mousedown) })
    expect(mousedown.defaultPrevented).toBe(true)
    expect(onSelect).not.toHaveBeenCalled()

    // Keyboard activation of a focused button dispatches click too.
    act(() => action.click())
    act(() => { vi.advanceTimersByTime(500) })
    expect(onSelect).toHaveBeenCalledOnce()
    expect(host.querySelector('input')!.value).toBe('')
    expect(document.body.textContent).not.toContain("Leases can't be searched.")
  })

  it('hides a note that belongs to a previous query', () => {
    render({ text: 'Still loading — results may change.' }, { isPlaceholderData: true })
    typeQuery('lease')
    expect(document.body.textContent).not.toContain('Still loading')
  })

  it('shows the note above the results, where a full page of hits cannot push it out of view', () => {
    const hits = Array.from({ length: 20 }, (_, i) => ({ score: 1, kind: 'Certificate', group: 'cert-manager.io', namespace: 'a', name: `lease-${i}` }))
    act(() => {
      root.render(<Omnibar onOpenResource={vi.fn()} commandItems={[]} searchData={{ hits }} note={{ text: "Couldn't read Certificate (networking.internal.knative.dev)." }} />)
    })
    typeQuery('lease')
    const text = document.body.textContent ?? ''
    expect(text.indexOf("Couldn't read Certificate")).toBeGreaterThan(-1)
    expect(text.indexOf("Couldn't read Certificate")).toBeLessThan(text.indexOf('lease-0'))
  })

  it('keeps the empty state and note steady while the same query re-fetches', () => {
    render({ text: 'Still loading — results may change.' }, { isFetching: true })
    typeQuery('lease')
    expect(document.body.textContent).toContain('No resources match')
    expect(document.body.textContent).toContain('Still loading — results may change.')
  })
})
