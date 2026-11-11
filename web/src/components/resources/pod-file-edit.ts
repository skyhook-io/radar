import { apiUrl, getAuthHeaders, getCredentialsMode } from '../../api/config'
import { fileMountHint, uploadBlockedHint, type MountHint } from './pod-file-upload'

export interface EditGateInput {
  canExec: boolean
  featureSupported: boolean
}

/** Editing rides on exec like uploads; the feature check keeps it off an older Radar. */
export function canShowEdit({ canExec, featureSupported }: EditGateInput): boolean {
  return canExec && featureSupported
}

export type LineEndings = 'none' | 'lf' | 'crlf' | 'mixed'

/**
 * The editor keeps one line ending for the whole file, so a file that mixes
 * them would be rewritten with one style on save. Those stay read-only.
 */
export function lineEndings(text: string): LineEndings {
  const crlf = (text.match(/\r\n/g) ?? []).length
  const lf = (text.match(/\n/g) ?? []).length - crlf
  const cr = (text.match(/\r/g) ?? []).length - crlf
  if (cr > 0 || (crlf > 0 && lf > 0)) return 'mixed'
  if (crlf > 0) return 'crlf'
  if (lf > 0) return 'lf'
  return 'none'
}

function dirOf(filePath: string): string {
  const i = filePath.lastIndexOf('/')
  return i <= 0 ? '/' : filePath.slice(0, i)
}

/** Why this file can't be edited here, from what the browser can see, or null. A hint: the server decides. */
export function editBlockedReason(pod: unknown, container: string, filePath: string, content: string | null): string | null {
  const mounted: MountHint | null = fileMountHint(pod, container, filePath) ?? uploadBlockedHint(pod, container, dirOf(filePath))
  if (mounted) return mounted.reason
  if (content !== null && lineEndings(content) === 'mixed') {
    return 'This file mixes line endings, and saving from the editor would rewrite them all one way.'
  }
  return null
}

export interface SaveResult {
  path: string
  size: number
  sha256: string
}

export class PodFileSaveError extends Error {
  readonly code: string
  readonly status: number
  constructor(message: string, code: string, status: number) {
    super(message)
    this.name = 'PodFileSaveError'
    this.code = code
    this.status = status
  }
}

export const FILE_CHANGED = 'file_changed'

export interface SaveRequest {
  namespace: string
  podName: string
  container: string
  filePath: string
  /** SHA-256 of the bytes the editor was opened on, as the preview returned it. */
  sha256: string
  content: string
}

export async function savePodFileContent(req: SaveRequest): Promise<SaveResult> {
  const params = new URLSearchParams({ container: req.container, path: req.filePath, sha256: req.sha256 })
  const url = apiUrl(`/pods/${encodeURIComponent(req.namespace)}/${encodeURIComponent(req.podName)}/files/content?${params}`)
  let response: Response
  try {
    response = await fetch(url, {
      method: 'PUT',
      credentials: getCredentialsMode(),
      headers: { ...getAuthHeaders(), 'Content-Type': 'text/plain; charset=utf-8' },
      body: new TextEncoder().encode(req.content),
    })
  } catch (err) {
    throw new PodFileSaveError(err instanceof Error ? err.message : 'The save could not reach Radar', 'network_error', 0)
  }
  let body: ({ error?: string; error_code?: string } & Partial<SaveResult>) | null = null
  try {
    body = await response.json()
  } catch {
    // keep the status alone
  }
  if (response.ok && body?.sha256) return body as SaveResult
  throw new PodFileSaveError(body?.error || `Save failed (HTTP ${response.status})`, body?.error_code || 'save_failed', response.status)
}
