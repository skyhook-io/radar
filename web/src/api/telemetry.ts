import { useEffect } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { fetchJSON } from './client'
import { apiUrl, getApiBase, getAuthHeaders, getCredentialsMode } from './config'

export type UsageDataState = 'undecided' | 'on' | 'off' | 'log'
export type UsageDataSource = 'default' | 'user' | 'env' | 'do-not-track' | 'deployment'

export interface ClusterShape {
  kubernetesVersion: string
  platform: string
  nodes: string
  integrations: string[]
}

export interface UsageReport {
  schema: number
  version: string
  os: string
  arch: string
  installMethod: string
  mode: string
  periodStart: string
  periodEnd: string
  setup: {
    authMode: string
    timelineStorage: string
    mcpEnabled: boolean
    prometheus: string
    costSource: string
    browsers: string[]
  }
  engagement: { sessions: number; activeMinutes: string }
  views: Record<string, number>
  actions: Record<string, number>
  mcpTools: Record<string, number>
  uiEvents: Record<string, number>
  errors: Record<string, number>
  clusters: { contexts: string; used: number; shapes: ClusterShape[] }
}

export interface UsageDataStatus {
  state: UsageDataState
  source: UsageDataSource
  canChange: boolean
  endpoint: string
  developmentBuild: boolean
  nextReportAt?: string
  lastSentAt?: string
  preview: UsageReport
  firstRunPrompt: boolean
  // What's New may ask: never asked, or asked and left unanswered long enough ago.
  ask: boolean
  // The choice covers everyone using this Radar (in-cluster, sign-in).
  shared: boolean
  // On a shared Radar, people who can change its Deployment decide in the UI.
  ownersDecide: boolean
  decidedBy?: string
  decidedAt?: string
  // Where the choice is kept; "pod" means a restart resets it.
  choiceStorage: 'local' | 'cluster' | 'pod'
}

const usageDataKey = (apiBase: string) => ['usage-data', apiBase]

// `fresh` is for the report preview: it must match what would be sent now, so
// it refetches every time it is shown instead of reusing the app shell's copy.
export function useUsageData(enabled = true, { fresh = false }: { fresh?: boolean } = {}) {
  const apiBase = getApiBase()
  return useQuery<UsageDataStatus>({
    queryKey: usageDataKey(apiBase),
    queryFn: () => fetchJSON<UsageDataStatus>('/telemetry'),
    enabled,
    staleTime: fresh ? 0 : 60_000,
    refetchOnMount: fresh ? 'always' : true,
    retry: false,
  })
}

export function useSetUsageData() {
  const queryClient = useQueryClient()
  const apiBase = getApiBase()
  return useMutation({
    mutationFn: (enabled: boolean) =>
      fetchJSON<UsageDataStatus>('/telemetry', {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ enabled }),
      }),
    meta: { errorMessage: 'Failed to save usage data choice' },
    onSuccess: (status) => {
      queryClient.setQueryData(usageDataKey(apiBase), status)
    },
  })
}

// Records that the question was shown, answered or not. Fire and forget: a
// failure only means it might show once more.
export function markUsagePromptShown(): void {
  void fetch(apiUrl('/telemetry/prompt-shown'), {
    method: 'POST',
    headers: getAuthHeaders(),
    credentials: getCredentialsMode(),
  }).catch(() => {})
}

export function isRecording(status: UsageDataStatus | undefined): boolean {
  return status?.state === 'on' || status?.state === 'log'
}

// Set from the status query so code without hook access (error boundaries,
// event handlers) can skip the request entirely when usage data is off.
let recording = false

export function setUsageRecording(on: boolean) {
  recording = on
}

type UsageEvent =
  | { type: 'view'; name: string }
  | { type: 'ui'; name: string }
  | { type: 'session' }
  | { type: 'active'; minutes: number }

// Fire and forget; the server drops anything outside its allow-list.
export function recordUsageEvent(event: UsageEvent): void {
  if (!recording) return
  void fetch(apiUrl('/telemetry/event'), {
    method: 'POST',
    headers: { ...getAuthHeaders(), 'Content-Type': 'application/json' },
    credentials: getCredentialsMode(),
    body: JSON.stringify(event),
    keepalive: true,
  }).catch(() => {})
}

const ACTIVE_TICK_MINUTES = 5

// Counts views, one session per page load, and visible time in five-minute
// ticks. Nothing is sent unless usage data is on.
export function useUsageRecording(view: string, status: UsageDataStatus | undefined) {
  const on = isRecording(status)
  useEffect(() => {
    setUsageRecording(on)
  }, [on])

  useEffect(() => {
    if (on) recordUsageEvent({ type: 'view', name: view })
  }, [view, on])

  useEffect(() => {
    if (!on) return
    recordUsageEvent({ type: 'session' })
    const tick = window.setInterval(() => {
      if (document.visibilityState === 'visible') {
        recordUsageEvent({ type: 'active', minutes: ACTIVE_TICK_MINUTES })
      }
    }, ACTIVE_TICK_MINUTES * 60_000)
    return () => window.clearInterval(tick)
  }, [on])
}
