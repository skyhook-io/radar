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

function render(coverageNote?: string) {
  act(() => {
    root.render(<CompareResourcePicker open onClose={() => {}} source={source} candidates={[source]} coverageNote={coverageNote} onPick={() => {}} />)
  })
  return document.body.textContent ?? ''
}

describe('compare picker empty state', () => {
  it('says nothing else exists only when the list was complete', () => {
    expect(render()).toContain('No other configmaps available to compare against.')
  })

  it("doesn't claim nothing else exists after a qualified read", () => {
    const text = render('Partial results. Radar read read-a; your permissions exclude configmaps in read-b.')
    expect(text).toContain('No other configmaps found in what Radar could read.')
    expect(text).not.toContain('available to compare against')
  })
})
