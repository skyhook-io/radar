import { useState, useRef, useEffect, useMemo, useCallback } from 'react'
import { createPortal } from 'react-dom'
import { X, File, Link2, ChevronRight, AlertTriangle, Loader2, Search, Download, FolderOpen } from 'lucide-react'
import { clsx } from 'clsx'
import type { FileNode } from '../../types'
import { formatBytes } from '../../utils/format'
import { downloadBlob, filterTree } from './file-browser-utils'

const API_BASE = '/api'

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

  const response = await fetch(`${API_BASE}/pods/${namespace}/${podName}/files?${params.toString()}`)
  if (!response.ok) {
    const error = await response.json().catch(() => ({ error: 'Request failed' }))
    throw new Error(error.error || `HTTP ${response.status}`)
  }
  return response.json()
}

interface PodFilesystemModalProps {
  open: boolean
  onClose: () => void
  namespace: string
  podName: string
  containers: string[]
  initialContainer?: string
  onSwitchToImageFiles?: () => void
}

export function PodFilesystemModal({
  open,
  onClose,
  namespace,
  podName,
  containers,
  initialContainer,
  onSwitchToImageFiles,
}: PodFilesystemModalProps) {
  const dialogRef = useRef<HTMLDivElement>(null)
  const [searchQuery, setSearchQuery] = useState('')
  const [selectedContainer, setSelectedContainer] = useState(initialContainer || containers[0] || '')
  const [currentPath, setCurrentPath] = useState('/')
  const [filesystem, setFilesystem] = useState<PodFilesystem | null>(null)
  const [isLoading, setIsLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const loadDirectory = useCallback(async (dirPath: string) => {
    setIsLoading(true)
    setError(null)
    try {
      const result = await fetchPodFiles(namespace, podName, selectedContainer, dirPath)
      setFilesystem(result)
      setCurrentPath(dirPath)
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to list files')
    } finally {
      setIsLoading(false)
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
      setError(null)
      setIsLoading(false)
      setCurrentPath('/')
      setSelectedContainer(initialContainer || containers[0] || '')
    }
  }, [open, initialContainer, containers])

  // Handle ESC key
  useEffect(() => {
    if (!open) return
    const handleKeyDown = (e: KeyboardEvent) => {
      if (e.key === 'Escape') { e.stopPropagation(); onClose() }
    }
    document.addEventListener('keydown', handleKeyDown, true)
    return () => document.removeEventListener('keydown', handleKeyDown, true)
  }, [open, onClose])

  // Focus trap
  useEffect(() => {
    if (open && dialogRef.current) {
      dialogRef.current.focus()
    }
  }, [open])

  if (!open) return null

  const showFilesystem = filesystem && filesystem.root

  // Build breadcrumb segments
  const pathSegments = currentPath === '/'
    ? ['/']
    : ['/', ...currentPath.split('/').filter(Boolean)]

  return createPortal(
    <div className="fixed inset-0 z-[100] flex items-center justify-center">
      {/* Backdrop */}
      <div className="absolute inset-0 bg-black/60 backdrop-blur-sm" onClick={onClose} />

      {/* Modal */}
      <div
        ref={dialogRef}
        tabIndex={-1}
        className="relative dialog w-full max-w-4xl mx-4 max-h-[85vh] flex flex-col outline-none"
      >
        {/* Header */}
        <div className="flex items-center justify-between p-4 border-b border-theme-border shrink-0">
          <div className="flex-1 min-w-0">
            <h3 className="text-lg font-semibold text-theme-text-primary">Pod Files</h3>
            <p className="text-sm text-theme-text-secondary truncate mt-0.5">
              {namespace}/{podName}
            </p>
          </div>

          {/* Container selector */}
          {containers.length > 1 && (
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
                    onClick={() => !isLast && loadDirectory(segmentPath)}
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
          {showFilesystem && (
            <div className="relative flex-1 min-w-[200px]">
              <Search className="absolute left-3 top-1/2 -translate-y-1/2 w-4 h-4 text-theme-text-tertiary" />
              <input
                type="text"
                placeholder="Filter files..."
                value={searchQuery}
                onChange={(e) => setSearchQuery(e.target.value)}
                className="w-full pl-10 pr-4 py-1.5 bg-theme-base border border-theme-border rounded-lg text-sm text-theme-text-primary placeholder-theme-text-tertiary focus:outline-none focus:ring-2 focus:ring-blue-500"
              />
            </div>
          )}
        </div>

        {/* Content */}
        <div className="flex-1 overflow-y-auto p-4">
          {/* Loading */}
          {isLoading && (
            <div className="flex flex-col items-center justify-center h-64">
              <Loader2 className="w-8 h-8 text-blue-400 animate-spin" />
              <span className="mt-3 text-theme-text-secondary">Loading files...</span>
            </div>
          )}

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
            />
          )}
        </div>

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
              onClick={() => { onClose(); onSwitchToImageFiles() }}
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
}

function PodFileTreeView({ root, searchQuery, namespace, podName, container, onNavigate }: PodFileTreeViewProps) {
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
}

function PodFileTreeNode({ node, namespace, podName, container, onNavigate }: PodFileTreeNodeProps) {
  const [downloading, setDownloading] = useState(false)
  const isDir = node.type === 'dir'
  const isSymlink = node.type === 'symlink'
  const isDownloadable = !isDir // files and symlinks can be downloaded

  const handleDownload = async (e: React.MouseEvent) => {
    e.stopPropagation()
    if (downloading) return

    setDownloading(true)
    try {
      const params = new URLSearchParams()
      params.set('container', container)
      params.set('path', node.path)

      const response = await fetch(`${API_BASE}/pods/${namespace}/${podName}/files/download?${params.toString()}`)
      if (!response.ok) {
        const err = await response.json().catch(() => ({ error: 'Download failed' }))
        throw new Error(err.error || `HTTP ${response.status}`)
      }

      const blob = await response.blob()
      downloadBlob(blob, node.name)
    } catch (err) {
      console.error('Download failed:', err)
    } finally {
      setDownloading(false)
    }
  }

  const handleClick = () => {
    if (isDir) {
      onNavigate(node.path)
    }
  }

  return (
    <div
      className={clsx(
        'flex items-center gap-1 py-0.5 px-1 rounded hover:bg-theme-elevated',
        isDir && 'font-medium cursor-pointer'
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

      <span className="text-theme-text-primary truncate flex-1">{node.name}</span>

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

      {isDownloadable && (
        <button
          onClick={handleDownload}
          disabled={downloading}
          className="p-1 text-theme-text-tertiary hover:text-blue-400 hover:bg-theme-elevated rounded ml-1 disabled:opacity-50"
          title="Download file"
        >
          {downloading ? (
            <Loader2 className="w-3.5 h-3.5 animate-spin" />
          ) : (
            <Download className="w-3.5 h-3.5" />
          )}
        </button>
      )}
    </div>
  )
}

