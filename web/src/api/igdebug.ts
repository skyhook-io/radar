import { fetchJSON } from './client'
import type { LiveDebugAspect, LiveDebugRun } from '@skyhook-io/k8s-ui/components/resources/renderers/LiveDebugSection'

export interface IGStatus {
  state: 'installed' | 'not-installed' | 'unknown'
  namespace?: string
  version?: string
  message?: string
}

export async function fetchIGStatus(): Promise<IGStatus> {
  return fetchJSON<IGStatus>('/ig/status')
}

export async function startIGCapture(input: {
  namespace: string
  pod: string
  podUid: string
  containerId?: string
  container: string
  aspect: LiveDebugAspect
  durationSeconds: number
}): Promise<LiveDebugRun> {
  const path = input.aspect === 'dns' ? '/ig/runs' : '/ig/snapshots'
  return fetchJSON<LiveDebugRun>(path, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(input),
  })
}

export async function fetchIGRun(id: string, signal?: AbortSignal): Promise<LiveDebugRun> {
  return fetchJSON<LiveDebugRun>(`/ig/runs/${encodeURIComponent(id)}`, { signal })
}

export async function stopIGRun(id: string): Promise<LiveDebugRun> {
  return fetchJSON<LiveDebugRun>(`/ig/runs/${encodeURIComponent(id)}/stop`, { method: 'POST' })
}
