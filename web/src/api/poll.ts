import { useQuery } from '@tanstack/react-query'
import { apiUrl, getAuthHeaders, getCredentialsMode } from './config'
import type { PollAnswers } from '../components/poll/pollDefinition'

export interface PollStatus {
  eligible: boolean
  reason?: string
  round: string
  mode: 'local' | 'in-cluster' | 'cloud'
}

export interface PollSubmitResult {
  answers: 'ok' | 'skipped'
  contact: 'ok' | 'failed' | 'none'
}

export class PollSubmitError extends Error {
  constructor(message: string, readonly status: number) {
    super(message)
  }
}

export function usePollStatus(enabled: boolean) {
  return useQuery<PollStatus>({
    queryKey: ['poll-status'],
    queryFn: async () => {
      const res = await fetch(apiUrl('/poll'), { credentials: getCredentialsMode(), headers: getAuthHeaders() })
      if (!res.ok) throw new Error(`HTTP ${res.status}`)
      return res.json()
    },
    enabled,
    staleTime: Infinity,
    retry: false,
  })
}

async function post(path: string, body: unknown): Promise<Response> {
  return fetch(apiUrl(path), {
    method: 'POST',
    credentials: getCredentialsMode(),
    headers: { ...getAuthHeaders(), 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  })
}

export function recordPollShown(): void {
  void post('/poll/shown', {}).catch(() => {})
}

export function dismissPoll(kind: 'snooze' | 'never'): void {
  void post('/poll/dismiss', { kind }).catch(() => {})
}

export async function submitPoll(body: {
  submissionId: string
  answers: PollAnswers
  email?: string
  wantsCall?: boolean
  contactOnly?: boolean
}): Promise<PollSubmitResult> {
  let res: Response
  try {
    res = await post('/poll/submit', body)
  } catch {
    throw new PollSubmitError('network', 0)
  }
  if (!res.ok) {
    const data = await res.json().catch(() => ({}))
    throw new PollSubmitError(typeof data.error === 'string' ? data.error : `HTTP ${res.status}`, res.status)
  }
  return res.json()
}
