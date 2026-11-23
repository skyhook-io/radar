import { useCallback, useLayoutEffect, useMemo, useRef, useState } from 'react'
import { createPortal } from 'react-dom'
import { useReactFlow, useStore } from '@xyflow/react'
import { Download, Loader2 } from 'lucide-react'
import { Input } from '../ui/Input'
import { Tooltip } from '../ui/Tooltip'
import { useToast } from '../ui/Toast'
import { triggerBlobDownload } from '../../utils/download'
import {
  captureFlowImage,
  graphExportSize,
  GRAPH_FORMAT_EXT,
  GRAPH_FORMAT_LABELS,
  GRAPH_IMAGE_FORMATS,
  type GraphCaptureMode,
  type GraphImageFormat,
} from '../../utils/graph-export'

export interface GraphExportButtonProps {
  /** Filename prefix, followed by a timestamp (e.g. "topology"). */
  defaultFilename: string
  /** Dialog heading. */
  title: string
  /** True while an entire-graph capture runs. A graph that renders only
   *  visible elements must render all of them for that time. */
  onExportingChange?: (exporting: boolean) => void
  /** Saves the file instead of a browser download (the desktop app's native
   *  save dialog). It owns its own success and failure messages. */
  onDownloadBlob?: (blob: Blob, filename: string) => void
}

const DIALOG_WIDTH = 288

/** Export-as-image button and dialog. Must render inside a React Flow graph. */
export function GraphExportButton({ defaultFilename, title, onExportingChange, onDownloadBlob }: GraphExportButtonProps) {
  const [showDialog, setShowDialog] = useState(false)
  const [exporting, setExporting] = useState(false)
  const [filename, setFilename] = useState('')
  const [transparent, setTransparent] = useState(false)
  const [scale, setScale] = useState(2)
  const [captureMode, setCaptureMode] = useState<GraphCaptureMode>('full')
  const [format, setFormat] = useState<GraphImageFormat>('image/webp')
  const container = useStore((s) => s.domNode)
  const { getNodes, getNodesBounds } = useReactFlow()
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
  }, [showDialog, captureMode, container])

  const dims = useMemo(() => {
    if (!showDialog || !container) return null
    const nodes = getNodes()
    if (nodes.length === 0) return null
    return graphExportSize(captureMode, container, getNodesBounds(nodes), scale)
  }, [showDialog, container, captureMode, scale, getNodes, getNodesBounds])

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

    const isFullCapture = captureMode === 'full'
    if (isFullCapture && onExportingChange) {
      onExportingChange(true)
      // Wait for React to render all off-screen nodes
      await new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve)))
    }

    // Yield to let the UI paint the exporting state before heavy DOM work
    await new Promise(resolve => setTimeout(resolve, 50))

    try {
      const blob = await captureFlowImage({
        container,
        mode: captureMode,
        bounds: getNodesBounds(nodes),
        scale,
        transparent,
        format,
      })
      const ext = GRAPH_FORMAT_EXT[format]
      triggerBlobDownload(blob, `${filename || defaultFilename}.${ext}`, onDownloadBlob)
      if (!onDownloadBlob) {
        const sizeMB = (blob.size / 1024 / 1024).toFixed(1)
        showSuccess(`Exported ${ext.toUpperCase()} (${sizeMB} MB)`)
      }
    } catch (err) {
      console.error('Failed to export graph:', err)
      showError('Export failed', err instanceof Error ? err.message : String(err))
    } finally {
      setExporting(false)
      onExportingChange?.(false)
      setShowDialog(false)
    }
  }, [container, getNodes, getNodesBounds, transparent, scale, captureMode, format, filename, defaultFilename, onDownloadBlob, showError, showSuccess, onExportingChange])

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
      <Tooltip content="Export as image" delay={100} position="right">
        <button
          ref={buttonRef}
          className="react-flow__controls-button"
          onClick={openDialog}
          disabled={exporting}
          aria-label="Export as image"
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
          <div className="flex items-center gap-3 mb-2">
            <div className="flex-1">
              <label className="block text-xs text-theme-text-secondary mb-1">Format</label>
              <div className="flex gap-1">
                {GRAPH_IMAGE_FORMATS.map(f => (
                  <button
                    key={f}
                    onClick={() => setFormat(f)}
                    className={`flex-1 px-2 py-1.5 text-xs rounded transition-colors ${format === f ? 'btn-brand' : 'bg-theme-base text-theme-text-secondary hover:text-theme-text-primary border border-theme-border'}`}
                  >
                    {GRAPH_FORMAT_LABELS[f]}
                  </button>
                ))}
              </div>
            </div>
            {captureMode === 'viewport' && (
              <div className="flex-1">
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
          </div>
          {dims && (
            <div className="text-[10px] text-theme-text-tertiary mb-2">
              Output: {dims.width} × {dims.height} px
            </div>
          )}
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
            <div className="text-sm text-theme-text-primary animate-pulse">Exporting image…</div>
          </div>
        </div>
      )}
    </>
  )
}
