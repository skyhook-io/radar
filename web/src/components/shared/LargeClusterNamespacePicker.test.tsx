// @vitest-environment jsdom
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { LargeClusterNamespacePicker } from './LargeClusterNamespacePicker'

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

describe('LargeClusterNamespacePicker', () => {
  it("says the namespace list couldn't be read instead of loading forever", async () => {
    await act(async () => root.render(<LargeClusterNamespacePicker namespaces={undefined} error="Radar couldn't list namespaces; retry shortly" onSelect={() => {}} />))
    expect(document.body.textContent).toContain("Radar couldn't list namespaces; retry shortly")
    expect(document.body.textContent).not.toContain('Loading namespaces')
  })
})
