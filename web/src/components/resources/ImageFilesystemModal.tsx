import { useState, useRef, useEffect, useMemo, useCallback } from 'react'
import { createPortal } from 'react-dom'
import { X, Folder, File, Link2, ChevronRight, ChevronDown, AlertTriangle, Loader2, Search, Download, HardDrive, Shield, ShieldCheck, Terminal, Copy, Check, RefreshCw } from 'lucide-react'
import { clsx } from 'clsx'
import { useImageMetadata, ApiError } from '../../api/client'
import type { FileNode, ImageFilesystem } from '../../types'
import { formatBytes } from '../../utils/format'
import { downloadBlob, filterTree } from './file-browser-utils'
import { apiUrl, getAuthHeaders, getCredentialsMode } from '../../api/config'

// Manual fetch function for filesystem (not a hook - gives us full control)
async function fetchImageFilesystem(
  image: string,
  namespace: string,
  podName: string,
  pullSecrets: string[]
): Promise<ImageFilesystem> {
  const params = new URLSearchParams()
  params.set('image', image)
  if (namespace) params.set('namespace', namespace)
  if (podName) params.set('pod', podName)
  if (pullSecrets.length > 0) params.set('pullSecrets', pullSecrets.join(','))

  const response = await fetch(apiUrl(`/images/inspect?${params.toString()}`), {
    credentials: getCredentialsMode(),
    headers: getAuthHeaders(),
  })
  if (!response.ok) {
    const error = await response.json().catch(() => ({ error: 'Request failed' }))
    throw new Error(error.error || `HTTP ${response.status}`)
  }
  return response.json()
}

interface ImageFilesystemModalProps {
  open: boolean
  onClose: () => void
  image: string
  namespace: string
  podName: string
  pullSecrets: string[]
  onSwitchToPodFiles?: () => void
}

export function ImageFilesystemModal({
  open,
  onClose,
  image,
  namespace,
  podName,
  pullSecrets,
  onSwitchToPodFiles,
}: ImageFilesystemModalProps) {
  const dialogRef = useRef<HTMLDivElement>(null)
  const [searchQuery, setSearchQuery] = useState('')

  // Manual fetch state (no automatic React Query fetching)
  const [filesystem, setFilesystem] = useState<ImageFilesystem | null>(null)
  const [isLoadingFilesystem, setIsLoadingFilesystem] = useState(false)
  const [filesystemError, setFilesystemError] = useState<Error | null>(null)

  // First, fetch metadata (lightweight)
  const {
    data: metadata,
    isLoading: isLoadingMetadata,
    error: metadataError,
    refetch: refetchMetadata,
  } = useImageMetadata(image, namespace, podName, pullSecrets, open)

  // Detect auth errors from metadata fetch
  const isAuthError = metadataError instanceof ApiError && metadataError.status === 401
  const registryType = isAuthError ? (metadataError.data?.registryType as string) : undefined

  // Use cached filesystem from metadata if available
  const displayFilesystem: ImageFilesystem | undefined = metadata?.cached
    ? metadata.filesystem
    : filesystem || undefined

  // Manual fetch triggered by user clicking "Download & View"
  const handleApproveDownload = useCallback(async () => {
    setIsLoadingFilesystem(true)
    setFilesystemError(null)
    try {
      const result = await fetchImageFilesystem(image, namespace, podName, pullSecrets)
      setFilesystem(result)
    } catch (err) {
      setFilesystemError(err instanceof Error ? err : new Error('Failed to fetch filesystem'))
    } finally {
      setIsLoadingFilesystem(false)
    }
  }, [image, namespace, podName, pullSecrets])

  // Reset state when modal closes or image changes
  useEffect(() => {
    if (!open) {
      setSearchQuery('')
      setFilesystem(null)
      setFilesystemError(null)
      setIsLoadingFilesystem(false)
    }
  }, [open, image])

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

  const error = metadataError || filesystemError
  const isLoading = isLoadingMetadata || isLoadingFilesystem
  // Show confirmation when: metadata loaded, not cached, no filesystem yet, no error
  const showConfirmation = metadata && !metadata.cached && !filesystem && !isLoadingFilesystem && !error
  const showFilesystem = displayFilesystem && displayFilesystem.root

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
            <h3 className="text-lg font-semibold text-theme-text-primary">Image Filesystem</h3>
            <p className="text-sm text-theme-text-secondary truncate mt-0.5" title={image}>
              {image}
            </p>
            {(displayFilesystem?.platform || metadata?.platform) && (
              <p className="text-xs text-theme-text-tertiary mt-1">
                Platform: {displayFilesystem?.platform || metadata?.platform}
              </p>
            )}
          </div>
          <button
            onClick={onClose}
            className="p-2 text-theme-text-secondary hover:text-theme-text-primary hover:bg-theme-elevated rounded ml-4"
          >
            <X className="w-5 h-5" />
          </button>
        </div>

        {/* Search bar - only show when filesystem is loaded */}
        {showFilesystem && (
          <div className="p-3 border-b border-theme-border shrink-0">
            <div className="relative">
              <Search className="absolute left-3 top-1/2 -translate-y-1/2 w-4 h-4 text-theme-text-tertiary" />
              <input
                type="text"
                placeholder="Search files..."
                value={searchQuery}
                onChange={(e) => setSearchQuery(e.target.value)}
                className="w-full pl-10 pr-4 py-2 bg-theme-base border border-theme-border rounded-lg text-sm text-theme-text-primary placeholder-theme-text-tertiary focus:outline-none focus:ring-2 focus:ring-blue-500"
              />
            </div>
          </div>
        )}

        {/* Content */}
        <div className="flex-1 overflow-y-auto p-4">
          {/* Loading state */}
          {isLoading && (
            <div className="flex flex-col items-center justify-center h-64">
              <Loader2 className="w-8 h-8 text-blue-400 animate-spin" />
              <span className="mt-3 text-theme-text-secondary">
                {isLoadingMetadata ? 'Checking image...' : 'Downloading image layers...'}
              </span>
              {isLoadingFilesystem && metadata && (
                <span className="mt-1 text-xs text-theme-text-tertiary">
                  This may take a moment for large images
                </span>
              )}
            </div>
          )}

          {/* Auth error - show guidance instead of generic error */}
          {isAuthError && (
            <AuthenticationHelp
              image={image}
              registryType={registryType}
              onRetry={() => refetchMetadata()}
            />
          )}

          {/* Non-auth errors keep the existing red error box */}
          {error && !isAuthError && (
            <div className="p-4 bg-red-500/10 border border-red-500/30 rounded-lg">
              <div className="flex items-start gap-3">
                <AlertTriangle className="w-5 h-5 text-red-400 shrink-0 mt-0.5" />
                <div>
                  <div className="font-medium text-red-400">Failed to inspect image</div>
                  <div className="text-sm text-theme-text-secondary mt-1">
                    {error instanceof Error ? error.message : 'Unknown error'}
                  </div>
                </div>
              </div>
            </div>
          )}

          {/* Confirmation dialog - image not cached */}
          {showConfirmation && (
            <DownloadConfirmation
              metadata={metadata}
              onConfirm={handleApproveDownload}
              onCancel={onClose}
            />
          )}

          {/* Filesystem tree */}
          {showFilesystem && (
            <FileTreeView
              root={displayFilesystem.root}
              searchQuery={searchQuery}
              image={image}
              namespace={namespace}
              podName={podName}
              pullSecrets={pullSecrets}
            />
          )}
        </div>

        {/* Footer with stats */}
        <div className="p-3 border-t border-theme-border text-xs text-theme-text-tertiary flex items-center gap-4 shrink-0">
          {displayFilesystem && (
            <>
              <span>{displayFilesystem.totalFiles.toLocaleString()} files</span>
              <span>{formatBytes(displayFilesystem.totalSize)}</span>
              {displayFilesystem.layers && <span>{displayFilesystem.layers.length} layers</span>}
              {displayFilesystem.digest && (
                <span className="truncate" title={displayFilesystem.digest}>
                  Digest: {displayFilesystem.digest.substring(0, 20)}...
                </span>
              )}
            </>
          )}
          {onSwitchToPodFiles && (
            <button
              onClick={() => { onClose(); onSwitchToPodFiles() }}
              className="ml-auto text-blue-400 hover:text-blue-300 hover:underline"
            >
              Browse live files from running pod &rarr;
            </button>
          )}
        </div>
      </div>
    </div>,
    document.body,
  )
}

// ============================================================================
// Download Confirmation Component
// ============================================================================

interface DownloadConfirmationProps {
  metadata: {
    image: string
    digest: string
    platform: string
    totalSize: number
    layerCount: number
    authMethod: string
  }
  onConfirm: () => void
  onCancel: () => void
}

function DownloadConfirmation({ metadata, onConfirm, onCancel }: DownloadConfirmationProps) {
  const isPublic = metadata.authMethod === 'anonymous'
  const authCommand = !isPublic ? getAuthCommand(metadata.authMethod, metadata.image) : null
  const [copied, setCopied] = useState(false)

  const handleCopy = async () => {
    if (!authCommand) return
    try {
      await navigator.clipboard.writeText(authCommand)
      setCopied(true)
      setTimeout(() => setCopied(false), 2000)
    } catch (err) {
      console.error('Failed to copy:', err)
    }
  }

  return (
    <div className="flex flex-col items-center justify-center py-8">
      <HardDrive className="w-16 h-16 text-blue-400 mb-4" />

      <h4 className="text-lg font-medium text-theme-text-primary mb-2">
        Download Image Layers?
      </h4>

      <p className="text-sm text-theme-text-secondary text-center max-w-md mb-6">
        This image is not cached locally. To view the filesystem, the image layers need to be downloaded.
      </p>

      {/* Image info */}
      <div className="bg-theme-base border border-theme-border rounded-lg p-4 mb-6 w-full max-w-sm">
        <div className="space-y-2 text-sm">
          <div className="flex justify-between">
            <span className="text-theme-text-tertiary">Download Size:</span>
            <span className="text-theme-text-primary font-medium">{formatBytes(metadata.totalSize)}</span>
          </div>
          <div className="flex justify-between">
            <span className="text-theme-text-tertiary">Layers:</span>
            <span className="text-theme-text-primary">{metadata.layerCount}</span>
          </div>
          <div className="flex justify-between">
            <span className="text-theme-text-tertiary">Platform:</span>
            <span className="text-theme-text-primary">{metadata.platform}</span>
          </div>
          <div className="flex justify-between items-center">
            <span className="text-theme-text-tertiary">Access:</span>
            <span className={clsx(
              'flex items-center gap-1',
              isPublic ? 'text-green-400' : 'text-amber-400'
            )}>
              {isPublic ? (
                <>
                  <ShieldCheck className="w-3.5 h-3.5" />
                  Public
                </>
              ) : (
                <>
                  <Shield className="w-3.5 h-3.5" />
                  {formatAuthMethod(metadata.authMethod)}
                </>
              )}
            </span>
          </div>
        </div>
      </div>

      {/* Auth command for private registries */}
      {authCommand && (
        <div className="bg-theme-base border border-amber-500/30 rounded-lg p-4 mb-6 w-full max-w-lg">
          <div className="flex items-center gap-2 text-amber-400 text-sm mb-2">
            <Terminal className="w-4 h-4" />
            <span className="font-medium">Authentication Command</span>
          </div>
          <p className="text-xs text-theme-text-secondary mb-3">
            Run this command to configure authentication for this registry:
          </p>
          <div className="relative">
            <pre className="bg-theme-elevated rounded p-3 text-xs text-theme-text-primary overflow-x-auto font-mono">
              {authCommand}
            </pre>
            <button
              onClick={handleCopy}
              className="absolute top-2 right-2 p-1.5 text-theme-text-tertiary hover:text-theme-text-primary hover:bg-theme-base rounded transition-colors"
              title="Copy to clipboard"
            >
              {copied ? (
                <Check className="w-4 h-4 text-green-400" />
              ) : (
                <Copy className="w-4 h-4" />
              )}
            </button>
          </div>
        </div>
      )}

      {/* Actions */}
      <div className="flex gap-3">
        <button
          onClick={onCancel}
          className="px-4 py-2 text-sm text-theme-text-secondary hover:text-theme-text-primary border border-theme-border rounded-lg hover:bg-theme-elevated transition-colors"
        >
          Cancel
        </button>
        <button
          onClick={onConfirm}
          className="px-4 py-2 text-sm btn-brand rounded-lg"
        >
          Download & View
        </button>
      </div>
    </div>
  )
}

// ============================================================================
// Authentication Help Component (shown on 401 errors)
// ============================================================================

interface AuthenticationHelpProps {
  image: string
  registryType?: string
  onRetry: () => void
}

function AuthenticationHelp({ image, registryType, onRetry }: AuthenticationHelpProps) {
  const [copied, setCopied] = useState(false)
  const [retrying, setRetrying] = useState(false)
  const registry = getRegistryHost(image)
  const authMethod = registryType || 'generic'
  const authCommand = getAuthCommand(authMethod, image)

  const handleCopy = async () => {
    if (!authCommand) return
    try {
      await navigator.clipboard.writeText(authCommand)
      setCopied(true)
      setTimeout(() => setCopied(false), 2000)
    } catch (err) {
      console.error('Failed to copy:', err)
    }
  }

  const handleRetry = () => {
    setRetrying(true)
    onRetry()
    // Reset after a short delay (the query state will update via React Query)
    setTimeout(() => setRetrying(false), 1000)
  }

  return (
    <div className="flex flex-col items-center justify-center py-8">
      <Shield className="w-16 h-16 text-amber-400 mb-4" />

      <h4 className="text-lg font-medium text-theme-text-primary mb-2">
        Authentication Required
      </h4>

      <p className="text-sm text-theme-text-secondary text-center max-w-md mb-2">
        This image is hosted on a private registry that requires authentication.
      </p>

      <p className="text-xs text-theme-text-tertiary text-center max-w-md mb-6">
        Registry: <span className="font-mono text-theme-text-secondary">{registry}</span>
        {registryType && registryType !== 'generic' && (
          <> ({formatAuthMethod(registryType)})</>
        )}
      </p>

      {/* Auth command */}
      {authCommand && (
        <div className="bg-theme-base border border-amber-500/30 rounded-lg p-4 mb-4 w-full max-w-lg">
          <div className="flex items-center gap-2 text-amber-400 text-sm mb-2">
            <Terminal className="w-4 h-4" />
            <span className="font-medium">Run this command to authenticate</span>
          </div>
          <div className="relative">
            <pre className="bg-theme-elevated rounded p-3 text-xs text-theme-text-primary overflow-x-auto font-mono">
              {authCommand}
            </pre>
            <button
              onClick={handleCopy}
              className="absolute top-2 right-2 p-1.5 text-theme-text-tertiary hover:text-theme-text-primary hover:bg-theme-base rounded transition-colors"
              title="Copy to clipboard"
            >
              {copied ? (
                <Check className="w-4 h-4 text-green-400" />
              ) : (
                <Copy className="w-4 h-4" />
              )}
            </button>
          </div>
        </div>
      )}

      {/* Explanation */}
      <p className="text-xs text-theme-text-tertiary text-center max-w-md mb-6">
        Radar uses your local Docker credentials (<span className="font-mono">~/.docker/config.json</span>).
        Run the command above in your terminal, then click Retry.
      </p>

      {/* Retry button */}
      <button
        onClick={handleRetry}
        disabled={retrying}
        className="flex items-center gap-2 px-4 py-2 text-sm btn-brand rounded-lg"
      >
        <RefreshCw className={clsx('w-4 h-4', retrying && 'animate-spin')} />
        {retrying ? 'Retrying...' : 'Retry'}
      </button>
    </div>
  )
}

function formatAuthMethod(method: string): string {
  switch (method) {
    case 'google': return 'Google Cloud'
    case 'aws': return 'AWS ECR'
    case 'azure': return 'Azure ACR'
    case 'github': return 'GitHub'
    case 'docker': return 'Docker Hub'
    case 'quay': return 'Quay.io'
    case 'gitlab': return 'GitLab'
    case 'credentials': return 'Authenticated'
    default: return 'Authenticated'
  }
}

function getRegistryHost(img: string): string {
  // Handle images like "nginx" (Docker Hub official)
  if (!img.includes('/') || !img.split('/')[0].includes('.')) {
    return 'docker.io'
  }
  // Extract hostname from "hostname/path/image:tag"
  return img.split('/')[0]
}

function getAuthCommand(authMethod: string, image: string): string | null {
  const registry = getRegistryHost(image)

  switch (authMethod) {
    case 'google':
      // Extract the specific registry (gcr.io, us-docker.pkg.dev, etc.)
      return `gcloud auth configure-docker ${registry} --quiet`

    case 'aws': {
      // Extract region from ECR URL: <account>.dkr.ecr.<region>.amazonaws.com
      const match = registry.match(/\.dkr\.ecr\.([^.]+)\.amazonaws\.com/)
      const region = match ? match[1] : '<region>'
      return `aws ecr get-login-password --region ${region} | docker login --username AWS --password-stdin ${registry}`
    }

    case 'azure': {
      // Extract ACR name from <name>.azurecr.io
      const acrMatch = registry.match(/^([^.]+)\.azurecr\.io/)
      const acrName = acrMatch ? acrMatch[1] : '<acr-name>'
      return `az acr login --name ${acrName}`
    }

    case 'github':
      return `echo $GITHUB_TOKEN | docker login ghcr.io -u <username> --password-stdin`

    case 'docker':
      return `docker login`

    case 'quay':
      return `docker login quay.io`

    case 'gitlab':
      return `docker login registry.gitlab.com -u <username> -p <access-token>`

    case 'generic':
    case 'credentials':
      return `docker login ${registry}`

    default:
      return `docker login ${registry}`
  }
}

// ============================================================================
// File Tree View Component
// ============================================================================

interface FileTreeViewProps {
  root: FileNode
  searchQuery: string
  image: string
  namespace: string
  podName: string
  pullSecrets: string[]
}

function FileTreeView({ root, searchQuery, image, namespace, podName, pullSecrets }: FileTreeViewProps) {
  const filteredRoot = useMemo(() => {
    if (!searchQuery.trim()) return root
    return filterTree(root, searchQuery.toLowerCase())
  }, [root, searchQuery])

  if (!filteredRoot || !filteredRoot.children || filteredRoot.children.length === 0) {
    return (
      <div className="text-center text-theme-text-tertiary py-8">
        {searchQuery ? 'No files match your search' : 'Empty filesystem'}
      </div>
    )
  }

  return (
    <div className="font-mono text-sm">
      {filteredRoot.children.map((node) => (
        <FileTreeNode
          key={node.path}
          node={node}
          depth={0}
          defaultExpanded={!searchQuery}
          image={image}
          namespace={namespace}
          podName={podName}
          pullSecrets={pullSecrets}
        />
      ))}
    </div>
  )
}

interface FileTreeNodeProps {
  node: FileNode
  depth: number
  defaultExpanded?: boolean
  image: string
  namespace: string
  podName: string
  pullSecrets: string[]
}

function FileTreeNode({ node, depth, defaultExpanded = true, image, namespace, podName, pullSecrets }: FileTreeNodeProps) {
  const [expanded, setExpanded] = useState(defaultExpanded && depth < 2)
  const [downloading, setDownloading] = useState(false)
  const isDir = node.type === 'dir'
  const isSymlink = node.type === 'symlink'
  const isFile = node.type === 'file'

  const handleDownload = async (e: React.MouseEvent) => {
    e.stopPropagation()
    if (downloading) return

    setDownloading(true)
    try {
      const params = new URLSearchParams()
      params.set('image', image)
      params.set('path', node.path)
      if (namespace) params.set('namespace', namespace)
      if (podName) params.set('pod', podName)
      if (pullSecrets.length > 0) params.set('pullSecrets', pullSecrets.join(','))

      const response = await fetch(apiUrl(`/images/file?${params.toString()}`), {
        credentials: getCredentialsMode(),
        headers: getAuthHeaders(),
      })
      if (!response.ok) {
        throw new Error('Failed to download file')
      }

      const blob = await response.blob()
      await downloadBlob(blob, node.name)
    } catch (err) {
      console.error('Download failed:', err)
    } finally {
      setDownloading(false)
    }
  }

  const handleClick = () => {
    if (isDir) {
      setExpanded(!expanded)
    }
  }

  return (
    <div>
      <div
        className={clsx(
          'flex items-center gap-1 py-0.5 px-1 rounded hover:bg-theme-elevated',
          isDir && 'font-medium cursor-pointer'
        )}
        style={{ paddingLeft: `${depth * 16 + 4}px` }}
        onClick={handleClick}
      >
        {isDir && (
          <span className="w-4 h-4 flex items-center justify-center">
            {expanded ? (
              <ChevronDown className="w-3.5 h-3.5 text-theme-text-tertiary" />
            ) : (
              <ChevronRight className="w-3.5 h-3.5 text-theme-text-tertiary" />
            )}
          </span>
        )}
        {!isDir && <span className="w-4" />}

        {isDir ? (
          <Folder className="w-4 h-4 text-amber-400 shrink-0" />
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

        {isFile && (
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

      {isDir && expanded && node.children && (
        <div>
          {node.children.map((child) => (
            <FileTreeNode
              key={child.path}
              node={child}
              depth={depth + 1}
              defaultExpanded={defaultExpanded}
              image={image}
              namespace={namespace}
              podName={podName}
              pullSecrets={pullSecrets}
            />
          ))}
        </div>
      )}
    </div>
  )
}

