// @vitest-environment jsdom
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { ToastProvider } from '../components/ui/Toast'

const { isDesktopApp, desktopSaveBlob } = vi.hoisted(() => ({ isDesktopApp: vi.fn(), desktopSaveBlob: vi.fn() }))
vi.mock('../utils/desktop-download', () => ({ isDesktopApp, desktopSaveBlob, desktopSaveFile: vi.fn() }))
vi.mock('../utils/desktop-open-folder', () => ({ openFile: vi.fn(), openFolder: vi.fn() }))

import { useDesktopBlobDownload } from './useDesktopDownload'

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })

let root: Root
let element: HTMLDivElement
type Save = ReturnType<typeof useDesktopBlobDownload>
const seen = vi.fn<(save: Save) => void>()
function Probe() {
  seen(useDesktopBlobDownload())
  return null
}
const latest = (): Save => seen.mock.calls.at(-1)![0]

beforeEach(() => {
  element = document.createElement('div')
  document.body.appendChild(element)
  root = createRoot(element)
  desktopSaveBlob.mockReset()
  seen.mockReset()
})
afterEach(async () => {
  await act(async () => root.unmount())
  element.remove()
})

async function renderProbe() {
  await act(async () => root.render(<ToastProvider><Probe /></ToastProvider>))
}

it('leaves the browser download in place outside the desktop app', async () => {
  isDesktopApp.mockResolvedValue(false)
  await renderProbe()
  expect(latest()).toBeUndefined()
})

it('saves the blob through the native dialog in the desktop app', async () => {
  isDesktopApp.mockResolvedValue(true)
  desktopSaveBlob.mockResolvedValue('/tmp/topology.png')
  await renderProbe()
  const blob = new Blob(['img'], { type: 'image/png' })

  await act(async () => latest()!(blob, 'topology.png'))

  expect(desktopSaveBlob).toHaveBeenCalledWith(blob, 'topology.png')
  expect(document.body.textContent).toContain('File saved')
})
