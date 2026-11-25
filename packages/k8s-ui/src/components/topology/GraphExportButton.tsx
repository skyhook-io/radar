import { useCallback, useLayoutEffect, useMemo, useRef, useState } from 'react'
import { createPortal, flushSync } from 'react-dom'
import { createRoot } from 'react-dom/client'
import { useReactFlow, useStore, type Edge, type InternalNode, type Node } from '@xyflow/react'
import { Download, Loader2, type LucideIcon } from 'lucide-react'
import { Input } from '../ui/Input'
import { Tooltip } from '../ui/Tooltip'
import { useToast } from '../ui/Toast'
import { triggerBlobDownload } from '../../utils/download'
import { formatBytes } from '../../utils/format'
import {
  captureFlowImage,
  graphExportSize,
  isDarkColor,
  isGraphImageFormat,
  paintedBackground,
  GRAPH_FORMAT_EXT,
  GRAPH_FORMAT_LABELS,
  GRAPH_IMAGE_FORMATS,
  GRAPH_VECTOR_FORMATS,
  type GraphCaptureMode,
  type GraphExportFormat,
  type GraphVectorFormat,
} from '../../utils/graph-export'
import { collectGraphModel, frameGraphModel, type FlowNodeInput, type GraphNodeMapper, type GraphRect } from '../../utils/graph-model'
import { writeDrawio } from '../../utils/graph-drawio'
import { writeSvg } from '../../utils/graph-svg'

export interface GraphExportButtonProps {
  /** Filename prefix, followed by a timestamp (e.g. "topology"). */
  defaultFilename: string
  /** Dialog heading. */
  title: string
  /** True while an entire-graph capture or an SVG / draw.io export runs. A graph
   *  that renders only visible elements must render all of them for that time. */
  onExportingChange?: (exporting: boolean) => void
  /** Saves the file instead of a browser download (the desktop app's native
   *  save dialog). It owns its own success and failure messages. */
  onDownloadBlob?: (blob: Blob, filename: string) => void
  /** Describes each node for the SVG and draw.io formats, which are offered only when it is set. */
  describeNode?: GraphNodeMapper
}

const DIALOG_WIDTH = 288

function renderIconMarkup(Icon: LucideIcon): string {
  const host = document.createElement('div')
  const root = createRoot(host)
  flushSync(() => root.render(<Icon />))
  const markup = host.querySelector('svg')?.innerHTML ?? ''
  root.unmount()
  return markup
}

function numericSize(value: unknown): number | undefined {
  return typeof value === 'number' && Number.isFinite(value) ? value : undefined
}

function flowNodeInput(node: Node, internal: InternalNode | undefined): FlowNodeInput {
  const position = internal?.internals.positionAbsolute ?? node.position
  return {
    id: node.id,
    type: node.type,
    parentId: node.parentId,
    hidden: node.hidden,
    data: node.data,
    x: position.x,
    y: position.y,
    width: internal?.measured.width ?? node.measured?.width ?? node.width ?? numericSize(node.style?.width) ?? 0,
    height: internal?.measured.height ?? node.measured?.height ?? node.height ?? numericSize(node.style?.height) ?? 0,
    handles: internal?.internals.handleBounds,
  }
}

function vectorBlob(
  format: GraphVectorFormat,
  nodes: FlowNodeInput[],
  edges: Edge[],
  describeNode: GraphNodeMapper,
  opts: { name: string; visible?: GraphRect; background: string | null; dark: boolean },
): Blob {
  const nothing = 'Nothing in the visible area to export. Move the graph into view or export the entire graph.'
  if (format === 'image/svg+xml') {
    // The SVG shows exactly the frame: lines crossing it are kept even when both ends are
    // outside, and the viewBox cuts everything at its edge.
    const full = collectGraphModel({ nodes, edges, describeNode, renderIcon: renderIconMarkup })
    const model = opts.visible ? frameGraphModel(full, opts.visible) : full
    if (model.boxes.length === 0 && model.lines.length === 0) throw new Error(nothing)
    return new Blob([writeSvg(model, { name: opts.name, theme: opts.dark ? 'dark' : 'light', background: opts.background, frame: opts.visible })], { type: format })
  }
  // draw.io is always the entire graph: a box cut at the screen edge can't be edited.
  const model = collectGraphModel({ nodes, edges, describeNode, renderIcon: renderIconMarkup })
  if (model.boxes.length === 0) throw new Error('This graph has nothing to export.')
  return new Blob([writeDrawio(model, { name: opts.name })], { type: format })
}

/** Export button and dialog: images, and SVG / draw.io when `describeNode` is set. Must render inside a React Flow graph. */
export function GraphExportButton({ defaultFilename, title, onExportingChange, onDownloadBlob, describeNode }: GraphExportButtonProps) {
  const [showDialog, setShowDialog] = useState(false)
  const [exporting, setExporting] = useState(false)
  const [filename, setFilename] = useState('')
  const [transparent, setTransparent] = useState(false)
  const [scale, setScale] = useState(2)
  const [captureMode, setCaptureMode] = useState<GraphCaptureMode>('full')
  const [format, setFormat] = useState<GraphExportFormat>('image/webp')
  const container = useStore((s) => s.domNode)
  const { getNodes, getNodesBounds, getEdges, getInternalNode, getViewport } = useReactFlow()
  const formats: readonly GraphExportFormat[] = describeNode ? [...GRAPH_IMAGE_FORMATS, ...GRAPH_VECTOR_FORMATS] : GRAPH_IMAGE_FORMATS
  const isImage = isGraphImageFormat(format)
  const hasVisibleArea = format !== 'application/vnd.jgraph.mxfile'
  // The picked mode is kept while draw.io is selected, so switching back restores it.
  const capture: GraphCaptureMode = hasVisibleArea ? captureMode : 'full'
  const { showError, showSuccess } = useToast()
  const inputRef = useRef<HTMLInputElement>(null)
  const buttonRef = useRef<HTMLButtonElement>(null)
  const panelRef = useRef<HTMLDivElement>(null)
  const [anchor, setAnchor] = useState<{ left: number; top: number; maxHeight: number } | null>(null)

  // The graph clips its children (overflow hidden) and sits under sibling
  // overlays such as a details panel, so the dialog is portaled to the body and
  // pinned beside the button: bottom-aligned with it, shifted down when the
  // window is too short, and scrolling when even that doesn't fit.
  useLayoutEffect(() => {
    if (!showDialog) return
    const place = () => {
      const r = buttonRef.current?.getBoundingClientRect()
      if (!r) return
      const margin = 8
      const panelW = panelRef.current?.offsetWidth ?? DIALOG_WIDTH
      const panelH = panelRef.current?.scrollHeight ?? 0
      const maxHeight = window.innerHeight - margin * 2
      const left = Math.max(margin, Math.min(r.right + margin, window.innerWidth - panelW - margin))
      const top = Math.max(margin, Math.min(r.bottom - panelH, window.innerHeight - margin - Math.min(panelH, maxHeight)))
      setAnchor({ left, top, maxHeight })
    }
    place()
    // Measure again after layout settles (fonts, the output-size line).
    const raf = requestAnimationFrame(place)
    // The button moves with the graph, e.g. when a bottom dock resizes it.
    const observer = typeof ResizeObserver === 'undefined' ? null : new ResizeObserver(place)
    if (container) observer?.observe(container)
    window.addEventListener('resize', place)
    window.addEventListener('scroll', place, true)
    return () => {
      cancelAnimationFrame(raf)
      observer?.disconnect()
      window.removeEventListener('resize', place)
      window.removeEventListener('scroll', place, true)
    }
  }, [showDialog, captureMode, format, container])

  const dims = useMemo(() => {
    if (!showDialog || !container || !isImage) return null
    const nodes = getNodes()
    if (nodes.length === 0) return null
    return graphExportSize(captureMode, container, getNodesBounds(nodes), scale)
  }, [showDialog, container, isImage, captureMode, scale, getNodes, getNodesBounds])

  const openDialog = useCallback((e: React.MouseEvent) => {
    e.stopPropagation()
    e.preventDefault()
    if (getNodes().length === 0) return
    setFilename(`${defaultFilename}-${new Date().toISOString().slice(0, 19).replace(/:/g, '-')}`)
    setShowDialog(true)
    setTimeout(() => inputRef.current?.select(), 50)
  }, [getNodes, defaultFilename])

  const doExport = useCallback(async () => {
    if (!container) return
    const nodes = getNodes()
    if (nodes.length === 0) return

    setExporting(true)

    // Vector formats read every node's measured size and handles, even for the visible area,
    // so off-screen nodes are rendered for them too.
    const isFullCapture = capture === 'full' || !isGraphImageFormat(format)
    if (isFullCapture && onExportingChange) {
      onExportingChange(true)
      // Wait for React to render all off-screen nodes
      await new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve)))
    }

    // Yield to let the UI paint the exporting state before heavy DOM work
    await new Promise(resolve => setTimeout(resolve, 50))

    try {
      const name = filename || defaultFilename
      let blob: Blob
      if (isGraphImageFormat(format)) {
        blob = await captureFlowImage({
          container,
          mode: capture,
          bounds: getNodesBounds(nodes),
          scale,
          transparent,
          format,
        })
      } else {
        if (!describeNode) throw new Error(`${GRAPH_FORMAT_LABELS[format]} export isn't available for this graph.`)
        const { x, y, zoom } = getViewport()
        const rect = container.getBoundingClientRect()
        const background = paintedBackground(container)
        blob = vectorBlob(format, getNodes().map(n => flowNodeInput(n, getInternalNode(n.id))), getEdges(), describeNode, {
          name,
          visible: capture === 'viewport' ? { x: -x / zoom, y: -y / zoom, width: rect.width / zoom, height: rect.height / zoom } : undefined,
          background: transparent ? null : background,
          dark: isDarkColor(background),
        })
      }
      triggerBlobDownload(blob, `${name}.${GRAPH_FORMAT_EXT[format]}`, onDownloadBlob)
      if (!onDownloadBlob) {
        showSuccess(`Exported ${GRAPH_FORMAT_LABELS[format]} (${formatBytes(blob.size)})`)
      }
    } catch (err) {
      console.error('Failed to export graph:', err)
      showError('Export failed', err instanceof Error ? err.message : String(err))
    } finally {
      setExporting(false)
      onExportingChange?.(false)
      setShowDialog(false)
    }
  }, [container, getNodes, getNodesBounds, getEdges, getInternalNode, getViewport, describeNode, transparent, scale, capture, format, filename, defaultFilename, onDownloadBlob, showError, showSuccess, onExportingChange])

  // On the dialog itself, so a second open dialog isn't affected and a focused
  // button keeps Enter for its own click.
  const handleDialogKey = useCallback((e: React.KeyboardEvent) => {
    if (e.key === 'Escape') {
      e.stopPropagation()
      setShowDialog(false)
    } else if (e.key === 'Enter' && !exporting && !(e.target instanceof HTMLButtonElement)) {
      e.preventDefault()
      e.stopPropagation()
      doExport()
    }
  }, [exporting, doExport])

  return (
    <>
      <Tooltip content="Export graph" delay={100} position="right">
        <button
          ref={buttonRef}
          className="react-flow__controls-button"
          onClick={openDialog}
          disabled={exporting}
          aria-label="Export graph"
        >
          {exporting ? <Loader2 className="w-3 h-3 animate-spin" /> : <Download className="w-3 h-3" />}
        </button>
      </Tooltip>
      {showDialog && createPortal(
        <div
          ref={panelRef}
          role="dialog"
          aria-label={title}
          style={{
            position: 'fixed',
            width: DIALOG_WIDTH,
            left: anchor?.left ?? 0,
            top: anchor?.top ?? 0,
            maxHeight: anchor?.maxHeight,
            visibility: anchor ? undefined : 'hidden',
          }}
          className="z-50 overflow-y-auto bg-theme-surface border border-theme-border rounded-lg shadow-2xl p-3"
          onClick={(e) => e.stopPropagation()}
          onKeyDown={handleDialogKey}
        >
          <div className="text-sm font-medium text-theme-text-primary mb-3">{title}</div>
          <label className="block text-xs text-theme-text-secondary mb-1">Filename</label>
          <Input
            ref={inputRef}
            value={filename}
            onChange={(e) => setFilename(e.target.value)}
            className="w-full px-2 py-1.5 text-sm bg-theme-base border border-theme-border rounded text-theme-text-primary outline-none focus:border-blue-500 mb-3"
          />
          {hasVisibleArea && (
            <>
              <label className="block text-xs text-theme-text-secondary mb-1">Capture</label>
              <div className="flex gap-1 mb-3">
                {(['full', 'viewport'] as const).map(mode => (
                  <button
                    key={mode}
                    onClick={() => setCaptureMode(mode)}
                    className={`flex-1 px-2 py-1.5 text-xs rounded transition-colors ${captureMode === mode ? 'btn-brand' : 'bg-theme-base text-theme-text-secondary hover:text-theme-text-primary border border-theme-border'}`}
                  >
                    {mode === 'full' ? 'Entire graph' : 'Visible area'}
                  </button>
                ))}
              </div>
            </>
          )}
          <label className="block text-xs text-theme-text-secondary mb-1">Format</label>
          <div className="flex gap-1 mb-3">
            {formats.map(f => (
              <button
                key={f}
                onClick={() => setFormat(f)}
                aria-pressed={format === f}
                className={`flex-1 px-1.5 py-1.5 text-xs rounded transition-colors ${format === f ? 'btn-brand' : 'bg-theme-base text-theme-text-secondary hover:text-theme-text-primary border border-theme-border'}`}
              >
                {GRAPH_FORMAT_LABELS[f]}
              </button>
            ))}
          </div>
          {isImage && captureMode === 'viewport' && (
            <div className="mb-2">
              <label className="block text-xs text-theme-text-secondary mb-1">Quality</label>
              <select
                value={scale}
                onChange={(e) => setScale(Number(e.target.value))}
                className="w-full px-2 py-1.5 text-sm bg-theme-base border border-theme-border rounded text-theme-text-primary outline-none focus:border-blue-500"
              >
                <option value={1}>Standard</option>
                <option value={2}>High (2x)</option>
                <option value={3}>Ultra (3x)</option>
              </select>
            </div>
          )}
          {dims && (
            <div className="text-[10px] text-theme-text-tertiary mb-2">
              Output: {dims.width} × {dims.height} px
            </div>
          )}
          {format !== 'application/vnd.jgraph.mxfile' && (
            <div className="flex items-center mb-3">
              <label className="flex items-center gap-2 cursor-pointer">
                <input
                  type="checkbox"
                  checked={transparent}
                  onChange={(e) => setTransparent(e.target.checked)}
                  className="rounded"
                />
                <span className="text-xs text-theme-text-secondary">Transparent background</span>
              </label>
            </div>
          )}
          <div className="flex gap-2">
            <button
              onClick={() => setShowDialog(false)}
              className="flex-1 px-3 py-1.5 text-sm text-theme-text-secondary hover:text-theme-text-primary hover:bg-theme-elevated rounded transition-colors"
            >
              Cancel
            </button>
            <button
              onClick={doExport}
              disabled={exporting}
              className="flex-1 px-3 py-1.5 text-sm font-medium btn-brand rounded flex items-center justify-center gap-1.5"
            >
              {exporting ? <Loader2 className="w-3 h-3 animate-spin" /> : <Download className="w-3 h-3" />}
              Export
            </button>
          </div>
        </div>,
        document.body,
      )}
      {exporting && (
        <div className="fixed inset-0 z-[100] flex items-center justify-center bg-black/40">
          <div className="bg-theme-surface border border-theme-border rounded-lg px-5 py-4 shadow-2xl">
            <div className="text-sm text-theme-text-primary animate-pulse">Exporting…</div>
          </div>
        </div>
      )}
    </>
  )
}
