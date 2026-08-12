import { apiUrl } from './config'
import { apiFetch } from './client'
import type { LiveDebugAspect, LiveDebugRun } from '@skyhook-io/k8s-ui/components/resources/renderers/LiveDebugSection'

export interface IGStatus {
  state: 'installed' | 'not-installed' | 'unknown'
  namespace?: string
  version?: string
  message?: string
}

async function responseJSON<T>(response: Response): Promise<T> {
  const body = await response.json().catch(() => ({}))
  if (!response.ok) throw new Error(body.error || `Request failed (${response.status})`)
  return body as T
}

export async function fetchIGStatus(): Promise<IGStatus> {
  return responseJSON<IGStatus>(await apiFetch(apiUrl('/ig/status')))
}

export async function startIGCapture(input: {
  namespace: string
  pod: string
  container: string
  aspect: LiveDebugAspect
  durationSeconds: number
}): Promise<LiveDebugRun> {
  const path = input.aspect === 'dns' ? '/ig/runs' : '/ig/snapshots'
  return responseJSON<LiveDebugRun>(await apiFetch(apiUrl(path), {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(input),
  }))
}

export async function fetchIGRun(id: string): Promise<LiveDebugRun> {
  return responseJSON<LiveDebugRun>(await apiFetch(apiUrl(`/ig/runs/${encodeURIComponent(id)}`)))
}

export async function stopIGRun(id: string): Promise<LiveDebugRun> {
  return responseJSON<LiveDebugRun>(await apiFetch(apiUrl(`/ig/runs/${encodeURIComponent(id)}/stop`), { method: 'POST' }))
}
