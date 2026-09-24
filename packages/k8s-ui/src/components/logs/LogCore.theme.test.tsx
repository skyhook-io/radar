// @vitest-environment jsdom
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { LogCore } from './LogCore'

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })

let root: Root
let element: HTMLDivElement
beforeEach(() => {
  localStorage.clear()
  element = document.createElement('div')
  document.body.appendChild(element)
  root = createRoot(element)
})
afterEach(async () => {
  await act(async () => root.unmount())
  element.remove()
})

const noop = () => {}
async function render(props: { forceDark?: boolean; defaultDark?: boolean }) {
  await act(async () =>
    root.render(
      <LogCore
        entries={[]}
        isLoading={false}
        isStreaming={false}
        onStopStream={noop}
        onRefresh={noop}
        onDownload={noop}
        {...props}
      />,
    ),
  )
}
const toggle = () => element.querySelector<HTMLButtonElement>('button[aria-label^="Switch log viewer"]')

describe('LogCore theme', () => {
  it('defaults to dark when no theme hint is given', async () => {
    await render({})
    expect(toggle()?.getAttribute('aria-label')).toBe('Switch log viewer to light mode')
  })

  it('follows defaultDark and keeps the toggle available', async () => {
    await render({ defaultDark: false })
    expect(toggle()?.getAttribute('aria-label')).toBe('Switch log viewer to dark mode')
    await render({ defaultDark: true })
    expect(toggle()?.getAttribute('aria-label')).toBe('Switch log viewer to light mode')
  })

  it('prefers the saved toggle choice over defaultDark', async () => {
    localStorage.setItem('radar-logs-dark', 'true')
    await render({ defaultDark: false })
    expect(toggle()?.getAttribute('aria-label')).toBe('Switch log viewer to light mode')
  })

  it('hides the toggle when forceDark is set', async () => {
    await render({ forceDark: true, defaultDark: false })
    expect(toggle()).toBeNull()
  })
})
