import { createElement, useState, useRef, useEffect, useMemo, useCallback } from 'react'
import { createPortal } from 'react-dom'
import { X, File, Link2, ChevronRight, ArrowLeft, AlertTriangle, Loader2, Search, Download, FolderOpen } from 'lucide-react'
import { PaneLoader, Input } from '@skyhook-io/k8s-ui'
import { clsx } from 'clsx'
import type { FileNode } from '../../types'
import { formatBytes } from '../../utils/format'
import { downloadBlob, filterTree } from './file-browser-utils'
import { apiUrl, getAuthHeaders, getCredentialsMode } from '../../api/config'
import { readErrorBody } from '../../api/httpErrors'
import { isDesktopApp } from '../../utils/desktop-download'
import { openFile, openFolder } from '../../utils/desktop-open-folder'
import { useToast } from '../ui/Toast'
import { Tooltip } from '../ui/Tooltip'
import { PodFilePreview, PREVIEW_BYTE_CAP } from './PodFilePreview'
import { useNamespacedCapabilities } from '../../contexts/CapabilitiesContext'
import { useRadarFeature } from '../../api/client'
import {
  canShowUpload,
  fileMountHint,
  isValidUploadName,
  oversizeMessage,
  PodFileUploadError,
  UPLOAD_CANCELLED,
  uploadBlockedHint,
  uploadPodFile,
  uploadTarget,
} from './pod-file-upload'
import { isUploadBusy, PodFileUploadButton, PodFileUploadNotices, PodFileUploadStatus, type UploadState } from './PodFileUploadPanel'

interface PodFilesystem {
  root: FileNode
  totalFiles: number
}

async function fetchPodFiles(
  namespace: string,
  podName: string,
  container: string,
  dirPath: string,
): Promise<PodFilesystem> {
  const params = new URLSearchParams()
  params.set('container', container)
  params.set('path', dirPath)

  const response = await fetch(apiUrl(`/pods/${namespace}/${podName}/files?${params.toString()}`), {
    credentials: getCredentialsMode(),
    headers: getAuthHeaders(),
  })
  if (!response.ok) {
    const error = await readErrorBody(response)
    throw new Error(error.error || `HTTP ${response.status}`)
  }
  return response.json()
}

/**
 * Desktop only: has the backend write the pod file straight to disk. The browser
 * route would hand the whole file to the webview only to have it hand every byte
 * back to be saved, which is what puts a large file out of reach there.
 * Returns the path it was saved to.
 */
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
  return body.path
}

type PodFileToast = Pick<ReturnType<typeof useToast>, 'showSuccess' | 'showError'>

// One download path for the listing row and the preview header. Desktop saves
// straight from the backend; the browser gets a blob.
async function downloadPodFile(
  namespace: string,
  podName: string,
  container: string,
  node: Pick<FileNode, 'path' | 'name'>,
  { showSuccess, showError }: PodFileToast,
) {
  try {
    if (await isDesktopApp()) {
      const savedPath = await savePodFileToDisk(namespace, podName, container, node.path)
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
    params.set('path', node.path)

    const response = await fetch(apiUrl(`/pods/${namespace}/${podName}/files/download?${params.toString()}`), {
      credentials: getCredentialsMode(),
      headers: getAuthHeaders(),
    })
    if (!response.ok) {
      const err = await readErrorBody(response)
      throw new Error(err.error || `HTTP ${response.status}`)
    }

    const blob = await response.blob()
    await downloadBlob(blob, node.name)
  } catch (err) {
    const message = err instanceof Error ? err.message : String(err)
    if (message !== 'cancelled') {
      showError(`Could not download ${node.name}`, message)
    }
  }
}

interface PodFilesystemModalProps {
  open: boolean
  onClose: () => void
  namespace: string
  podName: string
  containers: string[]
  initialContainer?: string
  onSwitchToImageFiles?: (containerName: string) => void
  /** The pod object; its volume mounts tell where uploads cannot work. */
  pod?: unknown
}

export function PodFilesystemModal({
  open,
  onClose,
  namespace,
  podName,
  containers,
  initialContainer,
  onSwitchToImageFiles,
  pod,
}: PodFilesystemModalProps) {
  const dialogRef = useRef<HTMLDivElement>(null)
  const [searchQuery, setSearchQuery] = useState('')
  const [selectedContainer, setSelectedContainer] = useState(initialContainer || containers[0] || '')
  const [currentPath, setCurrentPath] = useState('/')
  const [filesystem, setFilesystem] = useState<PodFilesystem | null>(null)
  // The container the shown listing came from; it lags selectedContainer
  // while a switch is loading.
  const [listedContainer, setListedContainer] = useState<string | null>(null)
  const loadSeq = useRef(0)
  const [isLoading, setIsLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)
  // The file being viewed in place of the listing; null shows the directory.
  const [previewFile, setPreviewFile] = useState<FileNode | null>(null)
  const toast = useToast()

  const { canExec } = useNamespacedCapabilities(namespace)
  const { support: uploadSupport, guard: guardUpload } = useRadarFeature('podFileUpload')
  const uploadEnabled = canShowUpload({ canExec, featureSupported: uploadSupport === 'supported' })
  const [upload, setUpload] = useState<UploadState>({ phase: 'idle' })
  const [dragging, setDragging] = useState(false)
  const pendingFile = useRef<File | null>(null)
  const uploadAbort = useRef<AbortController | null>(null)
  const fileInputRef = useRef<HTMLInputElement>(null)
  const listRef = useRef<HTMLDivElement>(null)
  const currentPathRef = useRef(currentPath)
  useEffect(() => { currentPathRef.current = currentPath }, [currentPath])
  const uploadRef = useRef(upload)
  useEffect(() => { uploadRef.current = upload }, [upload])
  // Uploads that had sent every byte when the dialog closed or the container
  // changed. The container may already be committing them, so they run on and
  // report through a toast instead of being cancelled.
  const detachedUploads = useRef(new Set<AbortController>())

  const loadDirectory = useCallback(async (dirPath: string) => {
    const seq = ++loadSeq.current
    setPreviewFile(null)
    setIsLoading(true)
    setError(null)
    try {
      const result = await fetchPodFiles(namespace, podName, selectedContainer, dirPath)
      if (seq !== loadSeq.current) return
      setFilesystem(result)
      setCurrentPath(dirPath)
      setListedContainer(selectedContainer)
    } catch (err) {
      if (seq !== loadSeq.current) return
      setError(err instanceof Error ? err.message : 'Failed to list files')
    } finally {
      if (seq === loadSeq.current) setIsLoading(false)
    }
  }, [namespace, podName, selectedContainer])

  // Load root on open or container change
  useEffect(() => {
    if (open && selectedContainer) {
      loadDirectory('/')
    }
  }, [open, selectedContainer, loadDirectory])

  // Reset state when modal closes
  useEffect(() => {
    if (!open) {
      setSearchQuery('')
      setFilesystem(null)
      setListedContainer(null)
      setError(null)
      setIsLoading(false)
      setCurrentPath('/')
      setPreviewFile(null)
      setSelectedContainer(initialContainer || containers[0] || '')
    }
  }, [open, initialContainer, containers])

  // Leaving the dialog or the container abandons an upload that has not
  // finished sending; the server then writes nothing. One that is already
  // saving is left to finish.
  useEffect(() => {
    if (!open) return
    const detached = detachedUploads.current
    return () => {
      const abort = uploadAbort.current
      if (abort && uploadRef.current.phase === 'saving') detached.add(abort)
      else abort?.abort()
      uploadAbort.current = null
      pendingFile.current = null
      setUpload({ phase: 'idle' })
    }
  }, [open, selectedContainer])

  const blockedHint = useMemo(
    () => (uploadEnabled ? uploadBlockedHint(pod, selectedContainer, currentPath) : null),
    [uploadEnabled, pod, selectedContainer, currentPath],
  )

  const sendFile = useCallback(async (file: File, dir: string, overwrite: boolean) => {
    const abort = new AbortController()
    uploadAbort.current = abort
    setUpload({ phase: 'uploading', name: file.name, dir, sent: 0, total: file.size })
    try {
      const result = await guardUpload(() => uploadPodFile({
        namespace,
        podName,
        container: selectedContainer,
        dir,
        file,
        name: file.name,
        overwrite,
        signal: abort.signal,
        onProgress: (sent, total) => {
          if (abort.signal.aborted) return
          // Every byte is out; what remains is the container committing it.
          setUpload(sent >= total
            ? { phase: 'saving', name: file.name, dir }
            : { phase: 'uploading', name: file.name, dir, sent, total })
        },
      }))
      if (detachedUploads.current.delete(abort)) {
        toast.showSuccess(result.replaced ? 'File replaced' : 'File uploaded', result.path)
        return
      }
      if (uploadAbort.current !== abort) return
      setUpload({ phase: 'saved', path: result.path, replaced: result.replaced })
      if (dir === currentPathRef.current) loadDirectory(dir)
    } catch (err) {
      if (detachedUploads.current.delete(abort)) {
        toast.showError(`Could not upload ${file.name}`, err instanceof Error ? err.message : String(err))
        return
      }
      if (uploadAbort.current !== abort) return
      if (err instanceof PodFileUploadError && err.code === UPLOAD_CANCELLED) {
        setUpload({ phase: 'idle' })
        return
      }
      setUpload({ phase: 'error', name: file.name, message: err instanceof Error ? err.message : String(err) })
    } finally {
      if (uploadAbort.current === abort) uploadAbort.current = null
    }
  }, [guardUpload, namespace, podName, selectedContainer, loadDirectory, toast])

  const listingReady = !isLoading && !!filesystem?.root && listedContainer === selectedContainer

  const startUpload = useCallback((file: File) => {
    if (!listingReady) return
    const dir = currentPath
    if (!isValidUploadName(file.name)) {
      setUpload({ phase: 'error', name: file.name, message: 'That file name cannot be used in the container.' })
      return
    }
    const oversize = oversizeMessage(file.size)
    if (oversize) {
      setUpload({ phase: 'error', name: file.name, message: oversize })
      return
    }
    const filePath = dir === '/' ? `/${file.name}` : `${dir}/${file.name}`
    const mounted = fileMountHint(pod, selectedContainer, filePath)
    if (mounted) {
      setUpload({ phase: 'error', name: file.name, message: mounted.reason })
      return
    }
    const target = uploadTarget(filesystem?.root, file.name)
    if (target.kind === 'refuse') {
      setUpload({ phase: 'error', name: file.name, message: target.reason })
      return
    }
    if (target.kind === 'replace') {
      pendingFile.current = file
      setUpload({ phase: 'confirm', name: file.name, dir, size: file.size })
      return
    }
    sendFile(file, dir, false)
  }, [listingReady, currentPath, pod, selectedContainer, filesystem, sendFile])

  const confirmReplace = useCallback(() => {
    const file = pendingFile.current
    pendingFile.current = null
    if (file && upload.phase === 'confirm') sendFile(file, upload.dir, true)
  }, [sendFile, upload])

  const cancelUpload = useCallback(() => {
    pendingFile.current = null
    const abort = uploadAbort.current
    uploadAbort.current = null
    abort?.abort()
    setUpload({ phase: 'idle' })
  }, [])

  const uploadHere = uploadEnabled && !previewFile && listingReady && !blockedHint && !isUploadBusy(upload)
  // A file dropped anywhere in the dialog must never reach the browser, which
  // would open it in place of Radar; only a drop on the listing uploads it.
  const ownsFileDrag = (e: React.DragEvent) => uploadEnabled && e.dataTransfer.types.includes('Files')
  const overList = (e: React.DragEvent) => !!listRef.current?.contains(e.target as Node)

  const handleDragOver = (e: React.DragEvent) => {
    if (!ownsFileDrag(e)) return
    e.preventDefault()
    const accept = uploadHere && overList(e)
    e.dataTransfer.dropEffect = accept ? 'copy' : 'none'
    if (accept !== dragging) setDragging(accept)
  }
  const handleDragLeave = (e: React.DragEvent) => {
    if (!e.currentTarget.contains(e.relatedTarget as Node | null)) setDragging(false)
  }
  const handleDrop = (e: React.DragEvent) => {
    setDragging(false)
    if (!ownsFileDrag(e)) return
    e.preventDefault()
    if (!overList(e)) return
    if (!uploadHere) {
      if (blockedHint) setUpload({ phase: 'error', name: 'the file', message: blockedHint.reason })
      return
    }
    const items = Array.from(e.dataTransfer.items ?? [])
    if (items.some((item) => item.webkitGetAsEntry?.()?.isDirectory)) {
      setUpload({ phase: 'error', name: 'folder', message: 'Folders cannot be uploaded; drop a single file.' })
      return
    }
    const files = Array.from(e.dataTransfer.files)
    if (files.length !== 1) {
      setUpload({ phase: 'error', name: `${files.length} files`, message: 'Drop one file at a time.' })
      return
    }
    startUpload(files[0])
  }

  // ESC steps back one level: out of a file to its listing, then out of the
  // dialog. One listener owns both so the two can never race.
  useEffect(() => {
    if (!open) return
    const handleKeyDown = (e: KeyboardEvent) => {
      if (e.key !== 'Escape') return
      e.stopPropagation()
      if (previewFile) setPreviewFile(null)
      else onClose()
    }
    document.addEventListener('keydown', handleKeyDown, true)
    return () => document.removeEventListener('keydown', handleKeyDown, true)
  }, [open, onClose, previewFile])

  // Focus trap
  useEffect(() => {
    if (open && dialogRef.current) {
      dialogRef.current.focus()
    }
  }, [open])

  if (!open) return null

  const showFilesystem = filesystem && filesystem.root

  // Breadcrumb: the directory path, plus the file when one is open. Clicking
  // the current directory while viewing a file just returns to its listing.
  const breadcrumbPath = previewFile ? previewFile.path : currentPath
  const pathSegments = breadcrumbPath === '/'
    ? ['/']
    : ['/', ...breadcrumbPath.split('/').filter(Boolean)]
  const goToDirectory = (dirPath: string) => {
    if (dirPath === currentPath) setPreviewFile(null)
    else loadDirectory(dirPath)
  }
  const downloadPreviewFile = () => {
    if (previewFile) downloadPodFile(namespace, podName, selectedContainer, previewFile, toast)
  }

  return createPortal(
    <div
      className="fixed inset-0 z-[100] flex items-center justify-center"
      onDragOver={handleDragOver}
      onDragLeave={handleDragLeave}
      onDrop={handleDrop}
    >
      {/* Backdrop */}
      <div className="absolute inset-0 bg-black/60 backdrop-blur-sm" onClick={onClose} />

      {/* Modal */}
      <div
        ref={dialogRef}
        tabIndex={-1}
        className="relative dialog w-full max-w-4xl mx-4 h-[85vh] flex flex-col outline-none"
      >
        {/* Header */}
        <div className="flex items-center justify-between p-4 border-b border-theme-border shrink-0">
          {previewFile ? (
            <>
              <Tooltip content="Back to files">
                <button
                  onClick={() => setPreviewFile(null)}
                  className="p-2 mr-2 text-theme-text-secondary hover:text-theme-text-primary hover:bg-theme-elevated rounded"
                >
                  <ArrowLeft className="w-5 h-5" />
                </button>
              </Tooltip>
              <div className="flex-1 min-w-0">
                <h3 className="text-lg font-semibold text-theme-text-primary truncate">{previewFile.name}</h3>
                <p className="text-sm text-theme-text-secondary truncate mt-0.5">
                  {namespace}/{podName} · {selectedContainer}
                  {previewFile.size !== undefined ? ` · ${formatBytes(previewFile.size)}` : null}
                </p>
              </div>
              <Tooltip content="Download file">
                <button
                  onClick={downloadPreviewFile}
                  className="p-2 text-theme-text-secondary hover:text-theme-text-primary hover:bg-theme-elevated rounded"
                >
                  <Download className="w-5 h-5" />
                </button>
              </Tooltip>
            </>
          ) : (
            <div className="flex-1 min-w-0">
              <h3 className="text-lg font-semibold text-theme-text-primary">Pod Files</h3>
              <p className="text-sm text-theme-text-secondary truncate mt-0.5">
                {namespace}/{podName}
              </p>
            </div>
          )}

          {uploadEnabled && !previewFile && showFilesystem && (
            <>
              <PodFileUploadButton
                blocked={blockedHint}
                busy={isUploadBusy(upload)}
                loading={!listingReady}
                onClick={() => fileInputRef.current?.click()}
              />
              <input
                ref={fileInputRef}
                type="file"
                className="hidden"
                aria-hidden="true"
                tabIndex={-1}
                onChange={(e) => {
                  const file = e.target.files?.[0]
                  e.target.value = ''
                  if (file) startUpload(file)
                }}
              />
            </>
          )}

          {/* Container selector */}
          {!previewFile && containers.length > 1 && (
            <select
              value={selectedContainer}
              onChange={(e) => setSelectedContainer(e.target.value)}
              className="mr-4 px-3 py-1.5 text-sm bg-theme-base border border-theme-border rounded-lg text-theme-text-primary focus:outline-none focus:ring-2 focus:ring-blue-500"
            >
              {containers.map((c) => (
                <option key={c} value={c}>{c}</option>
              ))}
            </select>
          )}

          <button
            onClick={onClose}
            className="p-2 text-theme-text-secondary hover:text-theme-text-primary hover:bg-theme-elevated rounded ml-2"
          >
            <X className="w-5 h-5" />
          </button>
        </div>

        {/* Breadcrumb + Search */}
        <div className="p-3 border-b border-theme-border shrink-0 flex items-center gap-3">
          {/* Breadcrumb */}
          <div className="flex items-center gap-1 text-sm min-w-0 flex-shrink overflow-hidden">
            {pathSegments.map((segment, i) => {
              const segmentPath = i === 0 ? '/' : '/' + pathSegments.slice(1, i + 1).join('/')
              const isLast = i === pathSegments.length - 1
              return (
                <span key={segmentPath} className="flex items-center gap-1">
                  {i > 0 && <ChevronRight className="w-3 h-3 text-theme-text-tertiary shrink-0" />}
                  <button
                    onClick={() => !isLast && goToDirectory(segmentPath)}
                    className={clsx(
                      'truncate',
                      isLast
                        ? 'text-theme-text-primary font-medium'
                        : 'text-blue-400 hover:text-blue-300 hover:underline'
                    )}
                  >
                    {segment === '/' ? '/' : segment}
                  </button>
                </span>
              )
            })}
          </div>

          {/* Search */}
          {showFilesystem && !previewFile && (
            <div className="relative flex-1 min-w-[200px]">
              <Search className="absolute left-3 top-1/2 -translate-y-1/2 w-4 h-4 text-theme-text-tertiary" />
              <Input
                placeholder="Filter files..."
                value={searchQuery}
                onChange={(e) => setSearchQuery(e.target.value)}
                className="w-full pl-10 pr-4 py-1.5 bg-theme-base border border-theme-border rounded-lg text-sm text-theme-text-primary placeholder-theme-text-tertiary focus:outline-none focus:ring-2 focus:ring-blue-500"
              />
            </div>
          )}
        </div>

        {/* Content */}
        {previewFile ? (
          <div className="flex-1 min-h-0 flex flex-col">
            <PodFilePreview
              key={previewFile.path}
              namespace={namespace}
              podName={podName}
              container={selectedContainer}
              filePath={previewFile.path}
              fileName={previewFile.name}
              onDownload={downloadPreviewFile}
            />
          </div>
        ) : (
        <div
          ref={listRef}
          className={clsx('relative flex-1 min-h-0 overflow-y-auto p-4', dragging && 'ring-2 ring-inset ring-accent')}
        >
          {uploadEnabled && showFilesystem && (
            <>
              <PodFileUploadNotices namespace={namespace} blocked={blockedHint} onNavigateAway={onClose} />
              <PodFileUploadStatus
                state={upload}
                onConfirmReplace={confirmReplace}
                onCancel={cancelUpload}
                onDismiss={() => setUpload({ phase: 'idle' })}
              />
            </>
          )}
          {dragging && (
            <div className="pointer-events-none absolute inset-0 flex items-center justify-center bg-theme-base/80 text-sm font-medium text-theme-text-primary">
              Drop to upload into {currentPath}
            </div>
          )}

          {/* Loading */}
          {isLoading && <PaneLoader label="Loading files…" className="h-full" />}

          {/* Error */}
          {error && !isLoading && (
            <div className="p-4 bg-red-500/10 border border-red-500/30 rounded-lg">
              <div className="flex items-start gap-3">
                <AlertTriangle className="w-5 h-5 text-red-400 shrink-0 mt-0.5" />
                <div>
                  <div className="font-medium text-red-400">Failed to list files</div>
                  <div className="text-sm text-theme-text-secondary mt-1">{error}</div>
                </div>
              </div>
            </div>
          )}

          {/* File tree */}
          {showFilesystem && !isLoading && (
            <PodFileTreeView
              root={filesystem.root}
              searchQuery={searchQuery}
              namespace={namespace}
              podName={podName}
              container={selectedContainer}
              onNavigate={loadDirectory}
              onOpenFile={setPreviewFile}
            />
          )}
        </div>
        )}

        {/* Footer with stats */}
        <div className="p-3 border-t border-theme-border text-xs text-theme-text-tertiary flex items-center gap-4 shrink-0">
          {filesystem && !isLoading && (
            <>
              <span>{filesystem.totalFiles} items</span>
              <span>Container: {selectedContainer}</span>
            </>
          )}
          {onSwitchToImageFiles && (
            <button
              onClick={() => { onClose(); onSwitchToImageFiles(selectedContainer) }}
              className="ml-auto text-blue-400 hover:text-blue-300 hover:underline"
            >
              Browse static image from registry &rarr;
            </button>
          )}
        </div>
      </div>
    </div>,
    document.body,
  )
}

// ============================================================================
// Pod File Tree View
// ============================================================================

interface PodFileTreeViewProps {
  root: FileNode
  searchQuery: string
  namespace: string
  podName: string
  container: string
  onNavigate: (path: string) => void
  onOpenFile: (node: FileNode) => void
}

function PodFileTreeView({ root, searchQuery, namespace, podName, container, onNavigate, onOpenFile }: PodFileTreeViewProps) {
  const filteredRoot = useMemo(() => {
    if (!searchQuery.trim()) return root
    return filterTree(root, searchQuery.toLowerCase())
  }, [root, searchQuery])

  if (!filteredRoot || !filteredRoot.children || filteredRoot.children.length === 0) {
    return (
      <div className="text-center text-theme-text-tertiary py-8">
        {searchQuery ? 'No files match your filter' : 'Empty directory'}
      </div>
    )
  }

  return (
    <div className="font-mono text-sm">
      {filteredRoot.children.map((node) => (
        <PodFileTreeNode
          key={node.path}
          node={node}
          namespace={namespace}
          podName={podName}
          container={container}
          onNavigate={onNavigate}
          onOpenFile={onOpenFile}
        />
      ))}
    </div>
  )
}

interface PodFileTreeNodeProps {
  node: FileNode
  namespace: string
  podName: string
  container: string
  onNavigate: (path: string) => void
  onOpenFile: (node: FileNode) => void
}

function PodFileTreeNode({ node, namespace, podName, container, onNavigate, onOpenFile }: PodFileTreeNodeProps) {
  const [downloading, setDownloading] = useState(false)
  const toast = useToast()
  const isDir = node.type === 'dir'
  const isSymlink = node.type === 'symlink'
  // Size is the one preview rejection the listing can see coming. Whether a
  // file is binary or readable is only known once its bytes are read.
  const tooLarge = !isDir && (node.size ?? 0) > PREVIEW_BYTE_CAP

  const handleDownload = async (e: React.MouseEvent) => {
    e.stopPropagation()
    if (downloading) return
    setDownloading(true)
    try {
      await downloadPodFile(namespace, podName, container, node, toast)
    } finally {
      setDownloading(false)
    }
  }

  // Directories open in place; anything else opens in the viewer, which
  // reports a symlink to a directory as "not a regular file" itself.
  const handleClick = () => {
    if (isDir) onNavigate(node.path)
    else if (!tooLarge) onOpenFile(node)
  }

  return (
    <Tooltip
      content={`Too large to preview (${formatBytes(node.size ?? 0)}) — download instead`}
      disabled={!tooLarge}
      wrapperClassName="flex w-full"
      position="bottom"
    >
    <div
      className={clsx(
        'flex flex-1 min-w-0 items-center gap-1 py-0.5 px-1 rounded hover:bg-theme-elevated',
        isDir && 'font-medium',
        tooLarge ? 'cursor-default' : 'cursor-pointer'
      )}
      onClick={handleClick}
    >
      {isDir ? (
        <FolderOpen className="w-4 h-4 text-amber-400 shrink-0" />
      ) : isSymlink ? (
        <Link2 className="w-4 h-4 text-cyan-400 shrink-0" />
      ) : (
        <File className="w-4 h-4 text-theme-text-tertiary shrink-0" />
      )}

      <span className={clsx('truncate flex-1', tooLarge ? 'text-theme-text-secondary' : 'text-theme-text-primary')}>{node.name}</span>

      {isSymlink && node.linkTarget && (
        <span className="text-xs text-cyan-400 truncate max-w-48">
          -&gt; {node.linkTarget}
        </span>
      )}

      {!isDir && node.size !== undefined && (
        <span className="text-xs text-theme-text-tertiary ml-2">
          {formatBytes(node.size)}
        </span>
      )}

      {node.permissions && (
        <span className="text-xs text-theme-text-tertiary ml-2 font-normal">
          {node.permissions}
        </span>
      )}

      {!isDir && (
        <Tooltip content="Download file" wrapperClassName="ml-1">
        <button
          onClick={handleDownload}
          disabled={downloading}
          className="p-1 text-theme-text-tertiary hover:text-blue-400 hover:bg-theme-elevated rounded disabled:opacity-50 disabled:pointer-events-none"
        >
          {downloading ? (
            <Loader2 className="w-3.5 h-3.5 animate-spin" />
          ) : (
            <Download className="w-3.5 h-3.5" />
          )}
        </button>
        </Tooltip>
      )}
    </div>
    </Tooltip>
  )
}
