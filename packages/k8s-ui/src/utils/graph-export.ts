import { toCanvas } from 'html-to-image'

export type GraphImageFormat = 'image/png' | 'image/webp'
export type GraphVectorFormat = 'image/svg+xml' | 'application/vnd.jgraph.mxfile'
export type GraphExportFormat = GraphImageFormat | GraphVectorFormat
export type GraphCaptureMode = 'full' | 'viewport'

export interface GraphBounds {
  x: number
  y: number
  width: number
  height: number
}

export const GRAPH_IMAGE_FORMATS: readonly GraphImageFormat[] = ['image/webp', 'image/png']
export const GRAPH_VECTOR_FORMATS: readonly GraphVectorFormat[] = ['image/svg+xml', 'application/vnd.jgraph.mxfile']
export const GRAPH_FORMAT_LABELS: Record<GraphExportFormat, string> = {
  'image/png': 'PNG',
  'image/webp': 'WebP',
  'image/svg+xml': 'SVG',
  'application/vnd.jgraph.mxfile': 'draw.io',
}
export const GRAPH_FORMAT_EXT: Record<GraphExportFormat, string> = {
  'image/png': 'png',
  'image/webp': 'webp',
  'image/svg+xml': 'svg',
  'application/vnd.jgraph.mxfile': 'drawio',
}

export function isGraphImageFormat(format: GraphExportFormat): format is GraphImageFormat {
  return GRAPH_IMAGE_FORMATS.some(f => f === format)
}

const EXPORT_PADDING = 16
const EXPORT_TIMEOUT_MS = 30_000
// What the browser paints when no element sets a background.
const CANVAS_BG = '#ffffff'

export interface CaptureFlowImageOptions {
  /** The React Flow root element (`.react-flow`). */
  container: HTMLElement
  mode: GraphCaptureMode
  /** Bounds of all nodes in flow coordinates. Used by the full capture. */
  bounds: GraphBounds
  /** Pixel ratio for a capture of the visible area. */
  scale: number
  transparent: boolean
  format: GraphImageFormat
}

/** Output size in pixels. A full capture is drawn 1:1 with the graph bounds. */
export function graphExportSize(
  mode: GraphCaptureMode,
  container: HTMLElement,
  bounds: GraphBounds,
  scale: number,
): { width: number; height: number } {
  if (mode === 'viewport') {
    const { width, height } = container.getBoundingClientRect()
    return { width: Math.ceil(width) * scale, height: Math.ceil(height) * scale }
  }
  return {
    width: Math.ceil(bounds.width + EXPORT_PADDING * 2),
    height: Math.ceil(bounds.height + EXPORT_PADDING * 2),
  }
}

function isPainted(color: string): boolean {
  return !!color && color !== 'transparent' && !/^rgba\(.*,\s*0\)$/.test(color)
}

/** The color the graph is painted on: the React Flow root is usually
 *  transparent, so this is the first ancestor that sets a background. */
export function paintedBackground(el: HTMLElement): string {
  for (let node: HTMLElement | null = el; node; node = node.parentElement) {
    const bg = getComputedStyle(node).backgroundColor
    if (isPainted(bg)) return bg
  }
  return CANVAS_BG
}

/** True for a dark CSS color in rgb()/rgba() or #rrggbb form. */
export function isDarkColor(color: string): boolean {
  const rgb = /^rgba?\(\s*([\d.]+)[\s,]+([\d.]+)[\s,]+([\d.]+)/.exec(color)
  const hex = /^#([0-9a-f]{2})([0-9a-f]{2})([0-9a-f]{2})/i.exec(color)
  const parts = rgb ? rgb.slice(1, 4).map(Number) : hex ? hex.slice(1, 4).map(h => parseInt(h, 16)) : null
  if (!parts) return false
  const [r, g, b] = parts
  return 0.2126 * r + 0.7152 * g + 0.0722 * b < 128
}

function withTimeout<T>(promise: Promise<T>, ms: number, msg: string): Promise<T> {
  let timer: ReturnType<typeof setTimeout> | undefined
  return Promise.race([
    promise,
    new Promise<never>((_, reject) => { timer = setTimeout(() => reject(new Error(msg)), ms) }),
  ]).finally(() => clearTimeout(timer))
}

/** Captures a React Flow graph as an image Blob. */
export async function captureFlowImage(opts: CaptureFlowImageOptions): Promise<Blob> {
  const { container, mode, bounds, scale, transparent, format } = opts
  const viewport = container.querySelector<HTMLElement>('.react-flow__viewport')
  if (!viewport) throw new Error("Couldn't find the graph to capture.")

  const backgroundColor = transparent ? 'transparent' : paintedBackground(container)
  const { width, height } = graphExportSize(mode, container, bounds, 1)
  const options = mode === 'full'
    ? {
        backgroundColor,
        width,
        height,
        pixelRatio: 1,
        skipFonts: true,
        style: {
          width: `${width}px`,
          height: `${height}px`,
          transform: `translate(${-bounds.x + EXPORT_PADDING}px, ${-bounds.y + EXPORT_PADDING}px) scale(1)`,
        },
      }
    : { backgroundColor, width, height, pixelRatio: scale, skipFonts: true }
  const timeoutMsg = mode === 'full'
    ? 'Export timed out. Try exporting the visible area instead.'
    : 'Export timed out.'

  const canvas = await withTimeout(toCanvas(viewport, options), EXPORT_TIMEOUT_MS, timeoutMsg)
  // WebP: lossless when transparent to avoid alpha artifacts. PNG ignores quality.
  const quality = format === 'image/webp' ? (transparent ? 1.0 : 0.92) : undefined
  const blob = await new Promise<Blob | null>((resolve) => canvas.toBlob(resolve, format, quality))
  if (!blob) throw new Error("Couldn't create the image. It may be too large; try the visible area or a lower quality.")
  return blob
}
