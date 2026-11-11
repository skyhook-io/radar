import { useEffect, useRef, useState } from 'react'
import Editor, { type OnMount } from '@monaco-editor/react'
import { AlertTriangle, Download, FileText, Loader2, Pencil, RotateCw } from 'lucide-react'
import { AlertBanner, PaneLoader, ensureMonacoRuntime } from '@skyhook-io/k8s-ui'
import { formatBytes } from '../../utils/format'
import { Disclosure } from '@skyhook-io/k8s-ui/components/ui/Disclosure'
import { apiUrl, getAuthHeaders, getCredentialsMode } from '../../api/config'
import { nonJsonErrorMessage } from '../../api/httpErrors'
import { Tooltip } from '../ui/Tooltip'
import { FILE_CHANGED, PodFileSaveError, type SaveResult } from './pod-file-edit'
import { POD_FILE_PERSISTENCE_NOTE } from './pod-file-upload'

// A curated inline viewer for text files inside a pod container, rendered in
// place of the file listing by PodFilesystemModal — never as its own dialog.
// Every error path is a named `code` from the backend so this file switches on
// intent, not on stderr wording. Editing is opt-in through the `edit` prop.

// Mirrors podFilePreviewByteCap on the server. The listing already knows each
// file's size, so a file over the cap is marked unopenable up front instead
// of being clicked into a rejection.
export const PREVIEW_BYTE_CAP = 1 << 20

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
  /** Hex SHA-256 of the bytes served; absent from a Radar too old to save edits. */
  sha256?: string
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
  let body: Record<string, unknown>
  try {
    body = raw ? (JSON.parse(raw) as Record<string, unknown>) : {}
  } catch {
    return {
      ok: false,
      code: 'network_error',
      message: response.ok
        ? `Unexpected non-JSON response (HTTP ${response.status}).`
        : nonJsonErrorMessage(response, raw),
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
    sha256: typeof body.sha256 === 'string' ? body.sha256 : undefined,
  }
}

// Map a filename to a Monaco language id. Only ids whose tokenizer the shared
// runtime registers (see monacoRuntime.ts) — anything else falls to plaintext.
// JSON goes to the YAML tokenizer: YAML 1.2 is a superset of JSON, and the
// JSON language would need a validation worker the runtime does not ship.
export function detectLanguage(fileName: string): string {
  const lower = fileName.toLowerCase()
  if (lower.endsWith('.json')) return 'yaml'
  if (lower.endsWith('.yaml') || lower.endsWith('.yml')) return 'yaml'
  if (lower.endsWith('.xml')) return 'xml'
  if (lower.endsWith('.html') || lower.endsWith('.htm')) return 'html'
  if (lower.endsWith('.css')) return 'css'
  if (lower.endsWith('.js') || lower.endsWith('.mjs')) return 'javascript'
  if (lower.endsWith('.ts')) return 'typescript'
  if (lower.endsWith('.py')) return 'python'
  if (lower.endsWith('.sh') || lower.endsWith('.bash')) return 'shell'
  if (lower.endsWith('.md') || lower.endsWith('.markdown')) return 'markdown'
  if (lower === 'dockerfile' || lower.endsWith('.dockerfile')) return 'dockerfile'
  if (lower.endsWith('.toml')) return 'ini'
  if (lower.endsWith('.ini') || lower.endsWith('.conf') || lower.endsWith('.cfg')) return 'ini'
  return 'plaintext'
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

export interface PodFileEditing {
  /** Why this file can't be edited, given its contents; null when it can. */
  blockedReason: (content: string) => string | null
  save: (content: string, sha256: string) => Promise<SaveResult>
  onDirtyChange: (dirty: boolean) => void
  /** Runs `discard` once the user agrees to drop unsaved changes. */
  confirmDiscard: (discard: () => void) => void
  onSaved: (result: SaveResult) => void
}

interface PodFilePreviewProps {
  namespace: string
  podName: string
  container: string
  filePath: string
  fileName: string
  onDownload: () => void
  /** Omitted when the user can't edit here (no exec, or a Radar without the endpoint). */
  edit?: PodFileEditing
}

type SaveProblem = { kind: 'changed' | 'failed'; message: string }

// The BOM and the file's own line endings are part of the bytes being saved;
// the plain getValue() drops the BOM.
const TEXT_DEFINED_EOL = 0

export function PodFilePreview({
  namespace,
  podName,
  container,
  filePath,
  fileName,
  onDownload,
  edit,
}: PodFilePreviewProps) {
  const [result, setResult] = useState<PreviewResult | null>(null)
  const [loading, setLoading] = useState(true)
  const [reloadCount, setReloadCount] = useState(0)
  const [runtime, setRuntime] = useState<'loading' | 'ready' | 'error'>('loading')
  const theme = useMonacoTheme()
  const [editing, setEditing] = useState(false)
  const [dirty, setDirty] = useState(false)
  const [saving, setSaving] = useState(false)
  const [problem, setProblem] = useState<SaveProblem | null>(null)
  // Remounting the editor is how Cancel puts the opened contents back.
  const [editorKey, setEditorKey] = useState(0)
  const editorRef = useRef<Parameters<OnMount>[0] | null>(null)
  const onDirtyChange = edit?.onDirtyChange

  useEffect(() => {
    onDirtyChange?.(dirty)
  }, [dirty, onDirtyChange])
  useEffect(() => () => onDirtyChange?.(false), [onDirtyChange])

  useEffect(() => {
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
  }, [namespace, podName, container, filePath, reloadCount])

  useEffect(() => {
    let active = true
    ensureMonacoRuntime()
      .then(() => active && setRuntime('ready'))
      .catch(() => active && setRuntime('error'))
    return () => {
      active = false
    }
  }, [])

  const leaveEditing = () => {
    setEditing(false)
    setDirty(false)
    setProblem(null)
    setEditorKey((k) => k + 1)
  }

  const currentText = () => editorRef.current?.getModel()?.getValue(TEXT_DEFINED_EOL, true) ?? ''

  if (loading) return <PaneLoader label="Reading file…" className="flex-1" />
  if (!result) return null

  if (!result.ok) {
    return (
      <PreviewErrorState
        result={result}
        onDownload={onDownload}
        onRetry={() => setReloadCount((n) => n + 1)}
      />
    )
  }

  const opened = result
  const blocked = edit ? edit.blockedReason(opened.content) : null
  const canEdit = !!edit && !!opened.sha256 && runtime === 'ready'

  const save = async () => {
    if (!edit || !opened.sha256 || saving) return
    const content = currentText()
    setSaving(true)
    setProblem(null)
    try {
      const saved = await edit.save(content, opened.sha256)
      setResult({ ...opened, content, size: saved.size, sha256: saved.sha256, empty: content === '' })
      leaveEditing()
      edit.onSaved(saved)
    } catch (err) {
      if (err instanceof PodFileSaveError && err.code === FILE_CHANGED) {
        setProblem({ kind: 'changed', message: err.message })
      } else {
        setProblem({ kind: 'failed', message: err instanceof Error ? err.message : String(err) })
      }
    } finally {
      setSaving(false)
    }
  }

  const cancel = () => {
    if (!dirty) {
      leaveEditing()
      return
    }
    edit?.confirmDiscard(leaveEditing)
  }

  const reloadDiscardingEdits = () => {
    edit?.confirmDiscard(() => {
      leaveEditing()
      setReloadCount((n) => n + 1)
    })
  }

  const toolbar = canEdit && (
    <div className="shrink-0 border-b border-theme-border px-3 py-2 flex flex-col gap-2">
      {editing && (
        <AlertBanner variant="info" title="Saving changes the live container" message={POD_FILE_PERSISTENCE_NOTE} className="mb-0" />
      )}
      {editing && problem?.kind === 'changed' && (
        <AlertBanner
          variant="warning"
          title="The file changed since you opened it"
          message={`${problem.message}. Your edits are still here; copy what you need, then reload to see the current file.`}
          className="mb-0"
          action={<button type="button" onClick={reloadDiscardingEdits} className="text-xs text-accent hover:underline">Discard my edits and reload</button>}
        />
      )}
      {editing && problem?.kind === 'failed' && (
        <AlertBanner variant="error" title="Could not save" message={problem.message} className="mb-0" />
      )}
      <div className="flex items-center gap-2">
        {editing ? (
          <>
            <span className="text-xs text-theme-text-tertiary mr-auto">{dirty ? 'Unsaved changes' : 'No changes yet'}</span>
            <button
              type="button"
              onClick={cancel}
              disabled={saving}
              className="px-3 py-1 text-xs rounded border border-theme-border text-theme-text-secondary hover:bg-theme-hover disabled:opacity-50"
            >
              Cancel
            </button>
            <button
              type="button"
              onClick={save}
              disabled={!dirty || saving}
              className="btn-brand inline-flex items-center gap-1.5 px-3 py-1 text-xs font-medium disabled:opacity-50 disabled:pointer-events-none"
            >
              {saving && <Loader2 className="w-3.5 h-3.5 animate-spin" />}
              {saving ? 'Saving…' : 'Save'}
            </button>
          </>
        ) : (
          <Tooltip content={blocked ?? 'Edit this file in the running container'} wrapperClassName="ml-auto">
            <button
              type="button"
              onClick={() => setEditing(true)}
              disabled={blocked !== null}
              className="btn-brand inline-flex items-center gap-1.5 px-3 py-1 text-xs font-medium disabled:opacity-50 disabled:pointer-events-none"
            >
              <Pencil className="w-3.5 h-3.5" />
              Edit
            </button>
          </Tooltip>
        )}
      </div>
    </div>
  )

  if (opened.empty && !editing) {
    return (
      <div className="flex-1 min-h-0 flex flex-col">
        {toolbar}
        <PreviewEmptyState fileName={fileName} />
      </div>
    )
  }

  if (runtime === 'loading') return <PaneLoader label="Loading editor…" className="flex-1" />

  if (runtime === 'error') {
    return (
      <pre className="flex-1 min-h-0 overflow-auto p-4 font-mono text-xs text-theme-text-primary whitespace-pre">
        {result.content}
      </pre>
    )
  }

  return (
    <div className="flex-1 min-h-0 flex flex-col">
      {toolbar}
      <div className="flex-1 min-h-0">
      <Editor
        key={editorKey}
        value={opened.content}
        language={detectLanguage(fileName)}
        theme={theme}
        onMount={(editor) => { editorRef.current = editor }}
        onChange={() => {
          if (editing) setDirty(currentText() !== opened.content)
        }}
        options={{
          readOnly: !editing || saving,
          domReadOnly: !editing || saving,
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
    </div>
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
        <Disclosure className="mt-4 text-xs text-theme-text-tertiary" summaryClassName="hover:text-theme-text-secondary" summary="Technical details">
          <div className="mt-2 font-mono whitespace-pre-wrap text-left bg-theme-elevated/40 p-3 rounded max-w-xl">
            {shape.details}
          </div>
        </Disclosure>
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

