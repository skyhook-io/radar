import type { FileNode } from '../../types'
import { apiUrl, getAuthHeaders, getCredentialsMode } from '../../api/config'
import { formatBytes } from '../../utils/format'

export const POD_FILE_PERSISTENCE_NOTE =
  "Changes apply to the running container immediately. Files in the container's own filesystem are lost when it's recreated; files on volumes may persist. For lasting changes, update the source (image, ConfigMap, manifest)."

/** Mirrors the server's podFileUploadMaxBytes, so an oversize file is refused before it is sent. */
export const POD_FILE_UPLOAD_MAX_BYTES = 100 * 1024 * 1024

export interface UploadGateInput {
  canExec: boolean
  featureSupported: boolean
}

/**
 * Uploads ride on exec: anyone who can exec can already write through the
 * terminal. The feature check keeps them off an older Radar without the endpoint.
 */
export function canShowUpload({ canExec, featureSupported }: UploadGateInput): boolean {
  return canExec && featureSupported
}

interface VolumeMountLike {
  name?: string
  mountPath?: string
  readOnly?: boolean
}

interface ContainerLike {
  name?: string
  volumeMounts?: VolumeMountLike[]
  securityContext?: { readOnlyRootFilesystem?: boolean }
}

interface ProjectionLike {
  configMap?: { name?: string }
  secret?: { name?: string }
}

interface VolumeLike {
  name?: string
  configMap?: { name?: string }
  secret?: { secretName?: string }
  projected?: { sources?: ProjectionLike[] }
  downwardAPI?: object
}

interface PodLike {
  spec?: {
    containers?: ContainerLike[]
    initContainers?: ContainerLike[]
    ephemeralContainers?: ContainerLike[]
    volumes?: VolumeLike[]
  }
}

export interface MountSource {
  kind: 'ConfigMap' | 'Secret'
  name: string
}

export interface MountHint {
  reason: string
  source?: MountSource
}

function findContainer(pod: unknown, container: string): ContainerLike | undefined {
  const spec = (pod as PodLike | undefined)?.spec ?? {}
  // Native sidecars are init containers with restartPolicy Always; they are
  // browsable, so their mounts count too.
  for (const list of [spec.containers, spec.initContainers, spec.ephemeralContainers]) {
    const found = (list ?? []).find((c) => c?.name === container)
    if (found) return found
  }
  return undefined
}

function covers(mountPath: string, dir: string): boolean {
  const mp = mountPath.length > 1 ? mountPath.replace(/\/+$/, '') : mountPath
  return mp === '/' || dir === mp || dir.startsWith(mp + '/')
}

function managedVolumeHint(volume: VolumeLike | undefined, mountPath: string): MountHint | null {
  if (volume?.configMap) {
    return {
      reason: `${mountPath} is a ConfigMap volume. Kubernetes manages these files and they are read-only; change the ConfigMap instead.`,
      source: volume.configMap.name ? { kind: 'ConfigMap', name: volume.configMap.name } : undefined,
    }
  }
  if (volume?.secret) {
    return {
      reason: `${mountPath} is a Secret volume. Kubernetes manages these files and they are read-only; change the Secret instead.`,
      source: volume.secret.secretName ? { kind: 'Secret', name: volume.secret.secretName } : undefined,
    }
  }
  if (volume?.projected) {
    const sources = volume.projected.sources ?? []
    const single = sources.length === 1 ? sources[0] : undefined
    let source: MountSource | undefined
    if (single?.configMap?.name) source = { kind: 'ConfigMap', name: single.configMap.name }
    else if (single?.secret?.name) source = { kind: 'Secret', name: single.secret.name }
    return {
      reason: `${mountPath} is a projected volume. Kubernetes manages these files and they are read-only; change their source instead.`,
      source,
    }
  }
  if (volume?.downwardAPI) {
    return { reason: `${mountPath} is a downward API volume. Kubernetes writes these files from the pod's own fields.` }
  }
  return null
}

/**
 * Why uploading into `dir` of `container` cannot work, read from the pod spec,
 * or null when nothing in the spec says so. A hint only: the server is the
 * authority and reports what actually happened.
 */
export function uploadBlockedHint(pod: unknown, container: string, dir: string): MountHint | null {
  const c = findContainer(pod, container)
  if (!c) return null
  const volumes = (pod as PodLike | undefined)?.spec?.volumes ?? []
  const mounts = (c.volumeMounts ?? [])
    .filter((m): m is VolumeMountLike & { mountPath: string } => typeof m?.mountPath === 'string' && covers(m.mountPath, dir))
    .sort((a, b) => b.mountPath.length - a.mountPath.length)
  const mount = mounts[0]
  if (mount) {
    const volume = volumes.find((v) => v?.name === mount.name)
    const managed = managedVolumeHint(volume, mount.mountPath)
    if (managed) return managed
    if (mount.readOnly) return { reason: `${mount.mountPath} is mounted read-only.` }
    return null
  }
  if (c.securityContext?.readOnlyRootFilesystem) {
    return { reason: "This container's root filesystem is read-only (readOnlyRootFilesystem), and this directory is not on a writable volume." }
  }
  return null
}

/** A volume mounted onto exactly `filePath` (a subPath file mount), which no rename can replace. */
export function fileMountHint(pod: unknown, container: string, filePath: string): MountHint | null {
  const c = findContainer(pod, container)
  const mount = (c?.volumeMounts ?? []).find((m) => m?.mountPath === filePath)
  if (!mount) return null
  const volume = (pod as PodLike | undefined)?.spec?.volumes?.find((v) => v?.name === mount.name)
  const managed = managedVolumeHint(volume, filePath)
  return {
    reason: `${filePath} is mounted into the container on its own, so it cannot be replaced here.${managed?.source ? ` Change the ${managed.source.kind} instead.` : ''}`,
    source: managed?.source,
  }
}

export type UploadTarget =
  | { kind: 'new' }
  | { kind: 'replace' }
  | { kind: 'refuse'; reason: string }

/** What uploading `name` into the listed directory would do, from the listing. */
export function uploadTarget(listing: FileNode | undefined, name: string): UploadTarget {
  const existing = listing?.children?.find((c) => c.name === name)
  if (!existing) return { kind: 'new' }
  if (existing.type === 'dir') return { kind: 'refuse', reason: `${name} is a directory here.` }
  if (existing.type === 'symlink') return { kind: 'refuse', reason: `${name} is a symlink here; uploads never follow symlinks.` }
  return { kind: 'replace' }
}

/** Why a file is over the upload cap, or null when it fits. */
export function oversizeMessage(size: number, max: number = POD_FILE_UPLOAD_MAX_BYTES): string | null {
  if (size <= max) return null
  return `This file is ${formatBytes(size)}; uploads on this Radar are limited to ${formatBytes(max)}.`
}

/** A single path segment the server will accept as a file name. */
export function isValidUploadName(name: string): boolean {
  if (name === '' || name === '.' || name === '..') return false
  if (name.includes('/') || name.includes('\0')) return false
  return new TextEncoder().encode(name).length <= 255
}

export interface UploadResult {
  path: string
  size: number
  replaced: boolean
}

export class PodFileUploadError extends Error {
  readonly code: string
  readonly status: number
  constructor(message: string, code: string, status: number) {
    super(message)
    this.name = 'PodFileUploadError'
    this.code = code
    this.status = status
  }
}

export const UPLOAD_CANCELLED = 'upload_cancelled'

export interface UploadRequest {
  namespace: string
  podName: string
  container: string
  dir: string
  file: Blob
  name: string
  overwrite: boolean
  onProgress?: (sent: number, total: number) => void
  signal?: AbortSignal
}

/**
 * Sends one file. XHR rather than fetch because fetch reports no upload
 * progress.
 */
export function uploadPodFile(req: UploadRequest): Promise<UploadResult> {
  const params = new URLSearchParams({ container: req.container, dir: req.dir, name: req.name })
  if (req.overwrite) params.set('overwrite', 'true')
  const url = apiUrl(`/pods/${encodeURIComponent(req.namespace)}/${encodeURIComponent(req.podName)}/files/upload?${params}`)

  return new Promise((resolve, reject) => {
    const xhr = new XMLHttpRequest()
    xhr.open('POST', url)
    xhr.withCredentials = getCredentialsMode() === 'include'
    for (const [k, v] of Object.entries(getAuthHeaders())) xhr.setRequestHeader(k, v)
    xhr.setRequestHeader('Content-Type', 'application/octet-stream')
    xhr.upload.onprogress = (e) => {
      if (e.lengthComputable) req.onProgress?.(e.loaded, e.total)
    }
    xhr.onload = () => {
      let body: { error?: string; error_code?: string } & Partial<UploadResult> | null = null
      try {
        body = JSON.parse(xhr.responseText)
      } catch {
        // fall through with the status alone
      }
      if (xhr.status >= 200 && xhr.status < 300 && body) {
        resolve(body as UploadResult)
        return
      }
      reject(new PodFileUploadError(body?.error || `Upload failed (HTTP ${xhr.status})`, body?.error_code || 'upload_failed', xhr.status))
    }
    xhr.onerror = () => reject(new PodFileUploadError('The upload could not reach Radar', 'network_error', 0))
    xhr.onabort = () => reject(new PodFileUploadError('Upload cancelled', UPLOAD_CANCELLED, 0))
    if (req.signal) {
      if (req.signal.aborted) {
        xhr.abort()
        return
      }
      req.signal.addEventListener('abort', () => xhr.abort(), { once: true })
    }
    xhr.send(req.file)
  })
}
