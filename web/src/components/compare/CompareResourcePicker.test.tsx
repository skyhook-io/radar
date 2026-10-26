// @vitest-environment jsdom
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { CompareResourcePicker, type CompareResourceRef } from '@skyhook-io/k8s-ui'

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })

const source: CompareResourceRef = { kind: 'configmaps', namespace: 'read-a', name: 'app' }
let root: Root
let element: HTMLDivElement

beforeEach(() => {
  element = document.createElement('div')
  document.body.appendChild(element)
  root = createRoot(element)
})

afterEach(() => {
  act(() => root.unmount())
  element.remove()
})

function render(notes: { coverageNote?: string; scopeNote?: string } = {}) {
  act(() => {
    root.render(<CompareResourcePicker open onClose={() => {}} source={source} candidates={[source]} {...notes} onPick={() => {}} />)
  })
  return document.body.textContent ?? ''
}

const note = (text: string) => [...document.querySelectorAll('[role="status"]')].find((el) => el.textContent === text)

describe('compare picker empty state', () => {
  it('says nothing else exists only when the list was complete', () => {
    expect(render()).toContain('No other configmaps available to compare against.')
  })

  it("doesn't claim nothing else exists after a qualified read", () => {
    const partial = 'Partial results. Radar read read-a; your permissions exclude configmaps in read-b.'
    const text = render({ coverageNote: partial })
    expect(text).toContain('No other configmaps found in what Radar could read.')
    expect(text).not.toContain('available to compare against')
    expect(note(partial)?.className).toContain('text-warning-text')
  })

  it('shows a collection-narrowed list as scope, not as a warning', () => {
    const scope = 'Radar collects this kind only in 1 namespace: read-a.'
    const text = render({ scopeNote: scope })
    expect(text).toContain('No other configmaps found in the collected namespaces.')
    expect(note(scope)?.className).not.toContain('text-warning-text')
  })

  it('lets the warning win when both notes are set', () => {
    const text = render({ coverageNote: 'Partial results.', scopeNote: 'Radar collects this kind only in 1 namespace: read-a.' })
    expect(text).toContain('Partial results.')
    expect(text).not.toContain('Radar collects this kind')
  })
})
