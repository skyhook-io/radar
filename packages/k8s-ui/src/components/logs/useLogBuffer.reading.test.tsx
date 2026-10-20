// @vitest-environment jsdom
import { act, StrictMode } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { useLogBuffer } from './useLogBuffer'

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })

let root: Root
let element: HTMLDivElement
let current: ReturnType<typeof useLogBuffer> | undefined
let flush: FrameRequestCallback | undefined
const buffer = () => {
  if (!current) throw new Error('Buffer not mounted')
  return current
}
function Harness() {
  current = useLogBuffer()
  return null
}
const lines = (count: number) => Array.from({ length: count }, (_, i) => ({ content: `INFO line ${i}`, container: 'app', timestamp: '' }))
async function append(count: number) {
  await act(async () => {
    buffer().appendBatch(lines(count))
    flush?.(0)
  })
}

beforeEach(async () => {
  vi.stubGlobal('requestAnimationFrame', (callback: FrameRequestCallback) => { flush = callback; return 1 })
  vi.stubGlobal('cancelAnimationFrame', () => { flush = undefined })
  element = document.createElement('div')
  root = createRoot(element)
  await act(async () => root.render(<StrictMode><Harness /></StrictMode>))
})
afterEach(async () => {
  await act(async () => root.unmount())
  current = undefined
  flush = undefined
  vi.unstubAllGlobals()
})

describe('reading a live log buffer', () => {
  it('appends below the reader without replacing existing records before rollover', async () => {
    await append(100)
    const anchor = buffer().entries[50]
    await act(async () => buffer().setFollowing(false))
    await append(100)
    expect(buffer().entries).toHaveLength(200)
    expect(buffer().entries[50]).toBe(anchor)
    expect(buffer().isFollowing).toBe(false)
    expect(buffer().pendingLineCount).toBe(0)
  })

  it('keeps the reading window through repeated eviction and resumes at the newest records', async () => {
    await append(10_000)
    await act(async () => buffer().setFollowing(false))
    const reading = buffer().entries
    await append(500)
    await append(20_000)
    expect(buffer().entries).toBe(reading)
    expect(buffer().entries[50]).toBe(reading[50])
    expect(buffer().pendingLineCount).toBe(20_500)
    expect(buffer().isFollowing).toBe(false)
    await act(async () => buffer().setFollowing(true))
    expect(buffer().entries).toHaveLength(10_000)
    expect(buffer().entries.at(-1)?.id).toBe(30_499)
    expect(buffer().pendingLineCount).toBe(0)
    expect(buffer().isFollowing).toBe(true)
  })

  it('still bounds the buffer while following', async () => {
    await append(10_500)
    expect(buffer().entries).toHaveLength(10_000)
    expect(buffer().entries[0].id).toBe(500)
    expect(buffer().isFollowing).toBe(true)
  })

  it('releases the saved history on refresh and clear', async () => {
    await append(10_000)
    await act(async () => buffer().setFollowing(false))
    await append(100)
    await act(async () => buffer().set(lines(5)))
    expect(buffer().entries).toHaveLength(5)
    expect(buffer().isFollowing).toBe(true)
    await act(async () => buffer().setFollowing(false))
    await act(async () => buffer().clear())
    expect(buffer().entries).toHaveLength(0)
    expect(buffer().pendingLineCount).toBe(0)
    expect(buffer().isFollowing).toBe(true)
  })
})

it('exports overlapping windows once and reports expired history after they separate', async () => {
  await append(10_000)
  await act(async () => buffer().setFollowing(false))
  await append(500)
  expect(buffer().retainedLineCount).toBe(10_500)
  expect(buffer().missingLineCount).toBe(0)
  expect(buffer().getRetainedEntries().map(entry => entry.id)).toEqual(Array.from({ length: 10_500 }, (_, i) => i))
  await append(10_000)
  const retained = buffer().getRetainedEntries()
  expect(buffer().retainedLineCount).toBe(20_000)
  expect(buffer().missingLineCount).toBe(500)
  expect(retained).toHaveLength(20_000)
  expect(retained[9_999].id).toBe(9_999)
  expect(retained[10_000].id).toBe(10_500)
  expect(retained.at(-1)?.id).toBe(20_499)
  await act(async () => buffer().setFollowing(true))
  expect(buffer().retainedLineCount).toBe(10_000)
  expect(buffer().missingLineCount).toBe(0)
  await act(async () => buffer().clear())
  expect(buffer().retainedLineCount).toBe(0)
  expect(buffer().getRetainedEntries()).toEqual([])
})

it('keeps collecting the latest logs when animation frames stop running', async () => {
  await act(async () => {
    buffer().appendBatch(lines(10_000))
  })
  // No animation frame was delivered, as happens in a hidden tab.
  expect(buffer().entries).toHaveLength(10_000)
  await act(async () => buffer().setFollowing(false))
  const saved = buffer().entries
  await act(async () => buffer().appendBatch(lines(30_000)))
  expect(buffer().entries).toBe(saved)
  expect(buffer().pendingLineCount).toBe(30_000)
  expect(buffer().getRetainedEntries().at(-1)?.id).toBe(39_999)
  expect(buffer().retainedLineCount).toBe(20_000)
  expect(buffer().missingLineCount).toBe(20_000)
  await act(async () => buffer().appendBatch(lines(5)))
  expect(buffer().pendingLineCount).toBe(30_000)
  await act(async () => flush?.(0))
  expect(buffer().pendingLineCount).toBe(30_005)
  expect(buffer().getRetainedEntries().at(-1)?.id).toBe(40_004)
  expect(buffer().missingLineCount).toBe(20_005)
})

it('does not count records from before an empty reset when pausing and receiving a large batch', async () => {
  await append(25_000)
  for (const reset of [() => buffer().clear(), () => buffer().set([])]) {
    await act(async () => { reset(); buffer().setFollowing(false) })
    await append(20_001)
    expect(buffer().entries).toHaveLength(10_000)
    expect(buffer().pendingLineCount).toBe(10_001)
    expect(buffer().missingLineCount).toBe(1)
    expect(buffer().retainedLineCount).toBe(20_000)
  }
})
