import { toCanvas } from 'html-to-image'

export type GraphImageFormat = 'image/png' | 'image/webp'
export type GraphCaptureMode = 'full' | 'viewport'

export interface GraphBounds {
  x: number
  y: number
  width: number
  height: number
}

export const GRAPH_IMAGE_FORMATS: readonly GraphImageFormat[] = ['image/webp', 'image/png']
export const GRAPH_FORMAT_LABELS: Record<GraphImageFormat, string> = { 'image/png': 'PNG', 'image/webp': 'WebP' }
export const GRAPH_FORMAT_EXT: Record<GraphImageFormat, string> = { 'image/png': 'png', 'image/webp': 'webp' }

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
