// @vitest-environment jsdom
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ToastProvider } from '../ui/Toast'

const { captureFlowImage, graphDom } = vi.hoisted(() => ({
  captureFlowImage: vi.fn(),
  graphDom: { current: null as HTMLDivElement | null },
}))
vi.mock('../../utils/graph-export', async (importOriginal) => ({
  ...(await importOriginal<typeof import('../../utils/graph-export')>()),
  captureFlowImage,
}))
vi.mock('@xyflow/react', () => ({
  useStore: (selector: (s: { domNode: HTMLDivElement | null }) => unknown) => selector({ domNode: graphDom.current }),
  useReactFlow: () => ({
    getNodes: () => [{ id: 'a', type: 'k8sResource', position: { x: 0, y: 0 }, data: { kind: 'Pod', name: 'web' }, measured: { width: 100, height: 50 } }],
    getNodesBounds: () => ({ x: 0, y: 0, width: 100, height: 50 }),
    getEdges: () => [],
    getInternalNode: () => undefined,
    getViewport: () => ({ x: 0, y: 0, zoom: 1 }),
  }),
}))

import { GraphExportButton } from './GraphExportButton'
import { GRAPH_FORMAT_LABELS, type GraphExportFormat } from '../../utils/graph-export'
import type { GraphNodeMapper } from '../../utils/graph-model'

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })

let root: Root
let element: HTMLDivElement
let anchorClicks: number
const image = new Blob(['img'], { type: 'image/webp' })

beforeEach(() => {
  element = document.createElement('div')
  document.body.appendChild(element)
  graphDom.current = document.createElement('div')
  root = createRoot(element)
  anchorClicks = 0
  captureFlowImage.mockReset()
  captureFlowImage.mockResolvedValue(image)
  vi.stubGlobal('URL', Object.assign(URL, { createObjectURL: vi.fn(() => 'blob:test'), revokeObjectURL: vi.fn() }))
  vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => { anchorClicks++ })
})
afterEach(async () => {
  await act(async () => root.unmount())
  element.remove()
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
})

async function exportOnce(onDownloadBlob?: (blob: Blob, filename: string) => void) {
  await act(async () => root.render(
    <ToastProvider>
      <GraphExportButton defaultFilename="traffic" title="Export traffic" onDownloadBlob={onDownloadBlob} />
    </ToastProvider>,
  ))
  await act(async () => element.querySelector<HTMLButtonElement>('button[aria-label="Export graph"]')!.click())
  const exportButton = [...document.querySelectorAll('button')].find((b) => b.textContent === 'Export')!
  await act(async () => {
    exportButton.click()
    await new Promise((resolve) => setTimeout(resolve, 100))
  })
}

it('passes the image to onDownloadBlob instead of a browser download', async () => {
  const onDownloadBlob = vi.fn()
  await exportOnce(onDownloadBlob)

  expect(captureFlowImage).toHaveBeenCalledWith(expect.objectContaining({ container: graphDom.current, format: 'image/webp' }))
  expect(onDownloadBlob).toHaveBeenCalledWith(image, expect.stringMatching(/^traffic-.*\.webp$/))
  expect(anchorClicks).toBe(0)
})

it('downloads through the browser when no onDownloadBlob is given', async () => {
  await exportOnce()

  expect(anchorClicks).toBe(1)
  expect(document.body.textContent).toMatch(/Exported/)
})

describe('keyboard', () => {
  const dialog = (title: string) => document.querySelector<HTMLElement>(`[role="dialog"][aria-label="${title}"]`)
  const press = async (target: Element, key: string) => {
    await act(async () => {
      target.dispatchEvent(new KeyboardEvent('keydown', { key, bubbles: true, cancelable: true }))
      await new Promise((resolve) => setTimeout(resolve, 100))
    })
  }

  // Two graphs on one page, both dialogs open.
  async function openTwo(onDownloadBlob: (blob: Blob, filename: string) => void) {
    await act(async () => root.render(
      <ToastProvider>
        <GraphExportButton defaultFilename="a" title="Export A" onDownloadBlob={onDownloadBlob} />
        <GraphExportButton defaultFilename="b" title="Export B" onDownloadBlob={onDownloadBlob} />
      </ToastProvider>,
    ))
    for (const button of element.querySelectorAll<HTMLButtonElement>('button[aria-label="Export graph"]')) {
      await act(async () => button.click())
    }
  }

  it('Enter in the filename exports from that dialog only', async () => {
    const onDownloadBlob = vi.fn()
    await openTwo(onDownloadBlob)

    await press(dialog('Export A')!.querySelector('input')!, 'Enter')

    expect(onDownloadBlob).toHaveBeenCalledTimes(1)
    expect(onDownloadBlob).toHaveBeenCalledWith(image, expect.stringMatching(/^a-/))
    expect(dialog('Export B')).not.toBeNull()
  })

  it('Enter on a button does not export', async () => {
    const onDownloadBlob = vi.fn()
    await openTwo(onDownloadBlob)
    const cancel = [...dialog('Export A')!.querySelectorAll('button')].find((b) => b.textContent === 'Cancel')!

    await press(cancel, 'Enter')

    expect(captureFlowImage).not.toHaveBeenCalled()
  })

  it('Escape closes only the dialog it was pressed in', async () => {
    await openTwo(vi.fn())

    await press(dialog('Export A')!.querySelector('input')!, 'Escape')

    expect(dialog('Export A')).toBeNull()
    expect(dialog('Export B')).not.toBeNull()
  })
})

describe('formats', () => {
  const describeNode: GraphNodeMapper = ({ data }) => ({ shape: 'resource', kind: String(data.kind), name: String(data.name) })
  const formatButton = (format: GraphExportFormat) =>
    [...document.querySelectorAll<HTMLButtonElement>('[role="dialog"] button[aria-pressed]')].find(b => b.textContent === GRAPH_FORMAT_LABELS[format])
  const modeButtons = () => [...document.querySelectorAll<HTMLButtonElement>('[role="dialog"] button:not([aria-pressed])')]

  async function open(props: { describeNode?: GraphNodeMapper; onDownloadBlob?: (blob: Blob, filename: string) => void }) {
    await act(async () => root.render(
      <ToastProvider>
        <GraphExportButton defaultFilename="topology" title="Export topology" {...props} />
      </ToastProvider>,
    ))
    await act(async () => element.querySelector<HTMLButtonElement>('button[aria-label="Export graph"]')!.click())
  }
  const click = async (button: HTMLButtonElement | undefined) => {
    await act(async () => {
      button!.click()
      await new Promise((resolve) => setTimeout(resolve, 100))
    })
  }

  it('offers SVG and draw.io only when the graph describes its nodes', async () => {
    await open({})
    expect(formatButton('image/svg+xml')).toBeUndefined()
    expect(formatButton('application/vnd.jgraph.mxfile')).toBeUndefined()

    await act(async () => root.unmount())
    root = createRoot(element)
    await open({ describeNode })
    expect(formatButton('image/svg+xml')).toBeDefined()
    expect(formatButton('application/vnd.jgraph.mxfile')).toBeDefined()
  })

  it('hides image quality for vector formats and the background option for draw.io', async () => {
    await open({ describeNode })
    // The second capture mode is the visible area, where image quality applies.
    await click(modeButtons()[1])
    expect(document.querySelector('[role="dialog"] select')).not.toBeNull()

    await click(formatButton('image/svg+xml'))
    expect(document.querySelector('[role="dialog"] select')).toBeNull()
    expect(document.querySelector('[role="dialog"] input[type="checkbox"]')).not.toBeNull()

    await click(formatButton('application/vnd.jgraph.mxfile'))
    expect(document.querySelector('[role="dialog"] select')).toBeNull()
    expect(document.querySelector('[role="dialog"] input[type="checkbox"]')).toBeNull()
  })

  it('hides the visible area for draw.io and keeps the pick for other formats', async () => {
    await open({ describeNode })
    const visibleArea = () => [...document.querySelectorAll('[role="dialog"] button')].find((b) => b.textContent === 'Visible area')
    await click(visibleArea() as HTMLButtonElement)

    await click(formatButton('application/vnd.jgraph.mxfile'))
    expect(visibleArea()).toBeUndefined()

    await click(formatButton('image/svg+xml'))
    expect(visibleArea()?.className).toContain('btn-brand')
  })

  it.each([
    ['image/svg+xml', 'svg', '<svg'],
    ['application/vnd.jgraph.mxfile', 'drawio', '<mxfile'],
  ] as const)('writes %s from the graph without capturing pixels', async (format, ext, root) => {
    const onDownloadBlob = vi.fn()
    await open({ describeNode, onDownloadBlob })
    await click(formatButton(format))
    await click([...document.querySelectorAll('button')].find((b) => b.textContent === 'Export'))

    expect(captureFlowImage).not.toHaveBeenCalled()
    const [blob, filename] = onDownloadBlob.mock.calls[0] as [Blob, string]
    expect(filename).toMatch(new RegExp(`^topology-.*\\.${ext}$`))
    expect(blob.type).toBe(format)
    const content = await blob.text()
    expect(content).toContain(root)
    expect(content).toContain('web')
  })
})
