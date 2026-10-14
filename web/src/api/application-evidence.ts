import { fetchJSON } from './client'

export type EvidenceApplication = 'rabbitmq' | 'nats' | 'vault'
export interface EvidenceTarget { namespace: string; pod: string; uid?: string; container?: string }
export interface EvidenceCandidate {
  application: EvidenceApplication
  target: EvidenceTarget & { uid: string; container: string }
  expectedEvidence: string[]
  coverage: string
}
export interface EvidenceCandidates {
  permissionCheckTimedOut?: boolean
  enabled: boolean
  context?: string
  subjectUID?: string
  candidates: EvidenceCandidate[]
  truncated?: boolean
  coverageLimited?: boolean
}
export interface ApplicationEvidenceResult {
  adapter: EvidenceApplication
  target: EvidenceTarget
  observedAt: string
  source: string
  outcome: 'observed' | 'unavailable'
  reason?: string
  httpStatus?: number
  facts?: {
    rabbitmq?: { diskAlarm: boolean; memoryAlarm: boolean }
    vault?: { initialized: boolean; sealed: boolean; standby: boolean; performanceStandby?: boolean }
    nats?: {
      jetStreamEnabled: boolean
      totals?: { accounts: number; streams: number; consumers: number; messages: number }
      consumers: { account: string; stream: string; name: string; pending: number; ackPending: number; redelivered: number }[]
      coverage: string
      returnedAccounts: number
      returnedStreams: number
      returnedConsumers: number
      truncated: boolean
    }
  }
  limitations: string[]
}
export function collectApplicationEvidence(candidate: EvidenceCandidate, context: string, signal: AbortSignal) {
  return fetchJSON<ApplicationEvidenceResult>('/application-evidence/collect', {
    method: 'POST', signal, headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ application: candidate.application, namespace: candidate.target.namespace, pod: candidate.target.pod, uid: candidate.target.uid, context, confirmNetworkAccess: true }),
  })
}
