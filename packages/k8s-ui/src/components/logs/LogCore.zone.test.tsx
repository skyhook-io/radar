// @vitest-environment jsdom
//
// A local timestamp format names the reader's zone, so a log time is never read
// in the wrong one.
import { act, type ReactNode } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { LogCore } from './LogCore'

vi.mock('react-virtuoso', () => ({ Virtuoso: ({ data, itemContent }: { data: unknown[]; itemContent: (index: number, value: unknown) => ReactNode }) => <div>{data.map((value, index) => <div key={index}>{itemContent(index, value)}</div>)}</div> }))

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })

let root: Root
let element: HTMLDivElement
beforeEach(() => {
  element = document.createElement('div')
  document.body.appendChild(element)
  root = createRoot(element)
})
afterEach(async () => {
  await act(async () => root.unmount())
  element.remove()
})

it('names the zone beside the local time format', async () => {
  const noop = () => {}
  await act(async () => root.render(<LogCore entries={[]} isLoading={false} isStreaming={false} onStopStream={noop} onRefresh={noop} onDownload={noop} />))
  const zone = Intl.DateTimeFormat().resolvedOptions().timeZone
  expect(element.querySelector('button[aria-label="Pick timestamp format"]')?.textContent).toContain(`Local time (${zone})`)
})
