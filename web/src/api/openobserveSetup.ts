import { apiUrl, getAuthHeaders, getCredentialsMode } from './config'
import { ApiError } from './client'

export interface OpenObserveLabelValue {
  label: string
  value: string
}

export interface OpenObserveMatch extends OpenObserveLabelValue {
  by: string
}

export interface OpenObserveConnection {
  baseUrl: string
  org: string
  user: string
  tokenSource: 'saved' | 'env' | 'none'
  tokenEnv?: string
  caCertFile?: string
  clusterLabel?: string
  clusters: { context: string; by: string }[]
}

export interface OpenObserveState {
  connection: OpenObserveConnection | null
  cluster: { uid?: string; error?: string; match?: OpenObserveMatch }
}

export interface OpenObserveSetupRequest {
  useSaved?: boolean
  baseUrl?: string
  org?: string
  user?: string
  token?: string
  caCertFile?: string
  clusterLabel?: string
  confirm?: OpenObserveLabelValue
  /** The cluster the page shows; the server refuses the save when Radar has moved to another. */
  clusterUid?: string
}

export interface OpenObserveSetupNotes {
  collector?: boolean
  missing?: string[]
  warning?: string
  versionWarning?: string
}

export interface OpenObserveSetupResult {
  outcome: 'saved' | 'confirm' | 'not_found'
  match?: OpenObserveMatch
  suggestion?: OpenObserveLabelValue
  reason?: string
  clusterNames?: OpenObserveLabelValue[]
  clusterUid?: string
  notes?: OpenObserveSetupNotes
}

async function request<T>(path: string, method: 'GET' | 'POST' | 'DELETE', body?: unknown, signal?: AbortSignal): Promise<T> {
  const response = await fetch(apiUrl(path), {
    method,
    signal,
    credentials: getCredentialsMode(),
    headers: { 'Content-Type': 'application/json', ...getAuthHeaders() },
    ...(body !== undefined ? { body: JSON.stringify(body) } : {}),
  })
  const data = (await response.json().catch(() => ({}))) as T & { error?: string }
  if (!response.ok) throw new ApiError(data.error || `OpenObserve request failed (${response.status})`, response.status)
  return data
}

export const getOpenObserveState = (signal?: AbortSignal) =>
  request<OpenObserveState>('/integrations/openobserve', 'GET', undefined, signal)

export const setupOpenObserve = (body: OpenObserveSetupRequest) =>
  request<OpenObserveSetupResult>('/integrations/openobserve/setup', 'POST', body)

export const removeOpenObserveCluster = (clusterUid?: string) =>
  request<OpenObserveState>(
    `/integrations/openobserve/cluster${clusterUid ? `?clusterUid=${encodeURIComponent(clusterUid)}` : ''}`,
    'DELETE',
  )

export const removeOpenObserveConnection = () =>
  request<OpenObserveState>('/integrations/openobserve', 'DELETE')
