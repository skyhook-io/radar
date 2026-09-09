import { createElement, useCallback, useEffect, useRef, useState } from 'react'
import { createPortal } from 'react-dom'
import Editor from '@monaco-editor/react'
import { X, AlertTriangle, Download, FileText, FolderOpen, RotateCw } from 'lucide-react'
import { PaneLoader } from '@skyhook-io/k8s-ui'
import { formatBytes } from '../../utils/format'
import { apiUrl, getAuthHeaders, getCredentialsMode } from '../../api/config'
import { downloadBlob } from './file-browser-utils'
import { isDesktopApp } from '../../utils/desktop-download'
import { openFile, openFolder } from '../../utils/desktop-open-folder'
import { useToast } from '../ui/Toast'

// A curated inline viewer for text files inside a pod container. Deliberately
// read-only for v1 — the download button is the only mutation-adjacent action.
// Every error path is a named `code` from the backend so this file switches on
// intent, not on stderr wording.

// The desktop-save route streams a pod file straight to disk from the backend,
// bypassing the webview round-trip that fails on large files. The download
// route goes through the webview and is fine in the browser. The preview
// offers Download precisely for the "file too large" case, so it MUST take
// the same desktop path as the file browser or that case is broken there too.
async function savePodFileToDisk(
  namespace: string,
  podName: string,
  container: string,
  filePath: string,
): Promise<string> {
  const params = new URLSearchParams()
  params.set('container', container)
  params.set('path', filePath)
  const response = await fetch(apiUrl(`/pods/${namespace}/${podName}/files/save?${params.toString()}`), {
    method: 'POST',
    credentials: getCredentialsMode(),
    headers: getAuthHeaders(),
  })
  if (response.status === 204) throw new Error('cancelled')
  if (!response.ok) {
    const error = await response.json().catch(() => ({ error: 'Save failed' }))
    throw new Error(error.error || `HTTP ${response.status}`)
  }
  const body = await response.json()
  return body.path as string
}

// Wire-side codes. Kept in sync with internal/server/copy.go.
export type PreviewErrorCode =
  | 'file_too_large'
  | 'binary_file'
  | 'not_a_regular_file'
  | 'not_found'
  | 'permission_denied'
  | 'no_shell'
  | 'container_missing_tools'
  | 'read_failed'

type PreviewSuccess = {
  ok: true
  content: string
  size: number
  mimeType: string
  encoding: string
  empty: boolean
}

export type PreviewError = {
  ok: false
  code: PreviewErrorCode | 'network_error'
  message: string
  size?: number
  mimeType?: string
}

type PreviewResult = PreviewSuccess | PreviewError

async function fetchPodFilePreview(
  namespace: string,
  podName: string,
  container: string,
  filePath: string,
  signal: AbortSignal,
): Promise<PreviewResult> {
  const params = new URLSearchParams()
  params.set('container', container)
  params.set('path', filePath)

  let response: Response
  try {
    response = await fetch(apiUrl(`/pods/${namespace}/${podName}/file?${params.toString()}`), {
      credentials: getCredentialsMode(),
      headers: getAuthHeaders(),
      signal,
    })
  } catch (err) {
    if ((err as { name?: string })?.name === 'AbortError') throw err
    return {
      ok: false,
      code: 'network_error',
      message: err instanceof Error ? err.message : 'Network request failed',
    }
  }

  const raw = await response.text()
  let body: Record<string, unknown> = {}
  try {
    body = raw ? (JSON.parse(raw) as Record<string, unknown>) : {}
  } catch {
    return {
      ok: false,
      code: 'network_error',
      message: `Unexpected non-JSON response (HTTP ${response.status}).`,
    }
  }

  if (!response.ok) {
    return {
      ok: false,
      code: (body.code as PreviewErrorCode) || 'read_failed',
      message: (body.error as string) || `HTTP ${response.status}`,
      size: typeof body.size === 'number' ? body.size : undefined,
      mimeType: typeof body.mimeType === 'string' ? body.mimeType : undefined,
    }
  }

  return {
    ok: true,
    content: (body.content as string) ?? '',
    size: (body.size as number) ?? 0,
    mimeType: (body.mimeType as string) ?? 'text/plain',
    encoding: (body.encoding as string) ?? 'utf-8',
    empty: body.code === 'empty_file',
  }
}

// Map a filename to a Monaco language id. Deliberately conservative — Monaco
// can highlight far more, but names it does not recognise fall to plaintext,
// which is fine for the preview surface.
export function detectLanguage(fileName: string): string {
  const lower = fileName.toLowerCase()
  if (lower.endsWith('.json')) return 'json'
  if (lower.endsWith('.yaml') || lower.endsWith('.yml')) return 'yaml'
  if (lower.endsWith('.xml')) return 'xml'
  if (lower.endsWith('.html') || lower.endsWith('.htm')) return 'html'
  if (lower.endsWith('.css')) return 'css'
  if (lower.endsWith('.js') || lower.endsWith('.mjs')) return 'javascript'
  if (lower.endsWith('.ts')) return 'typescript'
  if (lower.endsWith('.py')) return 'python'
  if (lower.endsWith('.sh') || lower.endsWith('.bash')) return 'shell'
  if (lower.endsWith('.md') || lower.endsWith('.markdown')) return 'markdown'
  if (lower.endsWith('.toml')) return 'ini'
  if (lower.endsWith('.ini') || lower.endsWith('.conf') || lower.endsWith('.cfg')) return 'ini'
  return 'plaintext'
}

// Shared open counter for the preview modal. The filesystem browser reads it
// to know whether an ESC keypress belongs to a nested preview — capture-phase
// listeners on `document` see every event, and `stopPropagation()` does not
// stop other listeners on the same target, so the parent has to opt out
// explicitly. A module-level counter avoids threading state through props.
let previewOpenCount = 0

export function isPodFilePreviewOpen(): boolean {
  return previewOpenCount > 0
}

function useMonacoTheme() {
  const [dark, setDark] = useState(() =>
    typeof document !== 'undefined' && document.documentElement.classList.contains('dark'),
  )
  useEffect(() => {
    if (typeof document === 'undefined') return
    const observer = new MutationObserver(() => {
      setDark(document.documentElement.classList.contains('dark'))
    })
    observer.observe(document.documentElement, { attributes: true, attributeFilter: ['class'] })
    return () => observer.disconnect()
  }, [])
  return dark ? 'vs-dark' : 'vs'
}

interface PodFilePreviewModalProps {
  open: boolean
  onClose: () => void
  namespace: string
  podName: string
  container: string
  filePath: string
  fileName: string
}

export function PodFilePreviewModal({
  open,
  onClose,
  namespace,
  podName,
  container,
  filePath,
  fileName,
}: PodFilePreviewModalProps) {
  const dialogRef = useRef<HTMLDivElement>(null)
  const [result, setResult] = useState<PreviewResult | null>(null)
  const [loading, setLoading] = useState(false)
  const [reloadCount, setReloadCount] = useState(0)
  const theme = useMonacoTheme()
  const { showError, showSuccess } = useToast()

  useEffect(() => {
    if (!open) return
    const controller = new AbortController()
    setLoading(true)
    setResult(null)
    fetchPodFilePreview(namespace, podName, container, filePath, controller.signal)
      .then((r) => setResult(r))
      .catch((err) => {
        if ((err as { name?: string })?.name === 'AbortError') return
        setResult({
          ok: false,
          code: 'network_error',
          message: err instanceof Error ? err.message : 'Request failed',
        })
      })
      .finally(() => setLoading(false))
    return () => controller.abort()
  }, [open, namespace, podName, container, filePath, reloadCount])

  useEffect(() => {
    if (!open) return
    previewOpenCount += 1
    const handleKeyDown = (e: KeyboardEvent) => {
      if (e.key === 'Escape') {
        e.stopPropagation()
        onClose()
      }
    }
    document.addEventListener('keydown', handleKeyDown, true)
    return () => {
      document.removeEventListener('keydown', handleKeyDown, true)
      previewOpenCount -= 1
    }
  }, [open, onClose])

  useEffect(() => {
    if (open && dialogRef.current) {
      dialogRef.current.focus()
    }
  }, [open])

  const handleDownload = useCallback(async () => {
    try {
      if (await isDesktopApp()) {
        const savedPath = await savePodFileToDisk(namespace, podName, container, filePath)
        showSuccess(
          'File saved',
          savedPath,
          {
            label: 'Show in Finder',
            icon: createElement(FolderOpen, { className: 'w-3.5 h-3.5' }),
            onClick: () => openFolder(savedPath),
          },
          () => openFile(savedPath),
        )
        return
      }

      const params = new URLSearchParams()
      params.set('container', container)
      params.set('path', filePath)
      const response = await fetch(
        apiUrl(`/pods/${namespace}/${podName}/files/download?${params.toString()}`),
        {
          credentials: getCredentialsMode(),
          headers: getAuthHeaders(),
        },
      )
      if (!response.ok) {
        const err = await response.json().catch(() => ({ error: 'Download failed' }))
        throw new Error(err.error || `HTTP ${response.status}`)
      }
      const blob = await response.blob()
      await downloadBlob(blob, fileName)
    } catch (err) {
      const message = err instanceof Error ? err.message : String(err)
      if (message !== 'cancelled') {
        showError(`Could not download ${fileName}`, message)
      }
    }
  }, [namespace, podName, container, filePath, fileName, showError, showSuccess])

  if (!open) return null

  const language = detectLanguage(fileName)

  return createPortal(
    <div className="fixed inset-0 z-[110] flex items-center justify-center">
      <div className="absolute inset-0 bg-black/60 backdrop-blur-sm" onClick={onClose} />

      {/*
        The dialog carries a definite h-[85vh] rather than only max-h so the
        Monaco editor (whose default height is 100%) resolves to a positive
        pixel value. A body sized by content collapses the editor to zero.
      */}
      <div
        ref={dialogRef}
        tabIndex={-1}
        className="relative dialog w-full max-w-5xl mx-4 h-[85vh] flex flex-col outline-none"
      >
        <div className="flex items-center justify-between p-4 border-b border-theme-border shrink-0">
          <div className="flex-1 min-w-0">
            <div className="flex items-center gap-2">
              <FileText className="w-4 h-4 text-theme-text-tertiary shrink-0" />
              <h3 className="text-lg font-semibold text-theme-text-primary truncate">{fileName}</h3>
            </div>
            <p className="text-xs text-theme-text-tertiary truncate mt-0.5 font-mono">
              {filePath}
            </p>
            <p className="text-xs text-theme-text-tertiary mt-0.5">
              {namespace}/{podName} · {container}
              {result?.ok && !result.empty && result.size ? ` · ${formatBytes(result.size)}` : null}
            </p>
          </div>

          <div className="flex items-center gap-1 ml-3">
            <button
              onClick={handleDownload}
              className="p-2 text-theme-text-secondary hover:text-theme-text-primary hover:bg-theme-elevated rounded"
              title="Download file"
            >
              <Download className="w-4 h-4" />
            </button>
            <button
              onClick={onClose}
              className="p-2 text-theme-text-secondary hover:text-theme-text-primary hover:bg-theme-elevated rounded"
            >
              <X className="w-5 h-5" />
            </button>
          </div>
        </div>

        <div className="flex-1 min-h-0 flex flex-col">
          {loading && <PaneLoader label="Reading file…" className="flex-1" />}

          {!loading && result?.ok && !result.empty && (
            <div className="flex-1 min-h-0">
              <Editor
                value={result.content}
                language={language}
                theme={theme}
                options={{
                  readOnly: true,
                  domReadOnly: true,
                  minimap: { enabled: false },
                  scrollBeyondLastLine: false,
                  fontSize: 12,
                  lineNumbers: 'on',
                  wordWrap: 'off',
                  renderWhitespace: 'selection',
                }}
                loading={<PaneLoader label="Loading editor…" className="h-full" />}
              />
            </div>
          )}

          {!loading && result?.ok && result.empty && (
            <PreviewEmptyState fileName={fileName} />
          )}

          {!loading && result && !result.ok && (
            <PreviewErrorState
              result={result}
              onDownload={handleDownload}
              onRetry={() => setReloadCount((n) => n + 1)}
            />
          )}
        </div>
      </div>
    </div>,
    document.body,
  )
}

// ============================================================================
// Curated error / empty states
// ============================================================================

function PreviewEmptyState({ fileName }: { fileName: string }) {
  return (
    <div className="flex-1 flex flex-col items-center justify-center text-center p-8">
      <FileText className="w-10 h-10 text-theme-text-tertiary mb-3" />
      <div className="text-base font-medium text-theme-text-primary">{fileName} is empty</div>
      <div className="text-sm text-theme-text-secondary mt-1">
        This file has no content.
      </div>
    </div>
  )
}

function PreviewErrorState({
  result,
  onDownload,
  onRetry,
}: {
  result: PreviewError
  onDownload: () => void
  onRetry: () => void
}) {
  const shape = curateError(result)

  return (
    <div className="flex-1 flex flex-col items-center justify-center text-center p-8 max-w-2xl mx-auto">
      <AlertTriangle className={`w-10 h-10 mb-3 ${shape.severity === 'warning' ? 'text-amber-400' : 'text-red-400'}`} />
      <div className="text-base font-medium text-theme-text-primary">{shape.title}</div>
      <div className="text-sm text-theme-text-secondary mt-2 leading-relaxed">
        {shape.description}
      </div>

      <div className="flex items-center gap-2 mt-6">
        {shape.actions.download && (
          <button
            onClick={onDownload}
            className="flex items-center gap-2 px-3 py-1.5 rounded btn-brand text-sm"
          >
            <Download className="w-3.5 h-3.5" />
            Download
          </button>
        )}
        {shape.actions.retry && (
          <button
            onClick={onRetry}
            className="flex items-center gap-2 px-3 py-1.5 rounded border border-theme-border hover:bg-theme-elevated text-sm text-theme-text-primary"
          >
            <RotateCw className="w-3.5 h-3.5" />
            Retry
          </button>
        )}
      </div>

      {shape.details && (
        <details className="mt-4 text-xs text-theme-text-tertiary">
          <summary className="cursor-pointer hover:text-theme-text-secondary">Technical details</summary>
          <div className="mt-2 font-mono whitespace-pre-wrap text-left bg-theme-elevated/40 p-3 rounded max-w-xl">
            {shape.details}
          </div>
        </details>
      )}
    </div>
  )
}

export interface CuratedShape {
  title: string
  description: string
  severity: 'warning' | 'error'
  actions: { download: boolean; retry: boolean }
  details?: string
}

export function curateError(result: PreviewError): CuratedShape {
  const size = result.size ? formatBytes(result.size) : ''

  switch (result.code) {
    case 'file_too_large':
      return {
        title: 'File too large to preview',
        description: size
          ? `This file is ${size} — larger than the 1 MiB preview limit. Download to view the full contents.`
          : 'This file is larger than the 1 MiB preview limit. Download to view.',
        severity: 'warning',
        actions: { download: true, retry: false },
      }
    case 'binary_file':
      return {
        title: 'Binary file — cannot preview',
        description: result.mimeType
          ? `This file appears to be binary (detected type: ${result.mimeType}). Download it to view or open with an appropriate application.`
          : 'This file is not valid text and cannot be shown inline. Download to view.',
        severity: 'warning',
        actions: { download: true, retry: false },
      }
    case 'not_a_regular_file':
      return {
        title: 'Not a regular file',
        description: 'This entry is a directory, device, or pipe — Radar cannot preview its contents.',
        severity: 'warning',
        actions: { download: false, retry: false },
      }
    case 'not_found':
      return {
        title: 'File not found',
        description: 'The file no longer exists in the container. It may have been rotated or deleted since the listing.',
        severity: 'warning',
        actions: { download: false, retry: true },
      }
    case 'permission_denied':
      return {
        title: 'Permission denied',
        description: 'The container user cannot read this file. Try switching to a different container, or run Radar with an identity that has broader access.',
        severity: 'warning',
        actions: { download: false, retry: false },
      }
    case 'no_shell':
      return {
        title: 'Preview not available',
        description: 'This container has no shell (likely distroless or scratch-based), so its filesystem cannot be read this way. Download is unavailable for the same reason.',
        severity: 'warning',
        actions: { download: false, retry: false },
      }
    case 'container_missing_tools':
      return {
        title: 'Container tools missing',
        description: 'This container lacks the tools Radar needs to read files (tar and cat). Download may still work in a fallback mode.',
        severity: 'warning',
        actions: { download: true, retry: false },
      }
    case 'network_error':
      return {
        title: 'Network error',
        description: 'Radar could not reach the cluster. Check your connection and try again.',
        severity: 'error',
        actions: { download: false, retry: true },
        details: result.message,
      }
    case 'read_failed':
    default:
      return {
        title: 'Could not read the file',
        description: 'Radar reached the container but could not read the file. This may be transient — try again, or download instead.',
        severity: 'error',
        actions: { download: true, retry: true },
        details: result.message,
      }
  }
}

