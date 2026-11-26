// @vitest-environment jsdom
import { beforeEach, describe, expect, it, vi } from 'vitest'

const { toCanvas } = vi.hoisted(() => ({ toCanvas: vi.fn() }))
vi.mock('html-to-image', () => ({ toCanvas }))

import { captureFlowImage, paintedBackground } from './graph-export'

function flowContainer(): HTMLElement {
  const container = document.createElement('div')
  container.className = 'react-flow'
  const viewport = document.createElement('div')
  viewport.className = 'react-flow__viewport'
  container.appendChild(viewport)
  return container
}

const bounds = { x: 100, y: 50, width: 400, height: 200 }

beforeEach(() => {
  toCanvas.mockReset()
  toCanvas.mockResolvedValue({
    toBlob: (cb: (b: Blob | null) => void, type: string) => cb(new Blob(['pixels'], { type })),
  })
})

describe('captureFlowImage', () => {
  it.each(['image/png', 'image/webp'] as const)('encodes %s from the canvas', async (format) => {
    const blob = await captureFlowImage({ container: flowContainer(), mode: 'viewport', bounds, scale: 3, transparent: false, format })

    expect(blob.type).toBe(format)
    expect(toCanvas.mock.calls[0][1]).toMatchObject({ pixelRatio: 3 })
  })

  it('frames the entire graph around the node bounds with padding', async () => {
    await captureFlowImage({ container: flowContainer(), mode: 'full', bounds, scale: 2, transparent: true, format: 'image/png' })

    expect(toCanvas.mock.calls[0][1]).toMatchObject({
      width: 432,
      height: 232,
      pixelRatio: 1,
      backgroundColor: 'transparent',
      style: { transform: 'translate(-84px, -34px) scale(1)' },
    })
  })

  it('fails when the canvas cannot be encoded', async () => {
    toCanvas.mockResolvedValue({ toBlob: (cb: (b: Blob | null) => void) => cb(null) })

    await expect(captureFlowImage({ container: flowContainer(), mode: 'full', bounds, scale: 1, transparent: false, format: 'image/png' }))
      .rejects.toThrow(/Couldn't create the image/)
  })
})

describe('paintedBackground', () => {
  it('uses the first ancestor that paints a background', () => {
    const pane = document.createElement('div')
    pane.style.backgroundColor = 'rgb(248, 250, 252)'
    const wrapper = document.createElement('div')
    wrapper.style.backgroundColor = 'rgba(0, 0, 0, 0)'
    const container = flowContainer()
    pane.appendChild(wrapper)
    wrapper.appendChild(container)
    document.body.appendChild(pane)

    expect(paintedBackground(container)).toBe('rgb(248, 250, 252)')
    pane.remove()
  })

  it('uses the browser canvas color when nothing paints one', () => {
    const container = flowContainer()
    document.body.appendChild(container)

    expect(paintedBackground(container)).toBe('#ffffff')
    container.remove()
  })
})
