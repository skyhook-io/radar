// @vitest-environment jsdom
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { triggerBlobDownload, triggerDownload } from './download'

let clicked: HTMLAnchorElement[]
beforeEach(() => {
  clicked = []
  vi.stubGlobal('URL', Object.assign(URL, { createObjectURL: vi.fn(() => 'blob:test'), revokeObjectURL: vi.fn() }))
  vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(function (this: HTMLAnchorElement) { clicked.push(this) })
})
afterEach(() => {
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
})

it('triggerDownload downloads text content as a file with its MIME type', async () => {
  triggerDownload('a: 1', 'application/yaml', 'pod.yaml')

  expect(clicked).toHaveLength(1)
  expect(clicked[0].download).toBe('pod.yaml')
  const blob = vi.mocked(URL.createObjectURL).mock.calls[0][0] as Blob
  expect(blob.type).toBe('application/yaml')
  expect(await blob.text()).toBe('a: 1')
})

it('triggerDownload hands text to the override instead of downloading', () => {
  const override = vi.fn()
  triggerDownload('a: 1', 'application/yaml', 'pod.yaml', override)

  expect(override).toHaveBeenCalledWith('a: 1', 'application/yaml', 'pod.yaml')
  expect(clicked).toHaveLength(0)
})

it('triggerBlobDownload hands the blob to the override instead of downloading', () => {
  const override = vi.fn()
  const blob = new Blob(['x'], { type: 'image/png' })
  triggerBlobDownload(blob, 'graph.png', override)

  expect(override).toHaveBeenCalledWith(blob, 'graph.png')
  expect(clicked).toHaveLength(0)
})
