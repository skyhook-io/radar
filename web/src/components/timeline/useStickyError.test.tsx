// @vitest-environment jsdom
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { useStickyError } from './useStickyError'

const seen: (Error | null)[] = []
function Harness({ error, hasData }: { error: Error | null; hasData: boolean }) {
  seen.push(useStickyError(error, hasData))
  return null
}

let root: Root
let container: HTMLDivElement
beforeEach(() => {
  seen.length = 0
  container = document.createElement('div')
  root = createRoot(container)
})
afterEach(() => act(() => root.unmount()))
const render = (error: Error | null, hasData: boolean) => act(() => root.render(<Harness error={error} hasData={hasData} />))
const last = () => seen[seen.length - 1]

describe('useStickyError', () => {
  it('keeps the error while a refetch with no data clears it', () => {
    const down = new Error('database down')
    render(down, false)
    expect(last()).toBe(down)
    render(null, false)
    expect(last()).toBe(down)
  })

  it('drops the error once data arrives', () => {
    render(new Error('database down'), false)
    render(null, true)
    expect(last()).toBeNull()
  })

  // A page with data on screen whose poll starts failing: the hook must settle,
  // not bounce between the error and null until React gives up.
  it('settles when an error arrives while data is on screen', () => {
    render(null, true)
    render(new Error('database down'), true)
    expect(last()).toBeNull()
    expect(seen.length).toBeLessThan(10)
  })
})
